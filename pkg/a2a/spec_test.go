package a2a_test

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/a2a"
)

// example is one request of the spec and what it answers
// (testdata/spec/examples.json).
type example struct {
	Name     string            `json:"name"`
	Section  string            `json:"section"`
	Binding  string            `json:"binding"`
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	Headers  map[string]string `json:"headers"`
	Request  json.RawMessage   `json:"request"`
	Subst    map[string]string `json:"subst"`
	Status   int               `json:"status"`
	Reason   string            `json:"reason"`
	Response json.RawMessage   `json:"response"`
	Stream   []json.RawMessage `json:"stream"`
	Ignore   []string          `json:"ignore"`
}

func loadExamples(t testing.TB) map[string]example {
	t.Helper()
	raw, err := os.ReadFile("testdata/spec/examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct{ Examples []example }
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	out := map[string]example{}
	for _, e := range f.Examples {
		out[e.Name] = e
	}
	return out
}

// exchange is what our server answered to an example.
type exchange struct {
	status int
	header http.Header
	body   []byte
	items  []json.RawMessage // a stream's data lines
}

// play sends an example's request, with its placeholders replaced by the
// values of vars.
func (r *rig) play(ex example, vars map[string]string) exchange {
	r.t.Helper()
	subst := func(s string) string {
		for from, name := range ex.Subst {
			v, ok := vars[name]
			if !ok {
				r.t.Fatalf("%s: no value for %q yet", ex.Name, name)
			}
			s = strings.ReplaceAll(s, from, v)
		}
		return s
	}
	method, path := ex.Method, subst(ex.Path)
	switch ex.Binding {
	case "JSONRPC":
		method, path = http.MethodPost, a2a.PathPrefix+"default"
	case "HTTP+JSON":
		path = a2a.PathPrefix + "default" + path
	}
	var body io.Reader
	if len(ex.Request) > 0 {
		body = strings.NewReader(subst(string(ex.Request)))
	}
	req, err := http.NewRequestWithContext(r.t.Context(), method, r.url(path), body)
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+keyA)
	req.Header.Set("Content-Type", "application/a2a+json")
	for k, v := range ex.Headers {
		req.Header.Set(k, v)
	}
	resp, err := r.ts.Client().Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	x := exchange{status: resp.StatusCode, header: resp.Header}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				x.items = append(x.items, json.RawMessage(d))
			}
		}
		return x
	}
	if x.body, err = io.ReadAll(resp.Body); err != nil {
		r.t.Fatal(err)
	}
	return x
}

