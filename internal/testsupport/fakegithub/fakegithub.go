// Package fakegithub provides a minimal in-process fake of the GitHub API
// surface the supervisor touches during boot: credential validation
// (GET /user for PATs, GET /app for GitHub Apps). Daemon tests point
// GITHUB_BASE_URL at it (see setFakeGitHub in cmd/runnero-supervisor) so
// seeded pools validate against the fake instead of the real api.github.com
// (RUN-149) — keeping unit tests hermetic: no outbound network dependency,
// no real API traffic with junk credentials.
package fakegithub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Request records one HTTP request the fake received, for assertions on
// what the supervisor actually sent.
type Request struct {
	Method        string
	Path          string
	Authorization string
}

// Fake is a running fake GitHub API. Create one with New and close it with
// Close (or t.Cleanup(fake.Close)).
type Fake struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests []Request
}

// New starts a fake GitHub API on a loopback port and returns it.
func New() *Fake {
	f := &Fake{}
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		writeJSON(w, map[string]any{"login": "runnero-test", "id": 1, "type": "User"})
	})
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		writeJSON(w, map[string]any{"id": 1, "slug": "runnero-test-app"})
	})
	f.srv = httptest.NewServer(mux)
	return f
}

// Close shuts the fake down.
func (f *Fake) Close() { f.srv.Close() }

// URL returns the fake's base URL in GITHUB_BASE_URL form
// (e.g. "http://127.0.0.1:41234").
func (f *Fake) URL() string { return f.srv.URL }

// Requests returns a copy of every request the fake has received so far.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Request, len(f.requests))
	copy(out, f.requests)
	return out
}

func (f *Fake) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, Request{
		Method:        r.Method,
		Path:          r.URL.Path,
		Authorization: r.Header.Get("Authorization"),
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}
