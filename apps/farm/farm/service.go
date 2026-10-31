package farm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Draft is a farm as the farmer gives it: everything but what the service
// fills in.
type Draft struct {
	Name     string   `json:"name"`
	Declared *Area    `json:"declared,omitempty"`
	Boundary Boundary `json:"boundary"`
}

// MaxNameLen bounds a farm's name, in bytes.
const MaxNameLen = 200

// Service saves farms, verified. It is safe for concurrent use.
type Service struct {
	store    Store
	verifier *Verifier
	clock    clock.Clock
	rand     io.Reader
}

// ServiceOption configures [NewService].
type ServiceOption func(*Service)

// WithServiceClock sets the clock that stamps farms (default the machine's).
func WithServiceClock(c clock.Clock) ServiceOption { return func(s *Service) { s.clock = c } }

// WithRandom sets where IDs come from (default crypto/rand).
func WithRandom(r io.Reader) ServiceOption { return func(s *Service) { s.rand = r } }

// NewService returns a Service over a store and a verifier.
func NewService(store Store, verifier *Verifier, opts ...ServiceOption) (*Service, error) {
	if store == nil || verifier == nil {
		return nil, fmt.Errorf("%w: NewService needs a store and a verifier", ErrInvalid)
	}
	s := &Service{store: store, verifier: verifier, clock: clock.Real(), rand: rand.Reader}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Verify checks a draft against the saved farms without saving it; except
// is a farm to leave out of the overlap check (the one being edited), or "".
func (s *Service) Verify(ctx context.Context, d Draft, except string) (Report, error) {
	if err := checkDraft(d, false); err != nil {
		return Report{}, err
	}
	others, err := s.others(ctx, except)
	if err != nil {
		return Report{}, err
	}
	return s.verifier.Verify(ctx, Request{Boundary: d.Boundary, Declared: d.Declared, Others: others}), nil
}

// Create verifies a draft and saves it as a new farm. An invalid boundary
// is refused with an [*InvalidError].
func (s *Service) Create(ctx context.Context, d Draft) (Farm, error) {
	d.Name = strings.TrimSpace(d.Name)
	if err := checkDraft(d, true); err != nil {
		return Farm{}, err
	}
	rep, err := s.Verify(ctx, d, "")
	if err != nil {
		return Farm{}, err
	}
	if rep.Status == StatusInvalid {
		return Farm{}, &InvalidError{Report: rep}
	}
	id, err := s.newID()
	if err != nil {
		return Farm{}, err
	}
	now := s.clock.Now().UTC()
	f := Farm{ID: id, Name: d.Name, Declared: d.Declared, Boundary: d.Boundary, Report: rep, CreatedAt: now, UpdatedAt: now}
	if err := s.store.Create(ctx, f); err != nil {
		return Farm{}, err
	}
	return f, nil
}

// Update replaces a farm's name, declared size and boundary, verified again.
func (s *Service) Update(ctx context.Context, id string, d Draft) (Farm, error) {
	d.Name = strings.TrimSpace(d.Name)
	if err := checkDraft(d, true); err != nil {
		return Farm{}, err
	}
	f, err := s.store.Get(ctx, id)
	if err != nil {
		return Farm{}, err
	}
	rep, err := s.Verify(ctx, d, id)
	if err != nil {
		return Farm{}, err
	}
	if rep.Status == StatusInvalid {
		return Farm{}, &InvalidError{Report: rep}
	}
	f.Name, f.Declared, f.Boundary, f.Report = d.Name, d.Declared, d.Boundary, rep
	f.UpdatedAt = s.clock.Now().UTC()
	if err := s.store.Update(ctx, f); err != nil {
		return Farm{}, err
	}
	return f, nil
}

// Get returns a farm.
func (s *Service) Get(ctx context.Context, id string) (Farm, error) { return s.store.Get(ctx, id) }

// List returns every farm, oldest first.
func (s *Service) List(ctx context.Context) ([]Farm, error) { return s.store.List(ctx) }

// Delete removes a farm.
func (s *Service) Delete(ctx context.Context, id string) error { return s.store.Delete(ctx, id) }

func (s *Service) others(ctx context.Context, except string) ([]Farm, error) {
	all, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, f := range all {
		if f.ID != except {
			out = append(out, f)
		}
	}
	return out, nil
}

func (s *Service) newID() (string, error) {
	b := make([]byte, 8)
	if _, err := io.ReadFull(s.rand, b); err != nil {
		return "", fmt.Errorf("farm: new ID: %w", err)
	}
	return "farm_" + hex.EncodeToString(b), nil
}

// checkDraft checks what verification does not; a name only if needName.
func checkDraft(d Draft, needName bool) error {
	name := strings.TrimSpace(d.Name)
	switch {
	case needName && name == "":
		return fmt.Errorf("%w: the farm needs a name", ErrInvalid)
	case len(name) > MaxNameLen:
		return fmt.Errorf("%w: name longer than %d bytes", ErrInvalid, MaxNameLen)
	}
	switch d.Boundary.Source {
	case "", SourceMap, SourceGPS, SourceTyped:
	default:
		return fmt.Errorf("%w: unknown boundary source %q", ErrInvalid, d.Boundary.Source)
	}
	if d.Declared != nil {
		if _, err := d.Declared.SquareMetres(); err != nil {
			return err
		}
	}
	return nil
}
