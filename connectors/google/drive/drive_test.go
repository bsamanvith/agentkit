package drive_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/google/common"
	"github.com/bsamanvith/agentkit/connectors/google/drive"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

const token = "ya29.CANARY-drive-token"

// seen is one request the fake Drive received.
type seen struct {
	Method, Path string
	Query        url.Values
	Header       http.Header
	Body         string
}

type fake struct {
	mu   sync.Mutex
	reqs []seen
}

func (f *fake) last() seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func (f *fake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func newRig(t *testing.T, settings string, h func(w http.ResponseWriter, r *http.Request)) (map[string]tool.Tool, *fake) {
	t.Helper()
	f := &fake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, seen{r.Method, r.URL.Path, r.URL.Query(), r.Header.Clone(), string(b)})
		f.mu.Unlock()
		if h == nil {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	fac := drive.Factory(drive.WithBaseURL(srv.URL), drive.WithTokenSource(
		common.TokenSourceFunc(func(context.Context) (string, error) { return token, nil })))
	if settings == "" {
		settings = "{}"
	}
	tools, err := fac.Build(context.Background(), json.RawMessage(settings))
	if err != nil {
		t.Fatalf("Build(%s): %v", settings, err)
	}
	m := map[string]tool.Tool{}
	for _, tl := range tools {
		m[tl.Definition().Name] = tl
	}
	return m, f
}

func call(t *testing.T, tools map[string]tool.Tool, name, input string) (map[string]any, error) {
	t.Helper()
	tl, ok := tools[name]
	if !ok {
		t.Fatalf("no tool %q", name)
	}
	out, err := tl.Invoke(context.Background(), json.RawMessage(input))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("%s output is not an object: %s", name, out)
	}
	return m, nil
}

func reply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func names(tools map[string]tool.Tool) []string {
	var out []string
	for n := range tools {
		out = append(out, n)
	}
	return out
}

func TestGatingFollowsSettings(t *testing.T) {
	t.Parallel()
	read := []string{"drive_search_files", "drive_get_file", "drive_read_file", "drive_list_folder"}
	for _, tc := range []struct {
		settings string
		want     []string
	}{
		{`{}`, read},
		{`{"read_only": true}`, read},
		{`{"read_only": false}`, append(read[:4:4], "drive_create_file", "drive_update_file", "drive_trash_file")},
		{`{"read_only": false, "allow_share": true}`, append(read[:4:4], "drive_create_file", "drive_update_file", "drive_trash_file", "drive_share_file")},
	} {
		tools, _ := newRig(t, tc.settings, nil)
		if len(tools) != len(tc.want) {
			t.Errorf("%s: tools = %v, want %v", tc.settings, names(tools), tc.want)
		}
		for _, n := range tc.want {
			if _, ok := tools[n]; !ok {
				t.Errorf("%s: no %s in %v", tc.settings, n, names(tools))
			}
		}
	}
}

func TestToolClasses(t *testing.T) {
	t.Parallel()
	tools, _ := newRig(t, `{"read_only": false, "allow_share": true}`, nil)
	want := map[string]struct {
		kind     tool.Kind
		risk     tool.Risk
		approval bool
	}{
		"drive_search_files": {tool.KindRead, tool.RiskLow, false}, "drive_get_file": {tool.KindRead, tool.RiskLow, false},
		"drive_read_file": {tool.KindRead, tool.RiskLow, false}, "drive_list_folder": {tool.KindRead, tool.RiskLow, false},
		"drive_create_file": {tool.KindWrite, tool.RiskMedium, false}, "drive_update_file": {tool.KindWrite, tool.RiskMedium, false},
		"drive_trash_file": {tool.KindDelete, tool.RiskHigh, true}, "drive_share_file": {tool.KindSend, tool.RiskHigh, true},
	}
	for name, w := range want {
		d := tools[name].Definition()
		k, r := d.Class()
		if k != w.kind || r != w.risk || d.RequiresApproval != w.approval {
			t.Errorf("%s = %v/%v approval %v, want %v/%v approval %v", name, k, r, d.RequiresApproval, w.kind, w.risk, w.approval)
		}
	}
}

func TestSettings(t *testing.T) {
	t.Parallel()
	fac := drive.Factory(drive.WithTokenSource(common.TokenSourceFunc(func(context.Context) (string, error) { return token, nil })))
	for name, s := range map[string]string{
		"unknown field":         `{"nope": 1}`,
		"literal token":         `{"access_token": "x"}`,
		"share while read only": `{"allow_share": true}`,
		"content chars too big": `{"max_content_chars": 999999999}`,
		"content chars tiny":    `{"max_content_chars": 5}`,
		"download too big":      `{"max_download_bytes": 99999999}`,
		"base_url over http":    `{"base_url": "http://drive.example.com"}`,
	} {
		if _, err := fac.Build(context.Background(), json.RawMessage(s)); !errors.Is(err, toolset.ErrInvalidSettings) {
			t.Errorf("%s: err = %v, want ErrInvalidSettings", name, err)
		}
	}
}

