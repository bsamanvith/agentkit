package connect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Pending flows live in the vault's BlobStore, so the callback can reach
// any replica: sealed like credentials, under a tenant of their own (the
// callback does not know the tenant until it has read the flow), in a fixed
// number of buckets, each one blob holding the flows whose state hashes to
// it. A state is stored only as its SHA-256. Each write drops the bucket's
// expired flows, and a full bucket drops its oldest, so the store stays
// small however many flows are abandoned.
const (
	// FlowTenant is the tenant the flows are kept under. No real tenant
	// should have this id: its users "flows-00" to "flows-3f" would
	// share the slot FlowSlot with the flows (which the API never lists or
	// writes: only the slots of the server's toolsets).
	FlowTenant = "agentkit-oauth-flows"
	// FlowSlot is the slot the flows are kept at. Slots starting with "_"
	// are refused for toolsets.
	FlowSlot = "_oauth_flows"
	// flowBuckets is how many blobs the flows are spread over, and
	// bucketSize how many each holds at most.
	flowBuckets = 64
	bucketSize  = 64
	// flowKind is the blob's Kind (backends store it as given).
	flowKind credentials.Kind = "oauth_flows"
	// flowWrites is how often a write that loses a race is tried again.
	flowWrites = 8
)

// errNoFlow means no flow is kept under a state.
var errNoFlow = errors.New("connect: no such flow")

// flow is a connect flow between Connect and the callback.
type flow struct {
	Tenant    string    `json:"tenant"`
	User      string    `json:"user,omitempty"`
	Slot      string    `json:"slot"`
	Verifier  string    `json:"verifier"`
	ReturnURL string    `json:"return_url,omitempty"`
	Created   time.Time `json:"created"`
	Expires   time.Time `json:"expires"`
}

type flows struct {
	store  credentials.BlobStore
	sealer *credentials.Sealer
	tenant tenancy.Tenant
}

func newFlows(store credentials.BlobStore, sealer *credentials.Sealer) (*flows, error) {
	t, err := tenancy.NewTenant(FlowTenant)
	if err != nil {
		return nil, err
	}
	return &flows{store: store, sealer: sealer, tenant: t}, nil
}

// locate is the bucket (a user of FlowTenant) and the entry's name for
// state.
func locate(state string) (bucket, id string) {
	sum := sha256.Sum256([]byte(state))
	return fmt.Sprintf("flows-%02x", sum[0]%flowBuckets), hex.EncodeToString(sum[:])
}

// read opens a bucket: its flows by id, and its version (0: none yet). A
// bucket that does not open (sealed under a key the ring no longer has,
// after a rotation, or damaged) reads as empty at its version, so the next
// write replaces it: its flows are lost, which fails closed (their states
// are unknown), instead of every flow hashing to it failing for good.
func (f *flows) read(ctx context.Context, bucket string) (map[string]flow, int64, error) {
	b, err := f.store.Get(ctx, bucket, FlowSlot)
	if errors.Is(err, credentials.ErrNotFound) {
		return map[string]flow{}, 0, nil
	} else if err != nil {
		return nil, 0, err
	}
	plain, _, err := f.sealer.Open(credentials.Key{Tenant: f.tenant, User: bucket, Slot: FlowSlot}, b.Data)
	if err != nil {
		return map[string]flow{}, b.Version, nil
	}
	defer clear(plain)
	m := map[string]flow{}
	if err := json.Unmarshal(plain, &m); err != nil {
		return map[string]flow{}, b.Version, nil
	}
	return m, b.Version, nil
}

// write seals and stores a bucket over version.
func (f *flows) write(ctx context.Context, bucket string, m map[string]flow, version int64, now time.Time) error {
	plain, err := json.Marshal(m)
	if err != nil {
		return err
	}
	defer clear(plain)
	sealed, err := f.sealer.Seal(credentials.Key{Tenant: f.tenant, User: bucket, Slot: FlowSlot}, plain)
	if err != nil {
		return err
	}
	_, err = f.store.Put(ctx, bucket, FlowSlot, credentials.Blob{
		Meta: credentials.Meta{Slot: FlowSlot, User: bucket, Kind: flowKind, UpdatedAt: now},
		Data: sealed,
	}, version)
	return err
}

// update applies change to state's bucket, again if another writer wins
// the race, and writes it back unless change says not to.
func (f *flows) update(ctx context.Context, state string, now time.Time, change func(m map[string]flow, id string) (write bool, err error)) error {
	ctx = tenancy.With(ctx, f.tenant)
	bucket, id := locate(state)
	for range flowWrites {
		m, version, err := f.read(ctx, bucket)
		if err != nil {
			return err
		}
		write, err := change(m, id)
		if err != nil || !write {
			return err
		}
		err = f.write(ctx, bucket, m, version, now)
		if !errors.Is(err, credentials.ErrConflict) && !errors.Is(err, credentials.ErrExists) {
			return err
		}
	}
	return fmt.Errorf("connect: the flows changed under every write, %d times", flowWrites)
}

// put keeps fl under state.
func (f *flows) put(ctx context.Context, state string, fl flow) error {
	return f.update(ctx, state, fl.Created, func(m map[string]flow, id string) (bool, error) {
		prune(m, fl.Created)
		for len(m) >= bucketSize { // drop the oldest
			ids := make([]string, 0, len(m))
			for k := range m {
				ids = append(ids, k)
			}
			oldest := slices.MinFunc(ids, func(a, b string) int { return m[a].Created.Compare(m[b].Created) })
			delete(m, oldest)
		}
		m[id] = fl
		return true, nil
	})
}

// take returns the flow under state and removes it, whatever happens
// next: a state is used once. It is errNoFlow when there is none (never
// kept, used already, or dropped). An expired flow is returned: the caller
// checks the expiry.
func (f *flows) take(ctx context.Context, state string, now time.Time) (flow, error) {
	var got flow
	err := f.update(ctx, state, now, func(m map[string]flow, id string) (bool, error) {
		fl, ok := m[id]
		if !ok {
			return false, errNoFlow
		}
		got = fl
		delete(m, id)
		prune(m, now)
		return true, nil
	})
	return got, err
}

// prune drops the flows that expired before now.
func prune(m map[string]flow, now time.Time) {
	for k, fl := range m {
		if fl.Expires.Before(now) {
			delete(m, k)
		}
	}
}
