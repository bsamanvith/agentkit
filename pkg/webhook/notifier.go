package webhook

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Defaults of the options.
const (
	// DefaultWindow is how long after its first attempt a notification is
	// tried again before the notifier gives up.
	DefaultWindow = 24 * time.Hour
	// DefaultBaseDelay and DefaultMaxDelay bound the wait after a failed
	// attempt: the n-th failure waits min(DefaultMaxDelay,
	// DefaultBaseDelay·2^(n-1)).
	DefaultBaseDelay = 5 * time.Second
	DefaultMaxDelay  = time.Hour
	// DefaultTimeout bounds one attempt: its payload, its post and the answer.
	DefaultTimeout = 15 * time.Second
	// DefaultInterval is the wait between the passes of [Notifier.Run].
	DefaultInterval = 2 * time.Second
	// DefaultLease is how long a claimed notification stays the notifier's.
	// It must outlast an attempt.
	DefaultLease = 2 * time.Minute
	// DefaultWorkers is how many deliveries a pass makes at once.
	DefaultWorkers = 4
)

// ErrInvalidConfig means [New] was given a missing argument or an unusable
// option.
var ErrInvalidConfig = errors.New("invalid webhook notifier configuration")

// Admin is the part of [store.Admin] a notifier uses: notifications of every
// tenant.
type Admin interface {
	ClaimNotification(ctx context.Context, owner string, ttl time.Duration) (store.TenantNotification, error)
	RecordDelivery(ctx context.Context, tenant tenancy.Tenant, runID, owner string, d store.Delivery) (store.Notification, error)
}

// Payload returns the body posted for the end of run runID; ctx carries the
// run's tenant.
type Payload func(ctx context.Context, runID string) ([]byte, error)

// Secrets returns a tenant's signing secret.
type Secrets func(tenant tenancy.Tenant) ([]byte, error)

// Notifier delivers the notifications of ended runs. Make one with [New]; it
// is safe for concurrent use.
type Notifier struct {
	admin   Admin
	payload Payload
	secrets Secrets

	client   *http.Client
	clk      clock.Clock
	log      *slog.Logger
	owner    string
	lease    time.Duration
	interval time.Duration
	window   time.Duration
	base     time.Duration
	maxDelay time.Duration
	timeout  time.Duration
	workers  int
}

// Option configures [New].
type Option func(*Notifier) error

// WithClient sets the HTTP client deliveries are posted with (default one
// that follows no redirect). Give it the SSRF guard's client
// (netguard.Guard.Client) in a deployment: the URLs are the users'. A
// redirect is never followed; it counts as a failed attempt.
func WithClient(c *http.Client) Option {
	return func(n *Notifier) error {
		if c == nil {
			return fmt.Errorf("%w: nil client", ErrInvalidConfig)
		}
		own := *c
		own.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		n.client = &own
		return nil
	}
}

// WithClock sets the clock of attempts, delays and passes (default
// [clock.Real]). The store's clock decides leases and what is due.
func WithClock(c clock.Clock) Option {
	return func(n *Notifier) error {
		if c == nil {
			return fmt.Errorf("%w: nil clock", ErrInvalidConfig)
		}
		n.clk = c
		return nil
	}
}

// WithLogger sets the logger (default: discard). It logs each attempt.
func WithLogger(l *slog.Logger) Option {
	return func(n *Notifier) error {
		if l == nil {
			return fmt.Errorf("%w: nil logger", ErrInvalidConfig)
		}
		n.log = l
		return nil
	}
}

// WithOwner sets the label of the notifier's leases (default a random one).
func WithOwner(owner string) Option {
	return func(n *Notifier) error {
		if owner == "" {
			return fmt.Errorf("%w: empty owner", ErrInvalidConfig)
		}
		n.owner = owner
		return nil
	}
}

