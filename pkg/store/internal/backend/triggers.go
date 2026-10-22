package backend

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store"
)

var triggerName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// maxFiringID is the longest [store.Firing.ID].
const maxFiringID = 128

// CheckTriggerID refuses an ID that breaks the rule on [store.TriggerRecord].
func CheckTriggerID(id string) error {
	if !triggerName.MatchString(id) {
		return fmt.Errorf("%w: trigger id %q must match %s", store.ErrInvalid, id, triggerName)
	}
	return nil
}

// CheckTrigger checks t and ifVersion for [store.TriggerStore.PutTrigger]
// against the rules on [store.TriggerRecord]. It returns a copy with only
// the fields a put writes, sharing no memory with t, and NextFireAt rounded
// to the store's precision (nanoseconds, no monotonic reading). Errors match
// [store.ErrInvalid].
func CheckTrigger(t store.TriggerRecord, ifVersion int64) (store.TriggerRecord, error) {
	if err := CheckTriggerID(t.ID); err != nil {
		return store.TriggerRecord{}, err
	}
	switch {
	case ifVersion < 0:
		return store.TriggerRecord{}, fmt.Errorf("%w: trigger %q: negative version %d", store.ErrInvalid, t.ID, ifVersion)
	case len(t.Data) == 0:
		return store.TriggerRecord{}, fmt.Errorf("%w: trigger %q has no data", store.ErrInvalid, t.ID)
	case len(t.Data) > store.MaxTriggerBytes:
		return store.TriggerRecord{}, fmt.Errorf("%w: trigger %q: data of %d bytes, more than %d", store.ErrInvalid, t.ID, len(t.Data), store.MaxTriggerBytes)
	case !json.Valid(t.Data):
		return store.TriggerRecord{}, fmt.Errorf("%w: trigger %q: data is not valid JSON", store.ErrInvalid, t.ID)
	case t.HookID != "" && !triggerName.MatchString(t.HookID):
		return store.TriggerRecord{}, fmt.Errorf("%w: trigger %q: hook id %q must match %s", store.ErrInvalid, t.ID, t.HookID, triggerName)
	}
	if err := CheckTextBytes("trigger data", t.Data); err != nil {
		return store.TriggerRecord{}, err
	}
	if err := CheckTime("next firing time", t.NextFireAt); err != nil {
		return store.TriggerRecord{}, err
	}
	return store.TriggerRecord{ID: t.ID, Data: clone(t.Data), HookID: t.HookID, Paused: t.Paused, NextFireAt: storedTime(t.NextFireAt)}, nil
}

// CheckDeleteTrigger checks the arguments of
// [store.TriggerStore.DeleteTrigger].
func CheckDeleteTrigger(id string, ifVersion int64) error {
	if err := CheckTriggerID(id); err != nil {
		return err
	}
	if ifVersion < 0 {
		return fmt.Errorf("%w: trigger %q: negative version %d", store.ErrInvalid, id, ifVersion)
	}
	return nil
}

// PutTriggerError is the error of a put of version ifVersion of a trigger
// whose stored version is have (0: none stored), or nil when the put may go
// ahead.
func PutTriggerError(id string, ifVersion, have int64) error {
	switch {
	case ifVersion == 0 && have != 0:
		return fmt.Errorf("%w: trigger %q exists", store.ErrConflict, id)
	case ifVersion != 0 && have == 0:
		return fmt.Errorf("%w: trigger %q", store.ErrNotFound, id)
	case ifVersion != have:
		return fmt.Errorf("%w: trigger %q is at version %d, not %d", store.ErrConflict, id, have, ifVersion)
	}
	return nil
}

// DeleteTriggerError is [PutTriggerError] for a delete: ifVersion 0 removes
// any version.
func DeleteTriggerError(id string, ifVersion, have int64) error {
	switch {
	case have == 0:
		return fmt.Errorf("%w: trigger %q", store.ErrNotFound, id)
	case ifVersion != 0 && ifVersion != have:
		return fmt.Errorf("%w: trigger %q is at version %d, not %d", store.ErrConflict, id, have, ifVersion)
	}
	return nil
}

