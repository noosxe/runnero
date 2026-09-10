// Package fakedocker provides an in-process fake Docker Engine API server so
// tests can boot the production supervisor daemon (or any Docker-backed
// component) without touching a real engine (RUN-143).
//
// The fake speaks just enough of the Engine HTTP API for a boot with zero
// managed containers: /_ping for health and degraded-mode probes,
// /containers/json for state rebuild, and a /events stream that blocks until
// the client disconnects. Any other endpoint answers 404 with a Docker-shaped
// error body, so coverage drift fails loudly instead of silently.
//
// The fake is transport-level on purpose: it decouples tests from the Docker
// SDK version, and future orchestration tests can drive realistic scenarios
// (adoption, reaping, termination) by seeding containers via SetContainers.
package fakedocker

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"time"
)

// APIVersion is the Engine API version the fake advertises via the
// Api-Version header on /_ping.
const APIVersion = "1.52"

// eventsBackstop bounds how long a /events handler waits for a client that
// never disconnects, so a buggy test fails fast at Server.Close instead of
// hanging forever.
const eventsBackstop = 5 * time.Minute

// versionPrefix matches the "/v1.52" style prefix the Docker SDK prepends to
// versioned endpoints (ping is unversioned).
var versionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+`)

// Container is the subset of a Docker container-list entry the fake can serve.
type Container struct {
	Id      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Labels  map[string]string `json:"Labels"`
	Created int64             `json:"Created"`
}

// Request records one HTTP request the fake received, for assertions on what
// a component actually asked the engine to do.
type Request struct {
	Method string
	Path   string
}

// Fake is a running fake Docker Engine. Create one with New and close it with
// Close (or t.Cleanup(fake.Close)).
type Fake struct {
	srv *httptest.Server

	mu            sync.Mutex
	containers    []Container
	requests      []Request
	eventsStreams int // in-flight /events handlers (RUN-160 leak assertions)
}

// New starts a fake Docker Engine on a loopback port and returns it. The
// engine initially has zero containers.
func New() *Fake {
	f := &Fake{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

// Host returns the endpoint in SUPERVISOR_DOCKER_HOST / DOCKER_HOST form,
// e.g. "tcp://127.0.0.1:41234".
func (f *Fake) Host() string {
	return "tcp://" + strings.TrimPrefix(f.srv.URL, "http://")
}

// Close shuts the fake down. It blocks until all in-flight handlers (notably
// any /events stream) have returned.
func (f *Fake) Close() {
	f.srv.Close()
}

// CloseClientConnections force-closes every open client connection,
// in-flight /events streams included. Leak-assertion tests call it after a
// failed assertion so the t.Cleanup(fake.Close) that follows is not itself
// blocked by the leak being reported (RUN-160).
func (f *Fake) CloseClientConnections() {
	f.srv.CloseClientConnections()
}

// EventsStreamsActive reports how many /events stream handlers are currently
// in flight. Boot-failure tests assert zero after the daemon returns (RUN-160):
// a leaked stream means the daemon's teardown did not release it, and Close()
// would block until the 5-minute eventsBackstop fires.
func (f *Fake) EventsStreamsActive() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.eventsStreams
}

// SetContainers replaces the container list served by /containers/json, so
// tests can stage state for adoption/reap scenarios.
func (f *Fake) SetContainers(containers []Container) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if containers == nil {
		containers = []Container{}
	}
	f.containers = containers
}

// Requests returns a copy of every request the fake has received so far.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Request, len(f.requests))
	copy(out, f.requests)
	return out
}

// Ping verifies the fake is answering, so tests fail fast with a clear
// message if the fake itself is broken rather than as a confusing daemon
// health failure later.
func (f *Fake) Ping() error {
	resp, err := http.Get(f.srv.URL + "/_ping")
	if err != nil {
		return fmt.Errorf("fakedocker: ping: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fakedocker: ping: status %d", resp.StatusCode)
	}
	return nil
}

func (f *Fake) handle(w http.ResponseWriter, r *http.Request) {
	f.record(Request{Method: r.Method, Path: r.URL.Path})

	path := versionPrefix.ReplaceAllString(r.URL.Path, "")
	switch path {
	case "/_ping":
		f.handlePing(w, r)
	case "/containers/json":
		f.handleContainerList(w)
	case "/events":
		f.handleEvents(w, r)
	default:
		writeError(w, http.StatusNotFound,
			fmt.Sprintf("fakedocker: no handler for %s %s (stub covers /_ping, /containers/json, /events)", r.Method, path))
	}
}

func (f *Fake) handlePing(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Api-Version", APIVersion)
	h.Set("Ostype", "linux")
	h.Set("Docker-Experimental", "false")
	h.Set("Builder-Version", "2")
	h.Set("Swarm", "inactive")
	h.Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, "OK")
	}
}

func (f *Fake) handleContainerList(w http.ResponseWriter) {
	f.mu.Lock()
	containers := f.containers
	f.mu.Unlock()
	if containers == nil {
		containers = []Container{}
	}
	writeJSON(w, containers)
}

// handleEvents blocks until the client disconnects — daemon shutdown cancels
// the stream's context, which releases this handler — or until the backstop
// fires. No events are ever emitted.
func (f *Fake) handleEvents(_ http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.eventsStreams++
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.eventsStreams--
		f.mu.Unlock()
	}()

	select {
	case <-r.Context().Done():
	case <-time.After(eventsBackstop):
	}
}

func (f *Fake) record(req Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers with the Docker Engine error wire shape
// ({"message": "..."}) so SDK clients decode a typed error instead of
// failing on an unparseable body.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}