// WithLease sets how long a claimed notification stays the notifier's
// (default [DefaultLease]); it must be longer than the attempt timeout.
func WithLease(d time.Duration) Option {
	return func(n *Notifier) error { n.lease = d; return nil }
}

// WithInterval sets the wait between passes of [Notifier.Run] (default
// [DefaultInterval]).
func WithInterval(d time.Duration) Option {
	return func(n *Notifier) error { n.interval = d; return nil }
}

// WithWindow sets how long after its first attempt a notification is tried
// again (default [DefaultWindow]).
func WithWindow(d time.Duration) Option {
	return func(n *Notifier) error { n.window = d; return nil }
}

// WithBackoff sets the delay after the first failed attempt, which doubles
// after each failure up to max (default [DefaultBaseDelay] and
// [DefaultMaxDelay]).
func WithBackoff(base, max time.Duration) Option {
	return func(n *Notifier) error { n.base, n.maxDelay = base, max; return nil }
}

// WithTimeout bounds one attempt (default [DefaultTimeout]).
func WithTimeout(d time.Duration) Option {
	return func(n *Notifier) error { n.timeout = d; return nil }
}

// WithWorkers sets how many deliveries a pass makes at once (default
// [DefaultWorkers]).
func WithWorkers(k int) Option {
	return func(n *Notifier) error { n.workers = k; return nil }
}

// New returns a notifier over admin, the store's cross-tenant view, that
// posts what payload gives, signed with the tenant's secret from secrets.
func New(admin Admin, payload Payload, secrets Secrets, opts ...Option) (*Notifier, error) {
	if admin == nil || payload == nil || secrets == nil {
		return nil, fmt.Errorf("%w: nil admin, payload or secrets", ErrInvalidConfig)
	}
	n := &Notifier{
		admin: admin, payload: payload, secrets: secrets,
		clk: clock.Real(), log: slog.New(slog.DiscardHandler),
		lease: DefaultLease, interval: DefaultInterval, window: DefaultWindow,
		base: DefaultBaseDelay, maxDelay: DefaultMaxDelay, timeout: DefaultTimeout, workers: DefaultWorkers,
	}
	if err := WithClient(&http.Client{})(n); err != nil {
		return nil, err
	}
	for _, opt := range opts {
		if err := opt(n); err != nil {
			return nil, err
		}
	}
	switch {
	case n.timeout <= 0, n.interval <= 0, n.window <= 0, n.base <= 0, n.maxDelay < n.base, n.workers < 1:
		return nil, fmt.Errorf("%w: timeout, interval, window, delays and workers must be positive, the max delay at least the first", ErrInvalidConfig)
	case n.lease <= n.timeout:
		return nil, fmt.Errorf("%w: the lease (%v) must outlast an attempt (%v)", ErrInvalidConfig, n.lease, n.timeout)
	}
	if n.owner == "" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		n.owner = "webhook-" + hex.EncodeToString(b)
	}
	return n, nil
}

// Report counts what one pass of [Notifier.Deliver] did.
type Report struct {
	// Delivered notifications were answered with a 2xx.
	Delivered int
	// Retrying notifications failed and are tried again later.
	Retrying int
	// Failed notifications failed for the last time: given up.
	Failed int
}

// Deliver makes one pass: its workers claim and deliver due notifications
// until none is due or ctx is done. Errors of the store are joined into the
// returned error; a failed attempt is not an error, it is recorded.
func (n *Notifier) Deliver(ctx context.Context) (Report, error) {
	var (
		mu   sync.Mutex
		rep  Report
		errs []error
		wg   sync.WaitGroup
	)
	for range n.workers {
		wg.Go(func() {
			for ctx.Err() == nil {
				tn, err := n.admin.ClaimNotification(ctx, n.owner, n.lease)
				if errors.Is(err, store.ErrNotFound) {
					return
				}
				if err != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("claim a notification: %w", err))
					mu.Unlock()
					return
				}
				state, err := n.deliver(ctx, tn)
				mu.Lock()
				switch {
				case err != nil:
					errs = append(errs, err)
				case state == store.NotificationDelivered:
					rep.Delivered++
				case state == store.NotificationFailed:
					rep.Failed++
				default:
					rep.Retrying++
				}
				mu.Unlock()
				if err != nil {
					return
				}
			}
		})
	}
	wg.Wait()
	return rep, errors.Join(errs...)
}

