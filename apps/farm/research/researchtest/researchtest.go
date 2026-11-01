// Package researchtest fakes the Exa and Parallel APIs for tests and
// evals: every search answers with the same three made-up pages about
// paddy varieties for the Godavari delta, on example.org, so nothing a
// model cites from them can be mistaken for a real source.
package researchtest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Page is a made-up web page.
type Page struct {
	Title, URL, Date, Text string
}

// Pages are what every search finds.
var Pages = []Page{
	{
		Title: "Kharif paddy varieties for the Godavari delta (example extension leaflet)",
		URL:   "https://example.org/leaflets/godavari-delta-paddy-varieties",
		Date:  "2026-05-20",
		Text:  "For the Godavari delta in kharif, short-duration varieties of 125 to 135 days with tolerance to submergence suit low-lying fields. The leaflet lists three example varieties, Delta-1 (130 days, tolerates 10 days of submergence), Delta-2 (125 days, fine grain) and Delta-3 (140 days, for well-drained fields). Seed rate 25 kg per acre for transplanting.",
	},
	{
		Title: "Paddy seed prices, Andhra Pradesh, kharif 2026 (example market bulletin)",
		URL:   "https://example.org/bulletins/ap-paddy-seed-prices-2026",
		Date:  "2026-06-02",
		Text:  "Certified seed of Delta-1 sold at Rs 42 per kg and Delta-2 at Rs 48 per kg at the example seed outlets in Amalapuram in June 2026. Subsidised seed is available through the village agriculture assistant.",
	},
	{
		Title: "Submergence-tolerant rice in Bangladesh's coastal districts (example research note)",
		URL:   "https://example.org/notes/bangladesh-submergence-rice",
		Date:  "2025-11-10",
		Text:  "Farmers in Bangladesh's coastal districts, with a monsoon climate like coastal Andhra Pradesh, adopted submergence-tolerant rice and raised kharif yields in flood years by about a fifth in the example trials.",
	},
}

// Server is a fake of both APIs. It counts its calls and keeps the last
// request body.
type Server struct {
	*httptest.Server
	mu    sync.Mutex
	calls map[string]int
	last  map[string]any
}

// New starts a fake. Close it when done.
func New() *Server {
	s := &Server{calls: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Calls returns how many requests a path got ("/search", "/contents",
// "/v1beta/search", "/v1beta/extract", ...).
func (s *Server) Calls(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[path]
}

// Last returns the last request's body.
func (s *Server) Last() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	s.mu.Lock()
	s.calls[r.URL.Path]++
	s.last = req
	s.mu.Unlock()
	if r.Header.Get("Authorization") == "" && r.Header.Get("X-Api-Key") == "" && r.Header.Get("x-api-key") == "" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"no key"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	var out any
	switch r.URL.Path {
	case "/search": // Exa
		var rs []map[string]any
		for _, p := range Pages {
			rs = append(rs, map[string]any{"id": p.URL, "url": p.URL, "title": p.Title, "publishedDate": p.Date + "T00:00:00.000Z", "highlights": []string{p.Text}})
		}
		out = map[string]any{"requestId": "fake", "results": rs}
	case "/contents": // Exa
		var rs, st []map[string]any
		for _, p := range Pages {
			rs = append(rs, map[string]any{"id": p.URL, "url": p.URL, "title": p.Title, "text": p.Text})
			st = append(st, map[string]any{"id": p.URL, "status": "success"})
		}
		out = map[string]any{"requestId": "fake", "results": rs, "statuses": st}
	case "/v1beta/search", "/v1/search": // Parallel
		var rs []map[string]any
		for _, p := range Pages {
			rs = append(rs, map[string]any{"url": p.URL, "title": p.Title, "publish_date": p.Date, "excerpts": []string{p.Text}})
		}
		out = map[string]any{"search_id": "search_fake", "results": rs, "warnings": nil}
	case "/v1beta/extract", "/v1/extract":
		var rs []map[string]any
		for _, p := range Pages {
			rs = append(rs, map[string]any{"url": p.URL, "title": p.Title, "publish_date": p.Date, "excerpts": []string{p.Text}, "full_content": p.Text})
		}
		out = map[string]any{"extract_id": "extract_fake", "results": rs, "errors": []any{}, "warnings": nil}
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"not found"}`)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}
