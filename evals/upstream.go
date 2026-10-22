package evals

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Upstream fakes the connectors' APIs for every scenario at once: a
// scenario's toolset t calls <base>/<scenario>/<t><endpoint>, and gets the
// scenario's scripted responses for that endpoint in order (the last one
// repeated), or the endpoint's default. Reset starts a scenario's scripts
// over, for its next run.
type Upstream struct {
	root      string
	scenarios map[string]Scenario

	mu    sync.Mutex
	next  map[string]int // by scenario/toolset/endpoint: responses served
	calls map[string][]string
}

// NewUpstream serves the scenarios; root is the repository root, which
// response files are relative to.
func NewUpstream(root string, scenarios []Scenario) *Upstream {
	u := &Upstream{root: root, scenarios: map[string]Scenario{}, next: map[string]int{}, calls: map[string][]string{}}
	for _, s := range scenarios {
		u.scenarios[s.Name] = s
	}
	return u
}

// Reset starts the scenario's scripts over and forgets its requests.
func (u *Upstream) Reset(scenario string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for k := range u.next {
		if strings.HasPrefix(k, scenario+"/") {
			delete(u.next, k)
		}
	}
	delete(u.calls, scenario)
}

// Requests returns the "<toolset><endpoint>" of each request the scenario
// made since its last Reset.
func (u *Upstream) Requests(scenario string) []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.calls[scenario]...)
}

func (u *Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 3 {
		http.Error(w, "no such scenario endpoint", http.StatusNotFound)
		return
	}
	scenario, toolset, endpoint := parts[0], parts[1], "/"+parts[2]
	s, ok := u.scenarios[scenario]
	responses := s.Upstream[toolset][endpoint]
	if len(responses) == 0 {
		responses = DefaultResponses[toolset][endpoint]
	}
	if !ok || !slices.Contains(s.Toolsets, toolset) || len(responses) == 0 {
		http.Error(w, "no such scenario endpoint", http.StatusNotFound)
		return
	}
	_, _ = io.Copy(io.Discard, r.Body)
	key := scenario + "/" + toolset + endpoint
	u.mu.Lock()
	n := u.next[key]
	u.next[key] = n + 1
	u.calls[scenario] = append(u.calls[scenario], toolset+endpoint)
	u.mu.Unlock()
	if err := u.write(w, responses[min(n, len(responses)-1)]); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (u *Upstream) write(w http.ResponseWriter, r Response) error {
	if r.Cassette != "" {
		raw, err := os.ReadFile(filepath.Join(u.root, r.Cassette))
		if err != nil {
			return err
		}
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
		if err != nil {
			return fmt.Errorf("cassette %s: %w", r.Cassette, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		if v := resp.Header.Get("Retry-After"); v != "" {
			w.Header().Set("Retry-After", v)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return nil
	}
	body := []byte(r.Body)
	if r.BodyFile != "" {
		var err error
		if body, err = os.ReadFile(filepath.Join(u.root, r.BodyFile)); err != nil {
			return err
		}
	}
	w.Header().Set("Content-Type", "application/json")
	for k, v := range r.Headers {
		w.Header().Set(k, v)
	}
	status := r.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return nil
}

// DenyProxy is the HTTP(S) proxy the server under evaluation is given: any
// request that does not go to a loopback address (which bypasses a proxy)
// ends here, is refused and recorded. An eval run with one is not hermetic.
type DenyProxy struct {
	mu   sync.Mutex
	seen []string
}

func (p *DenyProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if r.Method != http.MethodConnect {
		target = r.URL.String()
	}
	p.mu.Lock()
	p.seen = append(p.seen, r.Method+" "+target)
	p.mu.Unlock()
	http.Error(w, "evals: no network outside this machine", http.StatusForbidden)
}

// Seen returns the requests refused so far.
func (p *DenyProxy) Seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}
