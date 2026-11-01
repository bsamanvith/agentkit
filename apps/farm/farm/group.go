package farm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/planting"
)

// ErrNoGroups is returned when the store keeps no groups.
var ErrNoGroups = errors.New("farm: plot groups are not configured")

// Group is plots a farmer keeps together, such as the home farm or the
// fields by the river, with its own plant groups and how they are spread
// over its plots. A plot is in at most one group; a farmer has as many
// groups as they like.
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Plots are farm IDs, in the farmer's order.
	Plots []string `json:"plots"`
	// Distribution is its plant groups and how they are spread; nil until
	// the farmer gives one. It is laid out afresh on the plots when read,
	// so it follows a changed boundary.
	Distribution *planting.Distribution `json:"distribution,omitempty"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

// GroupDraft is a group as the farmer gives it.
type GroupDraft struct {
	Name  string   `json:"name"`
	Plots []string `json:"plots"`
}

// GroupStore keeps groups. Implementations are safe for concurrent use
// and pass farmtest.RunGroupStore. A [Store] that is also a GroupStore
// keeps the service's groups.
type GroupStore interface {
	// CreateGroup saves a new group; ErrInvalid if its ID is taken.
	CreateGroup(ctx context.Context, g Group) error
	// GetGroup returns a group; ErrNotFound if there is none.
	GetGroup(ctx context.Context, id string) (Group, error)
	// ListGroups returns every group, oldest first.
	ListGroups(ctx context.Context) ([]Group, error)
	// UpdateGroup replaces a group; ErrNotFound if there is none.
	UpdateGroup(ctx context.Context, g Group) error
	// DeleteGroup removes a group; ErrNotFound if there is none.
	DeleteGroup(ctx context.Context, id string) error
}

// MaxGroupPlots bounds a group.
const MaxGroupPlots = planting.MaxPlots

// CreateGroup saves a new group of plots. Every plot must exist and be in
// no other group.
func (s *Service) CreateGroup(ctx context.Context, d GroupDraft) (Group, error) {
	if s.groups == nil {
		return Group{}, ErrNoGroups
	}
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	d, err := s.checkGroup(ctx, d, "")
	if err != nil {
		return Group{}, err
	}
	id, err := s.newID()
	if err != nil {
		return Group{}, err
	}
	now := s.clock.Now().UTC()
	g := Group{ID: "group_" + strings.TrimPrefix(id, "farm_"), Name: d.Name, Plots: d.Plots, CreatedAt: now, UpdatedAt: now}
	if err := s.groups.CreateGroup(ctx, g); err != nil {
		return Group{}, err
	}
	return g, nil
}

// UpdateGroup renames a group and replaces its plots.
func (s *Service) UpdateGroup(ctx context.Context, id string, d GroupDraft) (Group, error) {
	if s.groups == nil {
		return Group{}, ErrNoGroups
	}
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	g, err := s.groups.GetGroup(ctx, id)
	if err != nil {
		return Group{}, err
	}
	if d, err = s.checkGroup(ctx, d, id); err != nil {
		return Group{}, err
	}
	g.Name, g.Plots, g.UpdatedAt = d.Name, d.Plots, s.clock.Now().UTC()
	if err := s.groups.UpdateGroup(ctx, g); err != nil {
		return Group{}, err
	}
	return g, nil
}

// SetDistribution replaces a group's distribution; nil clears it. The
// caller checks it.
func (s *Service) SetDistribution(ctx context.Context, id string, d *planting.Distribution) (Group, error) {
	if s.groups == nil {
		return Group{}, ErrNoGroups
	}
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	g, err := s.groups.GetGroup(ctx, id)
	if err != nil {
		return Group{}, err
	}
	g.Distribution, g.UpdatedAt = d, s.clock.Now().UTC()
	if err := s.groups.UpdateGroup(ctx, g); err != nil {
		return Group{}, err
	}
	return g, nil
}

// Group returns a group.
func (s *Service) Group(ctx context.Context, id string) (Group, error) {
	if s.groups == nil {
		return Group{}, ErrNoGroups
	}
	return s.groups.GetGroup(ctx, id)
}

// Groups returns every group, oldest first; none when groups are not
// configured.
func (s *Service) Groups(ctx context.Context) ([]Group, error) {
	if s.groups == nil {
		return []Group{}, nil
	}
	return s.groups.ListGroups(ctx)
}

// DeleteGroup removes a group; its plots stay.
func (s *Service) DeleteGroup(ctx context.Context, id string) error {
	if s.groups == nil {
		return ErrNoGroups
	}
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	return s.groups.DeleteGroup(ctx, id)
}

// GroupOf returns the group a plot is in, if any.
func (s *Service) GroupOf(ctx context.Context, farmID string) (Group, bool, error) {
	gs, err := s.Groups(ctx)
	if err != nil {
		return Group{}, false, err
	}
	for _, g := range gs {
		if slices.Contains(g.Plots, farmID) {
			return g, true, nil
		}
	}
	return Group{}, false, nil
}

func (s *Service) checkGroup(ctx context.Context, d GroupDraft, self string) (GroupDraft, error) {
	d.Name = strings.TrimSpace(d.Name)
	switch {
	case d.Name == "":
		return d, fmt.Errorf("%w: the group needs a name", ErrInvalid)
	case len(d.Name) > MaxNameLen:
		return d, fmt.Errorf("%w: name longer than %d bytes", ErrInvalid, MaxNameLen)
	}
	var plots []string
	for _, p := range d.Plots {
		if !slices.Contains(plots, p) {
			plots = append(plots, p)
		}
	}
	if len(plots) > MaxGroupPlots {
		return d, fmt.Errorf("%w: a group holds at most %d plots", ErrInvalid, MaxGroupPlots)
	}
	groups, err := s.groups.ListGroups(ctx)
	if err != nil {
		return d, err
	}
	for _, p := range plots {
		f, err := s.store.Get(ctx, p)
		if errors.Is(err, ErrNotFound) {
			return d, fmt.Errorf("%w: no plot %s", ErrInvalid, p)
		}
		if err != nil {
			return d, err
		}
		for _, g := range groups {
			if g.ID != self && slices.Contains(g.Plots, p) {
				return d, fmt.Errorf("%w: %s is already in the group %s: a plot is in one group at a time", ErrInvalid, f.Name, g.Name)
			}
		}
	}
	if plots == nil {
		plots = []string{}
	}
	d.Plots = plots
	return d, nil
}

// leaveGroups takes a deleted plot out of its group.
func (s *Service) leaveGroups(ctx context.Context, farmID string) error {
	if s.groups == nil {
		return nil
	}
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	gs, err := s.groups.ListGroups(ctx)
	if err != nil {
		return err
	}
	for _, g := range gs {
		if i := slices.Index(g.Plots, farmID); i >= 0 {
			g.Plots = slices.Delete(g.Plots, i, i+1)
			g.UpdatedAt = s.clock.Now().UTC()
			if err := s.groups.UpdateGroup(ctx, g); err != nil {
				return err
			}
		}
	}
	return nil
}
