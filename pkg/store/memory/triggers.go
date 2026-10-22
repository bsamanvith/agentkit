package memory

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

// memTrigger is a stored trigger and its firings, oldest first.
type memTrigger struct {
	rec     store.TriggerRecord
	firings []store.Firing
	lastSeq int64
}

// TriggerAdmin returns the [store.TriggerAdmin] view of s.
func TriggerAdmin(s *Store) store.TriggerAdmin { return admin{s} }

// PutTrigger implements [store.TriggerStore].
func (s *Store) PutTrigger(ctx context.Context, t store.TriggerRecord, ifVersion int64) (store.TriggerRecord, error) {
	k, err := scope(ctx, t.ID)
	if err != nil {
		return store.TriggerRecord{}, err
	}
	t, err = backend.CheckTrigger(t, ifVersion)
	if err != nil {
		return store.TriggerRecord{}, err
	}
	now := s.now().UTC()
	if err := backend.CheckTime("the clock's time", now); err != nil {
		return store.TriggerRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.triggers[k]
	var have int64
	if old != nil {
		have = old.rec.Version
	}
	if err := backend.PutTriggerError(t.ID, ifVersion, have); err != nil {
		return store.TriggerRecord{}, err
	}
	if t.HookID != "" {
		for ok, o := range s.triggers {
			if ok != k && o.rec.HookID == t.HookID {
				return store.TriggerRecord{}, backend.HookConflict(t.ID, t.HookID)
			}
		}
	}
	t.Version, t.CreatedAt, t.UpdatedAt = have+1, now, now
	if old == nil {
		s.triggers[k] = &memTrigger{rec: t}
		return cloneTrigger(t), nil
	}
	t.CreatedAt = old.rec.CreatedAt
	if now.Before(old.rec.UpdatedAt) {
		t.UpdatedAt = old.rec.UpdatedAt
	}
	old.rec = t
	return cloneTrigger(t), nil
}

// GetTrigger implements [store.TriggerStore].
func (s *Store) GetTrigger(ctx context.Context, id string) (store.TriggerRecord, error) {
	k, err := scope(ctx, id)
	if err != nil {
		return store.TriggerRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.triggers[k]
	if !ok {
		return store.TriggerRecord{}, fmt.Errorf("%w: trigger %q", store.ErrNotFound, id)
	}
	return cloneTrigger(t.rec), nil
}

// ListTriggers implements [store.TriggerStore].
func (s *Store) ListTriggers(ctx context.Context) ([]store.TriggerRecord, error) {
	k, err := scope(ctx, "")
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []store.TriggerRecord{}
	for tk, t := range s.triggers {
		if tk.tenant == k.tenant {
			out = append(out, cloneTrigger(t.rec))
		}
	}
	// sort.Slice, not slices.SortFunc: no sort instantiated for the type
	// in every binary that links the store.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// DeleteTrigger implements [store.TriggerStore].
func (s *Store) DeleteTrigger(ctx context.Context, id string, ifVersion int64) error {
	k, err := scope(ctx, id)
	if err != nil {
		return err
	}
	if err := backend.CheckDeleteTrigger(id, ifVersion); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var have int64
	if t := s.triggers[k]; t != nil {
		have = t.rec.Version
	}
	if err := backend.DeleteTriggerError(id, ifVersion, have); err != nil {
		return err
	}
	delete(s.triggers, k)
	return nil
}

// StartFiring implements [store.TriggerStore].
func (s *Store) StartFiring(ctx context.Context, triggerID string, f store.Firing, due, next time.Time) (store.Firing, error) {
	k, err := scope(ctx, triggerID)
	if err != nil {
		return store.Firing{}, err
	}
	f, due, next, err = backend.CheckStartFiring(triggerID, f, due, next)
	if err != nil {
		return store.Firing{}, err
	}
	now := s.now().UTC()
	if err := backend.CheckTime("the clock's time", now); err != nil {
		return store.Firing{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.triggers[k]
	if !ok {
		return store.Firing{}, fmt.Errorf("%w: trigger %q", store.ErrNotFound, triggerID)
	}
	i := slices.IndexFunc(t.firings, func(o store.Firing) bool { return o.ID == f.ID })
	var have store.FiringState
	if i >= 0 {
		have = t.firings[i].State
	}
	if err := backend.StartFiringError(t.rec, f.ID, i >= 0, have, due); err != nil {
		return store.Firing{}, err
	}
	if i >= 0 {
		t.firings = slices.Delete(t.firings, i, i+1)
	}
	t.lastSeq++
	f.Seq, f.At = t.lastSeq, now
	t.firings = append(t.firings, f)
	// The oldest beyond MaxFirings go, unless a replay of them could still
	// be accepted (FiringKeep).
	cutoff, drop := now.Add(-store.FiringKeep), 0
	for over := len(t.firings) - store.MaxFirings; drop < over && t.firings[drop].At.Before(cutoff); {
		drop++
	}
	t.firings = slices.Delete(t.firings, 0, drop)
	if !due.IsZero() {
		t.rec.NextFireAt = next
	}
	return f, nil
}

// EndFiring implements [store.TriggerStore].
func (s *Store) EndFiring(ctx context.Context, triggerID, firingID string, end store.FiringEnd) (store.Firing, error) {
	k, err := scope(ctx, triggerID)
	if err != nil {
		return store.Firing{}, err
	}
	if err := backend.CheckEndFiring(triggerID, firingID, end); err != nil {
		return store.Firing{}, err
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.triggers[k]
	i := -1
	if ok {
		i = slices.IndexFunc(t.firings, func(o store.Firing) bool { return o.ID == firingID })
	}
	if i < 0 {
		return store.Firing{}, fmt.Errorf("%w: firing %q of trigger %q", store.ErrNotFound, firingID, triggerID)
	}
	f := &t.firings[i]
	if err := backend.EndFiringError(triggerID, firingID, f.State); err != nil {
		return store.Firing{}, err
	}
	f.State, f.RunID, f.Reason, f.EndedAt = end.State, end.RunID, end.Reason, now
	if f.EndedAt.Before(f.At) {
		f.EndedAt = f.At // times never go backwards
	}
	return *f, nil
}

// ListFirings implements [store.TriggerStore].
func (s *Store) ListFirings(ctx context.Context, triggerID string, limit int) ([]store.Firing, error) {
	k, err := scope(ctx, triggerID)
	if err != nil {
		return nil, err
	}
	n, err := backend.FiringLimit(limit)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.triggers[k]
	if !ok {
		return nil, fmt.Errorf("%w: trigger %q", store.ErrNotFound, triggerID)
	}
	out := []store.Firing{}
	for i := len(t.firings) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, t.firings[i])
	}
	return out, nil
}

// DueTriggers implements [store.TriggerAdmin].
func (a admin) DueTriggers(ctx context.Context, now time.Time, limit int) ([]store.TenantTrigger, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n, err := backend.FiringLimit(limit)
	if err != nil {
		return nil, err
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	out := []store.TenantTrigger{}
	for k, t := range a.s.triggers {
		if backend.TriggerDue(t.rec, now) {
			out = append(out, store.TenantTrigger{Tenant: k.tenant, Trigger: cloneTrigger(t.rec)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		x, y := out[i], out[j]
		return cmp.Or(x.Trigger.NextFireAt.Compare(y.Trigger.NextFireAt),
			strings.Compare(x.Tenant.ID(), y.Tenant.ID()), strings.Compare(x.Trigger.ID, y.Trigger.ID)) < 0
	})
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// TriggerByHook implements [store.TriggerAdmin].
func (a admin) TriggerByHook(ctx context.Context, hookID string) (store.TenantTrigger, error) {
	if err := ctx.Err(); err != nil {
		return store.TenantTrigger{}, err
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	for k, t := range a.s.triggers {
		if hookID != "" && t.rec.HookID == hookID {
			return store.TenantTrigger{Tenant: k.tenant, Trigger: cloneTrigger(t.rec)}, nil
		}
	}
	return store.TenantTrigger{}, fmt.Errorf("%w: no trigger has hook %q", store.ErrNotFound, hookID)
}

func cloneTrigger(t store.TriggerRecord) store.TriggerRecord {
	t.Data = slices.Clone(t.Data)
	return t
}