func TestScopes(t *testing.T) {
	t.Parallel()
	for settings, want := range map[string]string{
		`{}`:                   drive.ScopeReadonly,
		`{"read_only": false}`: drive.Scope,
		`{"read_only": false, "narrow_scopes": true}`: drive.ScopeReadonly + " " + drive.ScopeFile,
		`{"narrow_scopes": true}`:                     drive.ScopeReadonly,
	} {
		got, err := drive.Scopes(json.RawMessage(settings))
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("Scopes(%s) = %v, %v; want %s", settings, got, err, want)
		}
	}
}

func TestSearchMapsFilesAndPages(t *testing.T) {
	t.Parallel()
	tools, f := newRig(t, "", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, `{"nextPageToken":"n2","files":[
			{"id":"f1","name":"Plan","mimeType":"text/plain","size":"12","parents":["p1"],"owners":[{"displayName":"Ada","emailAddress":"ada@example.com"}]},
			{"id":"d1","name":"Docs","mimeType":"application/vnd.google-apps.folder"}]}`)
	})
	out, err := call(t, tools, "drive_search_files", `{"query":"it's","max_results":5,"page_token":"n1"}`)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	r := f.reqs[0]
	f.mu.Unlock()
	if r.Query.Get("pageToken") != "n1" || r.Query.Get("pageSize") != "5" || !strings.Contains(r.Query.Get("q"), `'it\'s'`) {
		t.Errorf("query = %v", r.Query)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+token {
		t.Errorf("Authorization = %q", got)
	}
	files := out["files"].([]any)
	first, second := files[0].(map[string]any), files[1].(map[string]any)
	if first["size"] != float64(12) || first["owners"].([]any)[0] != "Ada <ada@example.com>" || first["parents"].([]any)[0] != "p1" {
		t.Errorf("first = %v", first)
	}
	if second["owners"] == nil || second["parents"] == nil {
		t.Errorf("a folder with no owners or parents must show empty lists, got %v", second)
	}
	// Two files a page, five wanted: the connector followed the token.
	if len(files) != 5 || out["next_page_token"] != "n2" || f.count() != 3 {
		t.Errorf("%d files, next %v, %d requests", len(files), out["next_page_token"], f.count())
	}
}

func TestReadFile(t *testing.T) {
	t.Parallel()
	meta := map[string]string{
		"doc":   `{"id":"doc","name":"Notes","mimeType":"application/vnd.google-apps.document"}`,
		"slide": `{"id":"slide","name":"Deck","mimeType":"application/vnd.google-apps.presentation"}`,
		"sheet": `{"id":"sheet","name":"Data","mimeType":"application/vnd.google-apps.spreadsheet"}`,
		"form":  `{"id":"form","name":"Poll","mimeType":"application/vnd.google-apps.form"}`,
		"md":    `{"id":"md","name":"README.md","mimeType":"text/markdown","size":"8"}`,
		"big":   `{"id":"big","name":"log.txt","mimeType":"text/plain","size":"6000000"}`,
		"pdf":   `{"id":"pdf","name":"a.pdf","mimeType":"application/pdf","size":"100","webViewLink":"https://drive.example/pdf"}`,
		"dir":   `{"id":"dir","name":"Docs","mimeType":"application/vnd.google-apps.folder"}`,
	}
	tools, f := newRig(t, `{"max_content_chars": 100}`, func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/drive/v3/files/"), "/")
		switch {
		case len(parts) == 2 && parts[1] == "export":
			_, _ = io.WriteString(w, map[string]string{"doc": strings.Repeat("héllo 世界 ", 40), "slide": "slide text", "sheet": "a,b\n1,2\n"}[parts[0]])
		case r.URL.Query().Get("alt") == "media":
			_, _ = io.WriteString(w, "# Title\n")
		case meta[parts[0]] != "":
			reply(w, 200, meta[parts[0]])
		default:
			http.NotFound(w, r)
		}
	})

	for id, target := range map[string]string{"doc": "text/markdown", "slide": "text/plain", "sheet": "text/csv"} {
		if _, err := call(t, tools, "drive_read_file", `{"file_id":"`+id+`"}`); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if got := f.last().Query.Get("mimeType"); got != target {
			t.Errorf("%s exported as %q, want %q", id, got, target)
		}
	}
	out, err := call(t, tools, "drive_read_file", `{"file_id":"md"}`)
	if err != nil || out["content"] != "# Title\n" || f.last().Query.Get("alt") != "media" {
		t.Errorf("md = %v, %v", out, err)
	}
	for id, want := range map[string]string{"big": "download limit", "pdf": "not text", "form": "Google form", "dir": "folder"} {
		_, err := call(t, tools, "drive_read_file", `{"file_id":"`+id+`"}`)
		if !errors.Is(err, tool.ErrInvalidInput) || !strings.Contains(err.Error(), want) {
			t.Errorf("read %s: err = %v, want ErrInvalidInput containing %q", id, err, want)
		}
	}
}

