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

	"github.com/bsamanvith/agentkit/connectors/google/drive"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

const token = "ya29.CANARY-drive-token"

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

// seen is one request the fake Drive received.
type seen struct {
	Method, Path string
	Query        url.Values
	Header       http.Header
	Body         string
}

// fake is a Drive that answers with canned replies and records requests.
type fake struct {
	mu      sync.Mutex
	reqs    []seen
	handler func(w http.ResponseWriter, r *http.Request)
}

func (f *fake) last() seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func newRig(t *testing.T, settings string, h func(w http.ResponseWriter, r *http.Request)) (map[string]tool.Tool, *fake) {
	t.Helper()
	f := &fake{handler: h}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, seen{r.Method, r.URL.Path, r.URL.Query(), r.Header.Clone(), string(b)})
		f.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		f.handler(w, r)
	}))
	t.Cleanup(srv.Close)
	fac := drive.Factory(drive.WithBaseURL(srv.URL), drive.WithTokenSource(staticToken(token)))
	if settings == "" {
		settings = "{}"
	}
	tools, err := fac.Build(context.Background(), json.RawMessage(settings))
	if err != nil {
		t.Fatalf("Build: %v", err)
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

const fileJSON = `{"id":"f1","name":"Plan","mimeType":"text/plain","size":"12","parents":["p1"],"owners":[{"displayName":"Ada","emailAddress":"ada@example.com"}],"webViewLink":"https://drive.google.com/file/d/f1/view"}`

func TestToolsAreClassified(t *testing.T) {
	tools, _ := newRig(t, "", nil)
	want := map[string]tool.Risk{
		"drive_search": tool.RiskLow, "drive_get_file": tool.RiskLow, "drive_read_file": tool.RiskLow, "drive_list_permissions": tool.RiskLow,
		"drive_create_file": tool.RiskMedium, "drive_update_file": tool.RiskMedium, "drive_copy_file": tool.RiskMedium,
		"drive_trash_file": tool.RiskMedium, "drive_share_file": tool.RiskHigh,
	}
	if len(tools) != len(want) {
		t.Errorf("got %d tools, want %d", len(tools), len(want))
	}
	for name, risk := range want {
		d := tools[name].Definition()
		k, r := d.Class()
		if !d.Classified() || r != risk {
			t.Errorf("%s class = %v/%v, want risk %v", name, k, r, risk)
		}
		if (risk == tool.RiskLow) != (k == tool.KindRead) {
			t.Errorf("%s kind = %v for risk %v", name, k, risk)
		}
	}
}

func TestSearchBuildsQueryAndMapsFiles(t *testing.T) {
	tools, f := newRig(t, "", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, `{"nextPageToken":"n2","files":[`+fileJSON+`,{"id":"d1","name":"Docs","mimeType":"application/vnd.google-apps.folder"}]}`)
	})
	out, err := call(t, tools, "drive_search",
		`{"text":"Q3 plan","name_contains":"it's","mime_type":"folder","folder_id":"root","modified_after":"2026-10-01","query":"starred","limit":5,"page_token":"n1"}`)
	if err != nil {
		t.Fatal(err)
	}
	r := f.last()
	if r.Method != "GET" || r.Path != "/drive/v3/files" {
		t.Errorf("request = %s %s", r.Method, r.Path)
	}
	wantQ := `fullText contains 'Q3 plan' and name contains 'it\'s' and mimeType = 'application/vnd.google-apps.folder' and 'root' in parents and modifiedTime > '2026-10-01T00:00:00Z' and (starred) and trashed = false`
	if got := r.Query.Get("q"); got != wantQ {
		t.Errorf("q = %q\nwant %q", got, wantQ)
	}
	if r.Query.Get("pageSize") != "5" || r.Query.Get("pageToken") != "n1" || r.Query.Get("orderBy") != "modifiedTime desc" {
		t.Errorf("query = %v", r.Query)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+token {
		t.Errorf("Authorization = %q", got)
	}
	files := out["files"].([]any)
	first := files[0].(map[string]any)
	if first["name"] != "Plan" || first["size_bytes"] != float64(12) || first["owners"].([]any)[0] != "Ada <ada@example.com>" {
		t.Errorf("first file = %v", first)
	}
	if files[1].(map[string]any)["is_folder"] != true || out["next_page_token"] != "n2" {
		t.Errorf("out = %v", out)
	}
}

