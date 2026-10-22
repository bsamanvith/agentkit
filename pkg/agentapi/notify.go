package agentapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/stream"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// The caller's notification stream (T090; E18, "Streams to subscribe to"):
// GET /v1/events. The process executing a run publishes its status changes
// on the bus topic of the run's user ([store.Run.User], the user the
// request that started it acted for), in the run's tenant: when an
// execution starts and when it stops, and for a cancel of a run nobody
// executes. Any replica on the same bus serves the stream. Notifications are
// not stored: the bus keeps an hour of them for a client that reconnects
// with Last-Event-ID, and the stream says resync when that is not enough.

// anonymous is the bus's user of the notification stream of the runs
// started for no user, followed by a caller with no user. Every user's has
// the prefix "u.", which it does not, so no user id maps to it, whatever an
// authenticator lets a user id be.
const anonymous = "none"

// notifyUser is the bus's user of the notification stream of user u.
func notifyUser(u string) string {
	if u == "" {
		return anonymous
	}
	return "u." + u
}

// A notification stream has no end, and each one open holds a subscription
// of the bus (on Redis, a connection of the subscriptions' pool, which the
// run streams and command inboxes share), so a server serves at most
// maxNotificationStreams at once, and at most maxCallerNotificationStreams
// to one caller (a user, or the callers with no user, of a tenant): a
// console open in several tabs, not a client that opens them in a loop.
const (
	maxNotificationStreams       = 500
	maxCallerNotificationStreams = 8
)

// notifyCaller is whose notification stream it is: a bus user in a tenant.
type notifyCaller struct{ tenant, user string }

// openNotifications counts a stream of c open, and returns the function that
// counts it closed, or the error to answer when the server, or c, has as
// many open as it may.
func (s *Server) openNotifications(c notifyCaller) (func(), *Error) {
	s.nmu.Lock()
	defer s.nmu.Unlock()
	switch {
	case s.nOpen >= maxNotificationStreams:
		return nil, &Error{Code: CodeOverCapacity, Retryable: true, Message: fmt.Sprintf("%d notification streams are open, the most this server serves; retry later", maxNotificationStreams)}
	case s.nByCaller[c] >= maxCallerNotificationStreams:
		return nil, &Error{Code: CodeOverCapacity, Retryable: true, Message: fmt.Sprintf("%d notification streams of this caller are open, the most one caller may have on a server; close one", maxCallerNotificationStreams)}
	}
	if s.nByCaller == nil {
		s.nByCaller = map[notifyCaller]int{}
	}
	s.nOpen++
	s.nByCaller[c]++
	return func() {
		s.nmu.Lock()
		defer s.nmu.Unlock()
		s.nOpen--
		if s.nByCaller[c]--; s.nByCaller[c] == 0 {
			delete(s.nByCaller, c)
		}
	}, nil
}

// notify publishes n on the notification stream of run's user, in ctx's
// tenant. A failure is logged: a notification is a hint, and a client that
// misses one refetches after its next resync or reconnect.
func (s *Server) notify(ctx context.Context, run store.Run, n UserEvent) {
	ctx = context.WithoutCancel(ctx) // the execution may be stopping: its end is still news
	n.V, n.At = WireVersion, s.cfg.clock.Now()
	n.RunID, n.ConversationID = run.ID, run.ConversationID
	data, err := json.Marshal(n)
	if err == nil {
		_, err = s.notifier.Notify(ctx, notifyUser(run.User), n.Type, data)
	}
	if err != nil {
		s.log.Warn("sending a notification failed", "run_id", run.ID, "type", n.Type, "err", err)
	}
}

// notifyStatus publishes run's status, and the code of the error it stopped
// with if any.
func (s *Server) notifyStatus(ctx context.Context, run store.Run, status string, e *Error) {
	n := UserEvent{Type: TypeRunStatus, Status: status, Background: !run.Interactive}
	if e != nil {
		n.Code = e.Code
	}
	s.notify(ctx, run, n)
}

// userEvents serves GET /v1/events: the caller's notifications from the one
// after Last-Event-ID (from now without it), as they come, until the client
// goes away or the server shuts down.
func (s *Server) userEvents(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	var from bus.ID
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		id, err := bus.ParseID(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "Last-Event-ID must be the id of a notification"})
			return
		}
		from = id
	}
	u, _ := tenancy.User(ctx)
	user := notifyUser(u)
	release, e := s.openNotifications(notifyCaller{tenancy.MustFrom(ctx).ID(), user})
	if e != nil {
		s.unavailable(w, e)
		return
	}
	defer release()
	if from.IsZero() {
		// From now: from the newest kept, read before the headers go, so a
		// client that has them misses nothing sent after.
		last, err := stream.LastNotification(ctx, s.bus, user)
		if err != nil {
			s.internal(w, "reading the caller's notifications", err)
			return
		}
		from = last
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		// The stream has no end of its own: it ends when the server starts
		// to drain (the process got SIGTERM: its client goes on from
		// another replica with Last-Event-ID), or shuts down.
		select {
		case <-s.cfg.drainStop: // nil without WithDrain
		case <-s.closed:
		case <-ctx.Done():
		}
		cancel()
	}()
	out := newSSEWriter(w, cancel)
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Accel-Buffering", "no")
	out.open()
	if s.cfg.keepAlive > 0 {
		defer out.keepAlive(s.cfg.clock, s.cfg.keepAlive)()
	}
	defer out.close()
	for e, err := range stream.WatchUser(ctx, s.bus, user, from) {
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("following a user's notifications failed", "err", err)
			}
			return
		}
		n := UserEvent{V: WireVersion, Type: TypeResync, At: s.cfg.clock.Now()}
		if e.Type != stream.TypeResync {
			if err := json.Unmarshal(e.Data, &n); err != nil {
				s.log.Warn("a notification that does not decode; dropped", "id", e.ID.String(), "err", err)
				continue
			}
		}
		n.ID = e.ID.String()
		out.notification(n)
	}
}