// TestSpecExamples plays the spec's examples (requests and responses of
// sections 6, 8.5, 9 and 11) against an agent on the fake model, through
// both bindings. Each response must have the spec's shape (matchShape), and
// ours are kept as golden files.
func TestSpecExamples(t *testing.T) {
	t.Parallel()
	r := newRig(t, a2a.WithPublicURL("https://agents.example.com"))
	exs := loadExamples(t)
	vars := map[string]string{}
	golden := map[string]any{}
	var order []string
	check := func(name string) exchange {
		t.Helper()
		ex, ok := exs[name]
		if !ok {
			t.Fatalf("no example %q", name)
		}
		x := r.play(ex, vars)
		if x.status != ex.Status {
			t.Fatalf("%s (%s): HTTP %d, want %d: %s", name, ex.Section, x.status, ex.Status, x.body)
		}
		switch {
		case ex.Stream != nil:
			if len(x.items) < len(ex.Stream) {
				t.Fatalf("%s: %d stream items, want at least %d", name, len(x.items), len(ex.Stream))
			}
			// The spec's first item, its first artifact update (if any)
			// and its last, against ours.
			pairs := [][2]json.RawMessage{{ex.Stream[0], x.items[0]}, {ex.Stream[len(ex.Stream)-1], x.items[len(x.items)-1]}}
			if len(ex.Stream) == 3 {
				i := slices.IndexFunc(x.items, func(it json.RawMessage) bool { return bytes.Contains(it, []byte(`"artifactUpdate"`)) })
				if i < 0 {
					t.Fatalf("%s: no artifact update", name)
				}
				pairs = append(pairs, [2]json.RawMessage{ex.Stream[1], x.items[i]})
			}
			for _, p := range pairs {
				if err := matchShape(p[0], p[1], ex.Ignore); err != nil {
					t.Errorf("%s (%s): %v\n got: %s", name, ex.Section, err, p[1])
				}
			}
			var items []any
			for _, it := range x.items {
				items = append(items, it)
			}
			golden[name] = items
		default:
			if ex.Reason != "" && !bytes.Contains(x.body, []byte(`"reason":"`+ex.Reason+`"`)) {
				t.Errorf("%s: no error with reason %s: %s", name, ex.Reason, x.body)
			}
			if ex.Response != nil {
				if err := matchShape(ex.Response, x.body, ex.Ignore); err != nil {
					t.Errorf("%s (%s): %v\n got: %s", name, ex.Section, err, x.body)
				}
			}
			golden[name] = json.RawMessage(x.body)
		}
		order = append(order, name)
		return x
	}
	taskOf := func(body []byte) a2a.Task {
		t.Helper()
		var v struct {
			Task   *a2a.Task `json:"task"`
			Result *struct {
				Task *a2a.Task `json:"task"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatal(err)
		}
		switch {
		case v.Task != nil:
			return *v.Task
		case v.Result != nil && v.Result.Task != nil:
			return *v.Result.Task
		}
		t.Fatalf("no task in %s", body)
		return a2a.Task{}
	}

	first := taskOf(check("6.1-send").body)
	vars["task"], vars["context"] = first.ID, first.ContextID
	check("6.2-stream")
	for _, name := range []string{
		"6.4-version", "6.5-list-invalid", "6.6-push", "6.7-file", "6.8-data", "11.4-send",
		"9.4.1-SendMessage", "9.4.2-SendStreamingMessage", "9.4.3-GetTask", "9.4.8-GetExtendedAgentCard",
		"9.5-method-not-found", "9.5-task-not-found", "11.6-not-found", "8.5-card",
	} {
		check(name)
	}
	check("6.5-list-context")

	// Two contexts of many tasks, and two waiting tasks.
	big := r.send("default", keyA, message("page 0"), nil)
	for i := 1; i < 12; i++ {
		m := message("page " + strconv.Itoa(i))
		m["contextId"] = big.ContextID
		r.send("default", keyA, m, nil)
	}
	vars["bigContext"] = big.ContextID
	vars["secondPage"] = pageToken(10)
	check("6.5-list-page")

	waiting := r.send("default", keyA, message("wait for me"), map[string]any{"returnImmediately": true})
	vars["waitingTask"], vars["waitingContext"], vars["firstPage"] = waiting.ID, waiting.ContextID, pageToken(0)
	check("6.5-list-working")
	check("9.4.4-ListTasks")

	// SubscribeToTask: the waiting task, canceled once the stream is open.
	r.whenStreamOpens(func() { r.cancelRun(keyA, waiting.ID) })
	check("9.4.6-SubscribeToTask")

	other := r.send("default", keyA, message("wait again"), map[string]any{"returnImmediately": true})
	vars["otherWaitingTask"] = other.ID
	check("9.4.5-CancelTask")

	out := map[string]any{}
	for _, name := range order {
		out[name] = golden[name]
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	testkit.Golden(t, "testdata/golden/spec_examples.json", normalize(data))
}

func pageToken(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

var timeRE = regexp.MustCompile(`"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z"`)

// normalize replaces the times in a golden file.
func normalize(b []byte) []byte {
	return append(timeRE.ReplaceAll(b, []byte(`"<time>"`)), '\n')
}

// freeKeys are fields whose values are the spec's own (IDs, times, text):
// ours only needs the same type.
var freeKeys = []string{
	"id", "contextId", "taskId", "artifactId", "messageId", "timestamp", "text",
	"name", "description", "message", "url", "version",
}

// matchShape reports how got differs from the spec's want: a field of want
// missing from got, or a value that differs (but for freeKeys, and the
// paths of ignore, dotted with array indexes: "tasks.0.status.message").
func matchShape(want, got json.RawMessage, ignore []string) error {
	var w, g any
	if err := json.Unmarshal(want, &w); err != nil {
		return fmt.Errorf("the spec's JSON: %w", err)
	}
	if err := json.Unmarshal(got, &g); err != nil {
		return fmt.Errorf("our JSON: %w", err)
	}
	return match(w, g, "", "", ignore)
}

func match(w, g any, path, key string, ignore []string) error {
	if slices.Contains(ignore, path) {
		return nil
	}
	at := func(k string) string {
		if path == "" {
			return k
		}
		return path + "." + k
	}
	switch w := w.(type) {
	case map[string]any:
		gm, ok := g.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: want an object, got %T", path, g)
		}
		for k, v := range w {
			if slices.Contains(ignore, at(k)) {
				continue
			}
			gv, ok := gm[k]
			if !ok {
				return fmt.Errorf("%s: missing", at(k))
			}
			if err := match(v, gv, at(k), k, ignore); err != nil {
				return err
			}
		}
	case []any:
		ga, ok := g.([]any)
		if !ok {
			return fmt.Errorf("%s: want an array, got %T", path, g)
		}
		for i, v := range w {
			if slices.Contains(ignore, at(strconv.Itoa(i))) {
				continue
			}
			if i >= len(ga) {
				return fmt.Errorf("%s: %d items, want %d", path, len(ga), len(w))
			}
			if err := match(v, ga[i], at(strconv.Itoa(i)), key, ignore); err != nil {
				return err
			}
		}
	default:
		if slices.Contains(freeKeys, key) {
			if fmt.Sprintf("%T", w) != fmt.Sprintf("%T", g) {
				return fmt.Errorf("%s: want a %T, got %T", path, w, g)
			}
			return nil
		}
		if w != g {
			return fmt.Errorf("%s: got %v, want %v", path, g, w)
		}
	}
	return nil
}