// Run makes a pass at once and then one every interval, by the notifier's
// clock, until ctx is done; it then returns nil. Errors of a pass are given
// to report, if set, and do not stop the loop.
func (n *Notifier) Run(ctx context.Context, report func(Report, error)) error {
	for {
		rep, err := n.Deliver(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if report != nil && (err != nil || rep != (Report{})) {
			report(rep, err)
		}
		t := n.clk.NewTimer(n.interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C():
		}
	}
}

// deliver makes one attempt at a claimed notification and records it.
func (n *Notifier) deliver(ctx context.Context, tn store.TenantNotification) (store.NotificationState, error) {
	nt := tn.Notification
	attempt := len(nt.Attempts) + 1
	at := n.clk.Now()
	status, aerr := n.post(tenancy.With(ctx, tn.Tenant), tn.Tenant, nt, attempt, at)
	d := store.Delivery{Attempt: store.DeliveryAttempt{At: at, Status: status}, State: store.NotificationDelivered}
	if aerr != nil {
		d.Attempt.Error = aerr.Error()
		first := at
		if len(nt.Attempts) > 0 {
			first = nt.Attempts[0].At
		}
		next := at.Add(n.delay(attempt))
		if next.After(first.Add(n.window)) {
			d.State = store.NotificationFailed
		} else {
			d.State, d.NextAttemptAt = store.NotificationPending, next
		}
	}
	// Recorded even if ctx ended meanwhile: the attempt was made.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), n.timeout)
	defer cancel()
	if _, err := n.admin.RecordDelivery(rctx, tn.Tenant, nt.RunID, n.owner, d); err != nil {
		return "", fmt.Errorf("record the delivery of run %q: %w", nt.RunID, err)
	}
	attrs := []any{"tenant", tn.Tenant.ID(), "run_id", nt.RunID, "attempt", attempt, "status", status, "state", string(d.State)}
	if aerr != nil {
		n.log.Warn("webhook attempt failed", append(attrs, "err", aerr)...)
	} else {
		n.log.Info("webhook delivered", attrs...)
	}
	return d.State, nil
}

// delay is the wait after the attempt-th failure.
func (n *Notifier) delay(attempt int) time.Duration {
	d := n.base
	for i := 1; i < attempt && d < n.maxDelay; i++ {
		d *= 2
	}
	return min(d, n.maxDelay)
}

// maxAnswer is how much of an answer's body is read, then dropped.
const maxAnswer = 64 << 10

// post sends one attempt. It returns the answer's status (0 for none) and
// why the attempt failed, or nil for a 2xx.
func (n *Notifier) post(ctx context.Context, tenant tenancy.Tenant, nt store.Notification, attempt int, at time.Time) (int, error) {
	// The whole attempt, its payload included, is bounded by the timeout,
	// which the lease outlasts: no other notifier takes the notification
	// over while this attempt may still post it.
	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	body, err := n.payload(ctx, nt.RunID)
	if err != nil {
		return 0, fmt.Errorf("making the payload: %w", err)
	}
	secret, err := n.secrets(tenant)
	if err != nil {
		return 0, fmt.Errorf("the tenant's secret: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nt.URL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("the URL: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "agentkit-webhook/1")
	req.Header.Set(HeaderID, nt.RunID)
	req.Header.Set(HeaderAttempt, strconv.Itoa(attempt))
	req.Header.Set(HeaderSignature, Sign(secret, at, body))
	resp, err := n.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}