// HookConflict is the error of a put whose hook ID another trigger has.
func HookConflict(id, hookID string) error {
	return fmt.Errorf("%w: trigger %q: another trigger has hook id %q", store.ErrConflict, id, hookID)
}

// CheckStartFiring checks the arguments of
// [store.TriggerStore.StartFiring]. It returns the firing as it is stored
// before the store sets Seq and At: only ID and ScheduledFor, pending, and
// due and next rounded like it.
func CheckStartFiring(triggerID string, f store.Firing, due, next time.Time) (store.Firing, time.Time, time.Time, error) {
	if err := CheckTriggerID(triggerID); err != nil {
		return store.Firing{}, time.Time{}, time.Time{}, err
	}
	if err := checkFiringID(f.ID); err != nil {
		return store.Firing{}, time.Time{}, time.Time{}, err
	}
	for what, t := range map[string]time.Time{"scheduled time": f.ScheduledFor, "due time": due, "next firing time": next} {
		if err := CheckTime(what, t); err != nil {
			return store.Firing{}, time.Time{}, time.Time{}, err
		}
	}
	switch {
	case due.IsZero() && !next.IsZero():
		return store.Firing{}, time.Time{}, time.Time{}, fmt.Errorf("%w: firing %q: a next firing time with no due time", store.ErrInvalid, f.ID)
	case !next.IsZero() && !next.After(due):
		return store.Firing{}, time.Time{}, time.Time{}, fmt.Errorf("%w: firing %q: next firing time %v is not after the due time %v", store.ErrInvalid, f.ID, next, due)
	}
	return store.Firing{ID: f.ID, ScheduledFor: storedTime(f.ScheduledFor), State: store.FiringPending}, storedTime(due), storedTime(next), nil
}

func checkFiringID(id string) error {
	if err := CheckID("firing", id); err != nil {
		return err
	}
	if len(id) > maxFiringID {
		return fmt.Errorf("%w: firing id of %d bytes, more than %d", store.ErrInvalid, len(id), maxFiringID)
	}
	return nil
}

// CheckEndFiring checks the arguments of [store.TriggerStore.EndFiring].
func CheckEndFiring(triggerID, firingID string, end store.FiringEnd) error {
	if err := CheckTriggerID(triggerID); err != nil {
		return err
	}
	if err := checkFiringID(firingID); err != nil {
		return err
	}
	switch end.State {
	case store.FiringStarted, store.FiringSkipped, store.FiringFailed:
	default:
		return fmt.Errorf("%w: firing %q: %q is not a final state", store.ErrInvalid, firingID, end.State)
	}
	if err := CheckText("firing run id", end.RunID); err != nil {
		return err
	}
	return CheckText("firing reason", end.Reason)
}

// StartFiringError is the error of a start of firing id (exists: whether the
// trigger has one, in state have) of trigger t with due time due, or nil when
// it may go ahead.
func StartFiringError(t store.TriggerRecord, id string, exists bool, have store.FiringState, due time.Time) error {
	switch {
	case exists && have != store.FiringFailed:
		return fmt.Errorf("%w: trigger %q has fired %q", store.ErrConflict, t.ID, id)
	case due.IsZero():
		return nil
	case t.Paused:
		return fmt.Errorf("%w: trigger %q is paused", store.ErrConflict, t.ID)
	case !t.NextFireAt.Equal(due):
		return fmt.Errorf("%w: trigger %q is due at %v, not %v", store.ErrConflict, t.ID, t.NextFireAt, due)
	}
	return nil
}

// EndFiringError is the error of ending a firing in state have, or nil.
func EndFiringError(triggerID, firingID string, have store.FiringState) error {
	if have != store.FiringPending {
		return fmt.Errorf("%w: firing %q of trigger %q is %s", store.ErrFinal, firingID, triggerID, have)
	}
	return nil
}

// FiringLimit returns the number of firings ListFirings returns for limit.
func FiringLimit(limit int) (int, error) { return Limit(store.Page{Limit: limit}) }

// TriggerDue reports whether t is due at now.
func TriggerDue(t store.TriggerRecord, now time.Time) bool {
	return !t.Paused && !t.NextFireAt.IsZero() && !t.NextFireAt.After(now)
}

// storedTime is t as a store gives it back: UTC, with no monotonic reading.
func storedTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.Round(0).UTC()
}
