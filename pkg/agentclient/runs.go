package agentclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// RunsQuery selects the runs of [Client.Runs].
type RunsQuery struct {
	// ConversationID lists that conversation's runs. It may be empty only
	// with Status [wire.StatusSuspended], to list every run waiting for a
	// decision.
	ConversationID string
	// Status keeps the runs of that status; empty keeps all.
	Status string
	// Limit is the page size; 0 is the server's default.
	Limit int
	// Cursor is the NextCursor of the previous page.
	Cursor string
}

// Runs returns a page of runs, newest first (GET /v1/runs). A bad query
// matches [ErrInvalidRequest]; an unknown conversation, [ErrNotFound].
func (c *Client) Runs(ctx context.Context, q RunsQuery) (wire.RunList, error) {
	v := url.Values{}
	if q.ConversationID != "" {
		v.Set("conversation_id", q.ConversationID)
	}
	if q.Status != "" {
		v.Set("status", q.Status)
	}
	if q.Limit != 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Cursor != "" {
		v.Set("cursor", q.Cursor)
	}
	path := "/v1/runs"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	var list wire.RunList
	return list, c.get(ctx, path, &list)
}

// Events follows a run's events (GET /v1/runs/{id}/events): those after seq
// after (-1 for all of them), then, while the run goes on, the new ones. A
// chat run whose stream went away is followed again this way within the
// server's reconnect grace. A run another server executes, or that has
// ended, is read from what was stored.
func (c *Client) Events(ctx context.Context, id string, after int) (*Stream, error) {
	var header http.Header
	if after >= 0 {
		header = http.Header{"Last-Event-Id": {strconv.Itoa(after)}}
	}
	return c.openStream(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(id)+"/events", nil, header)
}

// Resume continues a run that was interrupted or failed
// (POST /v1/runs/{id}/resume) and returns its event stream, from the first
// event of the continuation. A run that is going on matches
// [ErrConversationBusy] (code run_leased); one that ended for good, or waits
// for a decision, [ErrRunState].
func (c *Client) Resume(ctx context.Context, id string) (*Stream, error) {
	return c.openStream(ctx, http.MethodPost, "/v1/runs/"+url.PathEscape(id)+"/resume", struct{}{}, nil)
}

// Stop interrupts a run at once (POST /v1/runs/{id}/stop), keeping its
// completed turns, and returns it: [Client.Resume] continues it, and a new
// message to its conversation starts a new run.
func (c *Client) Stop(ctx context.Context, id string) (wire.Run, error) {
	return c.command(ctx, id, "stop", struct{}{})
}

// Cancel ends a run for good (POST /v1/runs/{id}/cancel) and returns it.
func (c *Client) Cancel(ctx context.Context, id string) (wire.Run, error) {
	return c.command(ctx, id, "cancel", struct{}{})
}

// Decide answers the approval at d.Path of a run waiting for a decision
// (POST /v1/runs/{id}/decisions), which then goes on; it returns the run as
// the decision left it. Follow the run with [Client.Events].
func (c *Client) Decide(ctx context.Context, id string, d wire.DecisionRequest) (wire.Run, error) {
	return c.command(ctx, id, "decisions", d)
}

func (c *Client) command(ctx context.Context, id, verb string, body any) (wire.Run, error) {
	var run wire.Run
	path := "/v1/runs/" + url.PathEscape(id) + "/" + verb
	resp, err := c.postJSON(ctx, path, body, "application/json")
	if err != nil {
		return run, err
	}
	return run, c.decode(resp, "POST "+path, &run)
}

// openStream sends a request answered by an event stream and returns it.
func (c *Client) openStream(ctx context.Context, method, path string, body any, header http.Header) (*Stream, error) {
	ctx, cancel := context.WithCancel(ctx)
	var (
		resp *http.Response
		err  error
	)
	if body != nil {
		resp, err = c.postJSON(ctx, path, body, "text/event-stream")
	} else {
		var r *http.Request
		if r, err = c.request(ctx, method, path, nil, "text/event-stream"); err == nil {
			for k, vs := range header {
				r.Header[k] = vs
			}
			resp, err = c.hc.Do(r)
		}
	}
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer cancel()
		return nil, c.apiError(resp)
	}
	if !hasMediaType(resp.Header.Get("Content-Type"), "text/event-stream") {
		cancel()
		resp.Body.Close()
		return nil, fmt.Errorf("%w: %s %s answered %q, not an event stream", ErrBadResponse, method, path, resp.Header.Get("Content-Type"))
	}
	return newStream(resp, cancel, c.maxBytes), nil
}
