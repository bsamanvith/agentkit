package weather

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// Fallback asks each source in turn and returns the first forecast, so
// a provider that is down, rate-limited or gone is passed over without the
// app knowing. log (nil for none) hears why a source was passed over.
func Fallback(log *slog.Logger, srcs ...Source) Source {
	if len(srcs) == 1 {
		return srcs[0]
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &fallback{srcs: srcs, log: log}
}

type fallback struct {
	srcs []Source
	log  *slog.Logger
}

// Name implements Source.
func (f *fallback) Name() string {
	names := make([]string, len(f.srcs))
	for i, s := range f.srcs {
		names[i] = s.Name()
	}
	return strings.Join(names, ", then ")
}

// Forecast implements Source.
func (f *fallback) Forecast(ctx context.Context, p geo.Point, days int) (Forecast, error) {
	var errs []error
	for i, s := range f.srcs {
		fc, err := s.Forecast(ctx, p, days)
		if err == nil && len(fc.Days) > 0 {
			return fc, nil
		}
		if ctx.Err() != nil {
			return Forecast{}, ctx.Err()
		}
		if err == nil {
			err = fmt.Errorf("%w: no days", ErrUnavailable)
		}
		errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		if i+1 < len(f.srcs) {
			f.log.WarnContext(ctx, "weather: trying the next provider", "provider", s.Name(), "next", f.srcs[i+1].Name(), "err", err)
		}
	}
	err := errors.Join(errs...)
	if !errors.Is(err, ErrUnavailable) {
		err = fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return Forecast{}, err
}
