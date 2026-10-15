package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store"
)

// CheckCommand checks c for [store.CommandLog.SendCommand] against the rules
// on [store.Command]. It returns a copy with empty Data as nil and Seq and
// the times zero, sharing no memory with c. Errors match [store.ErrInvalid].
func CheckCommand(c store.Command) (store.Command, error) {
	if c.Key == "" {
		return store.Command{}, fmt.Errorf("%w: command has no key", store.ErrInvalid)
	}
	if err := CheckText("command key", c.Key); err != nil {
		return store.Command{}, err
	}
	if c.Type == "" {
		return store.Command{}, fmt.Errorf("%w: command %q has no type", store.ErrInvalid, c.Key)
	}
	if err := CheckText("command type", c.Type); err != nil {
		return store.Command{}, err
	}
	if len(c.Data) > 0 && !json.Valid(c.Data) {
		return store.Command{}, fmt.Errorf("%w: command %q: data is not valid JSON", store.ErrInvalid, c.Key)
	}
	if err := CheckTextBytes("command data", c.Data); err != nil {
		return store.Command{}, err
	}
	return store.Command{Key: c.Key, Type: c.Type, Data: clone(c.Data)}, nil
}

// SentCommand decides a send of c (checked by [CheckCommand]) whose Key the
// run already has as stored: the stored command for a retry, [store.ErrConflict]
// for a different command under the same key.
func SentCommand(runID string, c, stored store.Command) (store.Command, error) {
	if c.Type != stored.Type || !bytes.Equal(c.Data, stored.Data) {
		return store.Command{}, fmt.Errorf("%w: run %q already has a different command with key %q", store.ErrConflict, runID, c.Key)
	}
	return stored, nil
}

// CheckConsume checks the arguments of [store.CommandLog.ConsumeCommands]
// that need no stored state.
func CheckConsume(owner string, through int64) error {
	if err := CheckOwner(owner); err != nil {
		return err
	}
	if through < 1 {
		return fmt.Errorf("%w: consume through command %d, want at least 1", store.ErrInvalid, through)
	}
	return nil
}

// ConsumeError says why owner may not consume run r's commands through
// through at now, when the run has last commands, or returns nil if it may.
func ConsumeError(r store.Run, owner string, through, last int64, now time.Time) error {
	switch {
	case r.Status.Final():
		return fmt.Errorf("%w: run %q", store.ErrFinal, r.ID)
	case Held(r.Lease, now) && r.Lease.Owner != owner:
		return fmt.Errorf("%w: run %q", store.ErrLeased, r.ID)
	case !Held(r.Lease, now):
		return fmt.Errorf("%w: run %q", store.ErrLeaseLost, r.ID)
	case through > last:
		return fmt.Errorf("%w: run %q has %d commands, asked to consume through %d", store.ErrInvalid, r.ID, last, through)
	}
	return nil
}