func TestSearchRefusesBadInput(t *testing.T) {
	tools, f := newRig(t, "", nil)
	for name, in := range map[string]string{
		"limit too big": `{"limit": 101}`,
		"bad folder id": `{"folder_id": "x' or '1'='1"}`,
		"bad mime":      `{"mime_type": "pdf"}`,
		"bad date":      `{"modified_after": "yesterday"}`,
		"bad order":     `{"order_by": "size"}`,
	} {
		if _, err := call(t, tools, "drive_search", in); !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
	if len(f.reqs) != 0 {
		t.Errorf("%d requests were sent for invalid input", len(f.reqs))
	}
}

func TestReadFile(t *testing.T) {
	const pngMeta = `{"id":"img1","name":"pic","mimeType":"image/png","webViewLink":"https://drive.example/pic"}`
	tools, f := newRig(t, `{"max_read_chars": 100}`, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/drive/v3/files/doc1":
			reply(w, 200, `{"id":"doc1","name":"Notes","mimeType":"application/vnd.google-apps.document"}`)
		case r.URL.Path == "/drive/v3/files/doc1/export":
			io.WriteString(w, strings.Repeat("héllo ", 40)) // 240 characters
		case r.URL.Path == "/drive/v3/files/sheet1":
			reply(w, 200, `{"id":"sheet1","name":"Data","mimeType":"application/vnd.google-apps.spreadsheet"}`)
		case r.URL.Path == "/drive/v3/files/sheet1/export":
			io.WriteString(w, "a,b\n1,2\n")
		case r.URL.Path == "/drive/v3/files/img1":
			reply(w, 200, pngMeta)
		case r.URL.Path == "/drive/v3/files/dir1":
			reply(w, 200, `{"id":"dir1","name":"Docs","mimeType":"application/vnd.google-apps.folder"}`)
		default:
			http.NotFound(w, r)
		}
	})

	out, err := call(t, tools, "drive_read_file", `{"file_id":"doc1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if f.last().Query.Get("mimeType") != "text/plain" {
		t.Errorf("export query = %v", f.last().Query)
	}
	if n := len([]rune(out["content"].(string))); n != 100 || out["total_chars"] != float64(240) || out["truncated"] != true {
		t.Errorf("first page: %d chars, out = %v", n, out)
	}
	if !strings.Contains(out["note"].(string), "offset=100") {
		t.Errorf("note = %q", out["note"])
	}
	next, err := call(t, tools, "drive_read_file", `{"file_id":"doc1","offset":200}`)
	if err != nil || len([]rune(next["content"].(string))) != 40 || next["truncated"] != nil {
		t.Errorf("last page = %v, %v", next, err)
	}
	if _, err := call(t, tools, "drive_read_file", `{"file_id":"doc1","offset":241}`); !errors.Is(err, tool.ErrInvalidInput) {
		t.Errorf("offset past the end: %v", err)
	}

	sheet, err := call(t, tools, "drive_read_file", `{"file_id":"sheet1"}`)
	if err != nil || sheet["content"] != "a,b\n1,2\n" || f.last().Query.Get("mimeType") != "text/csv" {
		t.Errorf("sheet = %v, %v", sheet, err)
	}
	for _, id := range []string{"img1", "dir1"} {
		if _, err := call(t, tools, "drive_read_file", `{"file_id":"`+id+`"}`); !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("read %s: err = %v, want ErrInvalidInput", id, err)
		}
	}
}

func TestReadTextFileDownloadsMedia(t *testing.T) {
	tools, f := newRig(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") == "media" {
			io.WriteString(w, "# Title\n")
			return
		}
		reply(w, 200, `{"id":"md1","name":"README.md","mimeType":"text/markdown"}`)
	})
	out, err := call(t, tools, "drive_read_file", `{"file_id":"md1"}`)
	if err != nil || out["content"] != "# Title\n" {
		t.Fatalf("out = %v, err = %v", out, err)
	}
	if f.last().Query.Get("alt") != "media" {
		t.Errorf("query = %v", f.last().Query)
	}
}

// multipartParts returns the parts of a multipart/related request body.
func multipartParts(t *testing.T, s seen) (meta map[string]any, ctype, content string) {
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

func TestCreateFile(t *testing.T) {
	tools, f := newRig(t, "", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, fileJSON) })

	// A folder: plain JSON, no upload.
	if _, err := call(t, tools, "drive_create_file", `{"name":"Reports","folder":true,"folder_id":"p1"}`); err != nil {
		t.Fatal(err)
	}
	r := f.last()
	if r.Path != "/drive/v3/files" || !strings.Contains(r.Body, `"mimeType":"application/vnd.google-apps.folder"`) || !strings.Contains(r.Body, `"parents":["p1"]`) {
		t.Errorf("folder request = %s %s", r.Path, r.Body)
	}

	// A text file: multipart upload.
	out, err := call(t, tools, "drive_create_file", `{"name":"a.csv","content":"x,y\n","mime_type":"text/csv"}`)
	if err != nil || out["id"] != "f1" {
		t.Fatalf("out = %v, err = %v", out, err)
	}
	r = f.last()
	if r.Path != "/upload/drive/v3/files" || r.Query.Get("uploadType") != "multipart" {
		t.Errorf("upload request = %s %v", r.Path, r.Query)
	}
	meta, ct, content := multipartParts(t, r)
	if meta["name"] != "a.csv" || meta["mimeType"] != "text/csv" || ct != "text/csv" || content != "x,y\n" {
		t.Errorf("parts = %v %q %q", meta, ct, content)
	}

	// Conversion to a Google document.
	if _, err := call(t, tools, "drive_create_file", `{"name":"Notes","content":"hello","convert_to":"document"}`); err != nil {
		t.Fatal(err)
	}
	meta, ct, _ = multipartParts(t, f.last())
	if meta["mimeType"] != "application/vnd.google-apps.document" || ct != "text/plain" {
		t.Errorf("converted parts = %v %q", meta, ct)
	}

	for name, in := range map[string]string{
		"no name":               `{"name":" "}`,
		"folder with content":   `{"name":"x","folder":true,"content":"y"}`,
		"sheet from plain text": `{"name":"x","content":"y","convert_to":"spreadsheet"}`,
		"unknown conversion":    `{"name":"x","content":"y","convert_to":"slides"}`,
		"native mime type":      `{"name":"x","content":"y","mime_type":"application/vnd.google-apps.document"}`,
		"bad folder id":         `{"name":"x","folder_id":"a b"}`,
	} {
		if _, err := call(t, tools, "drive_create_file", in); !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestUpdateFile(t *testing.T) {
	tools, f := newRig(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			id := strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
			if id == "gdoc" {
				reply(w, 200, `{"id":"gdoc","name":"G","mimeType":"application/vnd.google-apps.document"}`)
				return
			}
			reply(w, 200, fileJSON)
			return
		}
		reply(w, 200, fileJSON)
	})

	// Rename and move: parents are removed.
	if _, err := call(t, tools, "drive_update_file", `{"file_id":"f1","name":"New","move_to_folder_id":"p2"}`); err != nil {
		t.Fatal(err)
	}
	r := f.last()
	if r.Method != "PATCH" || r.Path != "/drive/v3/files/f1" || r.Query.Get("addParents") != "p2" || r.Query.Get("removeParents") != "p1" || !strings.Contains(r.Body, `"name":"New"`) {
		t.Errorf("request = %s %s %v %s", r.Method, r.Path, r.Query, r.Body)
	}

	// New content keeps the file's own type unless one is given.
	if _, err := call(t, tools, "drive_update_file", `{"file_id":"f1","content":"new text"}`); err != nil {
		t.Fatal(err)
	}
	r = f.last()
	_, ct, content := multipartParts(t, r)
	if r.Method != "PATCH" || r.Path != "/upload/drive/v3/files/f1" || ct != "text/plain" || content != "new text" {
		t.Errorf("content request = %s %s %q %q", r.Method, r.Path, ct, content)
	}

	for name, in := range map[string]string{
		"nothing":                 `{"file_id":"f1"}`,
		"empty name":              `{"file_id":"f1","name":""}`,
		"content of a Google doc": `{"file_id":"gdoc","content":"x"}`,
		"mime without content":    `{"file_id":"f1","name":"x","mime_type":"text/plain"}`,
	} {
		if _, err := call(t, tools, "drive_update_file", in); !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestCopyAndTrash(t *testing.T) {
	tools, f := newRig(t, "", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, fileJSON) })
	if _, err := call(t, tools, "drive_copy_file", `{"file_id":"f1","name":"Plan 2","folder_id":"p9"}`); err != nil {
		t.Fatal(err)
	}
	if r := f.last(); r.Method != "POST" || r.Path != "/drive/v3/files/f1/copy" || !strings.Contains(r.Body, `"name":"Plan 2"`) || !strings.Contains(r.Body, `"parents":["p9"]`) {
		t.Errorf("copy request = %s %s %s", r.Method, r.Path, r.Body)
	}
	if _, err := call(t, tools, "drive_trash_file", `{"file_id":"f1"}`); err != nil {
		t.Fatal(err)
	}
	if r := f.last(); r.Method != "PATCH" || r.Path != "/drive/v3/files/f1" || r.Body != `{"trashed":true}` {
		t.Errorf("trash request = %s %s %s", r.Method, r.Path, r.Body)
	}
	if _, err := call(t, tools, "drive_trash_file", `{"file_id":"../x"}`); !errors.Is(err, tool.ErrInvalidInput) {
		t.Errorf("bad id: %v", err)
	}
}

func TestSharing(t *testing.T) {
	tools, f := newRig(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			reply(w, 200, `{"permissions":[{"id":"p","type":"user","role":"writer","emailAddress":"ada@example.com","displayName":"Ada"}]}`)
			return
		}
		reply(w, 200, `{"id":"perm1","type":"user","role":"reader","emailAddress":"bob@example.com"}`)
	})
	got, err := call(t, tools, "drive_list_permissions", `{"file_id":"f1"}`)
	if err != nil || got["permissions"].([]any)[0].(map[string]any)["email_address"] != "ada@example.com" {
		t.Fatalf("permissions = %v, %v", got, err)
	}

	out, err := call(t, tools, "drive_share_file", `{"file_id":"f1","type":"user","role":"reader","email":"bob@example.com","message":"fyi"}`)
	if err != nil || out["permission"].(map[string]any)["id"] != "perm1" {
		t.Fatalf("share = %v, %v", out, err)
	}
	r := f.last()
	if r.Method != "POST" || r.Path != "/drive/v3/files/f1/permissions" || r.Query.Get("sendNotificationEmail") != "true" || r.Query.Get("emailMessage") != "fyi" {
		t.Errorf("share request = %s %s %v", r.Method, r.Path, r.Query)
	}
	if !strings.Contains(r.Body, `"emailAddress":"bob@example.com"`) {
		t.Errorf("body = %s", r.Body)
	}

	n := len(f.reqs)
	for name, in := range map[string]string{
		"public by default": `{"file_id":"f1","type":"anyone","role":"reader"}`,
		"bad role":          `{"file_id":"f1","type":"user","role":"owner","email":"a@b.c"}`,
		"bad email":         `{"file_id":"f1","type":"user","role":"reader","email":"a b"}`,
		"bad type":          `{"file_id":"f1","type":"world","role":"reader"}`,
		"domain missing":    `{"file_id":"f1","type":"domain","role":"reader"}`,
	} {
		if _, err := call(t, tools, "drive_share_file", in); !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
	if len(f.reqs) != n {
		t.Error("a request was sent for invalid input")
	}

	// With the setting, anyone is allowed, and no email is sent.
	pub, pf := newRig(t, `{"allow_public_sharing": true}`, func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, `{"id":"anyoneWithLink","type":"anyone","role":"reader"}`)
	})
	if _, err := call(t, pub, "drive_share_file", `{"file_id":"f1","type":"anyone","role":"reader"}`); err != nil {
		t.Fatal(err)
	}
	if pf.last().Query.Get("sendNotificationEmail") != "false" {
		t.Errorf("query = %v", pf.last().Query)
	}
}

func TestErrors(t *testing.T) {
	status := 0
	body := ""
	tools, _ := newRig(t, "", func(w http.ResponseWriter, r *http.Request) { reply(w, status, body) })
	for _, tc := range []struct {
		status  int
		body    string
		want    error
		contain string
	}{
		{400, `{"error":{"message":"Invalid Value for q ` + token + `"}}`, tool.ErrInvalidInput, "Invalid Value"},
		{404, `{"error":{"message":"File not found: x."}}`, tool.ErrToolFailed, "not found"},
		{401, `{}`, tool.ErrToolFailed, "reconnected"},
		{403, `{"error":{"message":"The user does not have sufficient permissions"}}`, tool.ErrToolFailed, "sufficient permissions"},
		{429, `{}`, tool.ErrToolFailed, "rate limited"},
		{503, `oops`, tool.ErrToolFailed, "503"},
	} {
		status, body = tc.status, tc.body
		_, err := call(t, tools, "drive_get_file", `{"file_id":"f1"}`)
		if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.contain) {
			t.Errorf("status %d: err = %v, want %v containing %q", tc.status, err, tc.want, tc.contain)
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("status %d: the token is in the error: %v", tc.status, err)
		}
	}
}

func TestWithoutATokenSourceCallsFail(t *testing.T) {
	tools, err := drive.Factory().Build(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tools[0].Invoke(context.Background(), json.RawMessage(`{}`))
	if !errors.Is(err, tool.ErrToolFailed) || !strings.Contains(err.Error(), "no Google account") {
		t.Errorf("err = %v", err)
	}
}

func TestSettings(t *testing.T) {
	fac := drive.Factory()
	for name, s := range map[string]string{
		"unknown field":        `{"nope": 1}`,
		"literal token":        `{"access_token": "x"}`,
		"chars too small":      `{"max_read_chars": 5}`,
		"chars too big":        `{"max_read_chars": 999999999}`,
		"base_url over http":   `{"base_url": "http://drive.example.com"}`,
		"base_url with a user": `{"base_url": "https://u:p@drive.example.com"}`,
	} {
		if _, err := fac.Build(context.Background(), json.RawMessage(s)); !errors.Is(err, toolset.ErrInvalidSettings) {
			t.Errorf("%s: err = %v, want ErrInvalidSettings", name, err)
		}
	}
	for _, s := range []string{`{}`, `{"base_url": "http://127.0.0.1:9000"}`, `{"base_url": "https://example.com/"}`} {
		if _, err := fac.Build(context.Background(), json.RawMessage(s)); err != nil {
			t.Errorf("Build(%s) = %v", s, err)
		}
	}
}

func TestBuildMakesNoRequest(t *testing.T) {
	_, f := newRig(t, "", func(w http.ResponseWriter, r *http.Request) { t.Error("request during Build") })
	if len(f.reqs) != 0 {
		t.Errorf("%d requests", len(f.reqs))
	}
}