// TestReadCutsAtARuneBoundary: text is cut to max_chars whole characters and
// the marker says how many were left and how to get them.
func TestReadCutsAtARuneBoundary(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("héllo 世界 ", 30) // 9 characters a repeat: 270
	tools, _ := newRig(t, "", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/export") {
			_, _ = io.WriteString(w, text)
			return
		}
		reply(w, 200, `{"id":"doc","name":"Notes","mimeType":"application/vnd.google-apps.document"}`)
	})
	out, err := call(t, tools, "drive_read_file", `{"file_id":"doc","max_chars":101}`)
	if err != nil {
		t.Fatal(err)
	}
	content := out["content"].(string)
	kept, marker, found := strings.Cut(content, "…[")
	if !found || kept != string([]rune(text)[:101]) || !strings.HasPrefix("…["+marker, "…[truncated 169 characters; call drive_read_file with a larger max_chars") {
		t.Errorf("content = %q", content)
	}
	if _, err := call(t, tools, "drive_read_file", `{"file_id":"doc","max_chars":999999}`); !errors.Is(err, tool.ErrInvalidInput) {
		t.Errorf("max_chars over the toolset's limit: %v", err)
	}
}

func TestTrashSetsTrashedAndNeverDeletes(t *testing.T) {
	t.Parallel()
	tools, f := newRig(t, `{"read_only": false, "allow_share": true}`, func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, `{"id":"f1","name":"Plan","mimeType":"text/plain"}`)
	})
	for _, c := range []struct{ tool, in string }{
		{"drive_trash_file", `{"file_id":"f1"}`},
		{"drive_create_file", `{"name":"a"}`},
		{"drive_update_file", `{"file_id":"f1","name":"b"}`},
	} {
		if _, err := call(t, tools, c.tool, c.in); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.reqs {
		if r.Method == http.MethodDelete {
			t.Errorf("DELETE %s", r.Path)
		}
	}
	if r := f.reqs[0]; r.Method != "PATCH" || r.Body != `{"trashed":true}` {
		t.Errorf("trash request = %s %s", r.Method, r.Body)
	}
}

func TestShare(t *testing.T) {
	t.Parallel()
	tools, f := newRig(t, `{"read_only": false, "allow_share": true}`, func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, `{"id":"perm1","type":"group","role":"commenter","emailAddress":"team@example.com"}`)
	})
	out, err := call(t, tools, "drive_share_file", `{"file_id":"f1","email":"team@example.com","role":"commenter","type":"group","notify":true}`)
	if err != nil || out["permission_id"] != "perm1" || out["email"] != "team@example.com" {
		t.Fatalf("out = %v, err = %v", out, err)
	}
	if r := f.last(); r.Query.Get("sendNotificationEmail") != "true" || !strings.Contains(r.Body, `"type":"group"`) {
		t.Errorf("request = %v %s", r.Query, r.Body)
	}
	if _, err := call(t, tools, "drive_share_file", `{"file_id":"f1","email":"a@example.com","role":"reader"}`); err != nil {
		t.Fatal(err)
	}
	if f.last().Query.Get("sendNotificationEmail") != "false" {
		t.Errorf("notify must default to false: %v", f.last().Query)
	}
	n := f.count()
	for name, in := range map[string]string{
		"anyone":          `{"file_id":"f1","email":"a@example.com","role":"reader","type":"anyone"}`,
		"domain":          `{"file_id":"f1","email":"a@example.com","role":"reader","type":"domain"}`,
		"owner":           `{"file_id":"f1","email":"a@example.com","role":"owner"}`,
		"two addresses":   `{"file_id":"f1","email":"a@example.com, b@example.com","role":"reader"}`,
		"no address":      `{"file_id":"f1","email":"everyone","role":"reader"}`,
		"newline address": `{"file_id":"f1","email":"a@example.com\nbcc: x@y.z","role":"reader"}`,
	} {
		if _, err := call(t, tools, "drive_share_file", in); !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
	if f.count() != n {
		t.Error("a request was sent for invalid input")
	}
}

