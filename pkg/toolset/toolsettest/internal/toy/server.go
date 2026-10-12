package toy

import (
	"encoding/json"
	"net/http"
	"strings"
)

// NewServer returns a reference implementation of the toy API: the "real"
// upstream that the toy's cassettes are recorded from. It accepts key, sent as
// "Authorization: Bearer <key>", or a keyless request carrying
// [KeylessHeader].
func NewServer(key string) http.Handler {
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+key && r.Header.Get(KeylessHeader) != "keyless" {
				writeError(w, http.StatusUnauthorized, "missing or invalid API key")
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("POST /v1/search", auth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Q     string `json:"q"`
			Limit int    `json:"limit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Q == "" {
			writeError(w, http.StatusBadRequest, "q is required")
			return
		}
		type hit struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		}
		hits := []hit{}
		for _, it := range items {
			if len(hits) < req.Limit && strings.Contains(strings.ToLower(it.name+" "+it.text), strings.ToLower(req.Q)) {
				hits = append(hits, hit{Title: it.name, Link: "https://toy.example/items/" + it.id, Snippet: it.text})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"hits": hits, "took_ms": 3})
	}))
	mux.HandleFunc("GET /v1/items/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		for _, it := range items {
			if it.id == r.PathValue("id") {
				writeJSON(w, http.StatusOK, map[string]any{"id": it.id, "name": it.name, "text": it.text})
				return
			}
		}
		writeError(w, http.StatusNotFound, "no such item")
	}))
	return mux
}

type item struct{ id, name, text string }

var items = []item{
	{"go1", "The Go gopher", "Go is an open source programming language."},
	{"go2", "Go generics", "Go 1.18 added type parameters."},
	{"rs1", "Rust ownership", "Rust checks borrows at compile time."},
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", "req-toy")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": msg}})
}

// ErrorBody formats an error the way the toy API does, for the conformance
// suite's error matrix.
func ErrorBody(message string) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"message": message}})
	return b
}