// parts reads a multipart/related request: the metadata and the content.
func parts(t *testing.T, s seen) (meta map[string]any, contentType, content string) {
	t.Helper()
	mt, params, err := mime.ParseMediaType(s.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/related" {
		t.Fatalf("Content-Type = %q", s.Header.Get("Content-Type"))
	}
	mr := multipart.NewReader(strings.NewReader(s.Body), params["boundary"])
	p1, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(p1)
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatalf("metadata part: %v", err)
	}
	p2, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	c, _ := io.ReadAll(p2)
	return meta, p2.Header.Get("Content-Type"), string(c)
}

func TestCreateAndUpdateUploadContent(t *testing.T) {
	t.Parallel()
	tools, f := newRig(t, `{"read_only": false}`, func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
		if r.Method == "GET" {
			reply(w, 200, map[string]string{
				"gdoc": `{"id":"gdoc","name":"G","mimeType":"application/vnd.google-apps.document"}`,
				"gsh":  `{"id":"gsh","name":"S","mimeType":"application/vnd.google-apps.spreadsheet"}`,
				"gfm":  `{"id":"gfm","name":"F","mimeType":"application/vnd.google-apps.form"}`,
				"txt":  `{"id":"txt","name":"t.md","mimeType":"text/markdown"}`,
				"pdf":  `{"id":"pdf","name":"a.pdf","mimeType":"application/pdf"}`,
			}[id])
			return
		}
		reply(w, 200, `{"id":"new","name":"n","mimeType":"text/plain"}`)
	})

	// A new text file, a new Doc from text, a new Sheet from CSV.
	for _, c := range []struct{ in, wantType, wantMime, wantContent string }{
		{`{"name":"a.csv","content":"x,y\n","mime_type":"text/csv","folder_id":"p1"}`, "text/csv", "text/csv", "x,y\n"},
		{`{"name":"Notes","content":"hello","mime_type":"google_doc"}`, "text/plain", "application/vnd.google-apps.document", "hello"},
		{`{"name":"Data","content":"a,b\n","mime_type":"google_sheet"}`, "text/csv", "application/vnd.google-apps.spreadsheet", "a,b\n"},
		{`{"name":"plain","content":"hi"}`, "text/plain", "text/plain", "hi"},
	} {
		if _, err := call(t, tools, "drive_create_file", c.in); err != nil {
			t.Fatal(err)
		}
		r := f.last()
		meta, ct, content := parts(t, r)
		if r.Method != "POST" || r.Path != "/upload/drive/v3/files" || r.Query.Get("uploadType") != "multipart" ||
			meta["mimeType"] != c.wantMime || ct != c.wantType || content != c.wantContent {
			t.Errorf("%s -> %s %s %v meta %v %q %q", c.in, r.Method, r.Path, r.Query, meta, ct, content)
		}
	}

	// Replacing content: the file's own type decides.
	for id, ct := range map[string]string{"gdoc": "text/plain", "gsh": "text/csv", "txt": "text/markdown"} {
		if _, err := call(t, tools, "drive_update_file", `{"file_id":"`+id+`","content":"new","name":"renamed"}`); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		r := f.last()
		meta, got, content := parts(t, r)
		if r.Method != "PATCH" || r.Path != "/upload/drive/v3/files/"+id || got != ct || content != "new" || meta["name"] != "renamed" {
			t.Errorf("%s -> %s %s %v %q %q", id, r.Method, r.Path, meta, got, content)
		}
	}
	for _, id := range []string{"gfm", "pdf"} {
		if _, err := call(t, tools, "drive_update_file", `{"file_id":"`+id+`","content":"x"}`); !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("replace %s: err = %v, want ErrInvalidInput", id, err)
		}
	}
}

func TestWriteInputChecks(t *testing.T) {
	t.Parallel()
	tools, f := newRig(t, `{"read_only": false}`, nil)
	for name, c := range map[string]struct{ tool, in string }{
		"blank name":        {"drive_create_file", `{"name":" "}`},
		"native mime type":  {"drive_create_file", `{"name":"x","mime_type":"application/vnd.google-apps.document"}`},
		"bad folder id":     {"drive_create_file", `{"name":"x","folder_id":"a b"}`},
		"oversized content": {"drive_create_file", `{"name":"x","content":"` + strings.Repeat("a", 5<<20+1) + `"}`},
		"empty new name":    {"drive_update_file", `{"file_id":"f1","name":""}`},
		"bad id":            {"drive_trash_file", `{"file_id":"../x"}`},
	} {
		if _, err := call(t, tools, c.tool, c.in); !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
	if f.count() != 0 {
		t.Errorf("%d requests were sent for invalid input", f.count())
	}
}
