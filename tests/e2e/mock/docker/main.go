package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mock Docker Daemon HTTP engine simulating the Docker Engine API subset the
// supervisor uses (v1.40 - v1.56), including container lifecycle events.
//
// Lifecycle fidelity (RUN-165): production runners are ephemeral — a runner
// container picks up one job and exits itself, and the supervisor reaps it
// through the docker `die` event path. This mock reproduces that semantics:
//
//   - state transitions (start / die / destroy) are broadcast to /events
//     subscribers with server-side filter application (type, event, label),
//   - POST /_admin/containers/{name|id}/exit marks a running container
//     exited and broadcasts `die` — the E2E "job completed" primitive,
//   - DELETE /containers/{id} broadcasts `die` (when running) + `destroy`,
//     mirroring `docker rm -f`,
//   - prune broadcasts `destroy` per reclaimed container.
//
// Specs drive the half-states deliberately (busy released but container
// alive, container gone but still tracked) to exercise busy-sync (docs/19)
// and the ghost sweep (docs/20) — the admin API keeps those writable.

type containerState struct {
	ID       string            `json:"Id"`
	Names    []string          `json:"Names"`
	Image    string            `json:"Image"`
	State    string            `json:"State"`
	Status   string            `json:"Status"`
	Created  int64             `json:"Created"`
	Labels   map[string]string `json:"Labels"`
	ExitCode int               `json:"-"` // surfaced via die-event attributes
}

// eventFilter is the server-side filter set a /events subscriber asked for
// (the real daemon applies these before streaming; so do we).
type eventFilter struct {
	types   map[string]bool
	actions map[string]bool
	labels  map[string]string // k=v pairs, all must match
}

// parseFilters decodes the daemon's `filters` query parameter:
// {"type":{"container":true},"event":{"die":true},"label":{"k=v":true}}.
func parseFilters(raw string) eventFilter {
	f := eventFilter{
		types:   map[string]bool{},
		actions: map[string]bool{},
		labels:  map[string]string{},
	}
	if raw == "" {
		return f
	}
	var parsed map[string]map[string]bool
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		log.Printf("[mock-docker] unparseable filters %q: %v", raw, err)
		return f
	}
	for k, vals := range parsed {
		for v := range vals {
			switch k {
			case "type":
				f.types[v] = true
			case "event":
				f.actions[v] = true
			case "label":
				if eq := strings.Index(v, "="); eq >= 0 {
					f.labels[v[:eq]] = v[eq+1:]
				} else {
					f.labels[v] = ""
				}
			}
		}
	}
	return f
}

// matches reports whether ev passes f. Container-scoped checks only run when
// the event carries container labels; engine-level events (none today) would
// fail an explicit label filter, as on a real daemon.
func (f eventFilter) matches(ev dockerEvent, labels map[string]string) bool {
	if len(f.types) > 0 && !f.types[ev.Type] {
		return false
	}
	if len(f.actions) > 0 && !f.actions[ev.Action] {
		return false
	}
	for k, want := range f.labels {
		if labels[k] != want {
			return false
		}
	}
	return true
}

// dockerEvent mirrors moby/api/types/events.Message's wire format: Type,
// Action and Actor marshal without json tags (Go field names), while time /
// timeNano are lowercased. Attributes is map[string]string — numbers must be
// encoded as strings.
type dockerEvent struct {
	Type     string           `json:"Type"`
	Action   string           `json:"Action"`
	Actor    dockerEventActor `json:"Actor"`
	Time     int64            `json:"time"`
	TimeNano int64            `json:"timeNano"`
}

type dockerEventActor struct {
	ID         string            `json:"ID"`
	Attributes map[string]string `json:"Attributes"`
}

// queuedEvent pairs an event with the container-label snapshot broadcast
// alongside it — filtering matches those labels even after the container
// object is gone (a daemon retains config labels for destroy events).
type queuedEvent struct {
	ev     dockerEvent
	labels map[string]string
}

// eventBus fans state transitions out to /events subscribers.
type eventBus struct {
	mu     sync.Mutex
	nextID int
	subs   map[int]chan queuedEvent
}

func newEventBus() *eventBus {
	return &eventBus{subs: map[int]chan queuedEvent{}}
}

func (b *eventBus) subscribe() (int, <-chan queuedEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	ch := make(chan queuedEvent, 256)
	b.subs[b.nextID] = ch
	return b.nextID, ch
}

func (b *eventBus) unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(ch)
	}
}

// broadcast queues ev with its label snapshot for every subscriber; the
// subscriber's own filter decides delivery. Buffered channels decouple slow
// readers so a stalled spec cannot wedge the daemon.
func (b *eventBus) broadcast(ev dockerEvent, labels map[string]string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	snapshot := make(map[string]string, len(labels))
	for k, v := range labels {
		snapshot[k] = v
	}
	qe := queuedEvent{ev: ev, labels: snapshot}
	for _, ch := range b.subs {
		select {
		case ch <- qe:
		default:
			// Slow consumer; real daemons drop nothing, but a wedged E2E
			// subscriber means the spec has already failed elsewhere.
			log.Printf("[mock-docker] dropping event %s for a slow subscriber", ev.Action)
		}
	}
}

var (
	mu         sync.Mutex
	containers = make(map[string]*containerState)
	counter    = 1
	hub        = newEventBus()
)

func nowTimes() (int64, int64) {
	now := time.Now()
	return now.Unix(), now.UnixNano()
}

// broadcastDie emits a die event for cnt with its exit code and labels in the
// actor attributes — the shape EventListener parses on the supervisor side.
func broadcastDie(cnt *containerState) {
	attrs := actorAttributes(cnt)
	attrs["exitCode"] = strconv.Itoa(cnt.ExitCode)
	sec, nsec := nowTimes()
	hub.broadcast(dockerEvent{
		Type:     "container",
		Action:   "die",
		Actor:    dockerEventActor{ID: cnt.ID, Attributes: attrs},
		Time:     sec,
		TimeNano: nsec,
	}, cnt.Labels)
}

func broadcastDestroy(cnt *containerState) {
	sec, nsec := nowTimes()
	hub.broadcast(dockerEvent{
		Type:     "container",
		Action:   "destroy",
		Actor:    dockerEventActor{ID: cnt.ID, Attributes: actorAttributes(cnt)},
		Time:     sec,
		TimeNano: nsec,
	}, cnt.Labels)
}

func broadcastStart(cnt *containerState) {
	sec, nsec := nowTimes()
	hub.broadcast(dockerEvent{
		Type:     "container",
		Action:   "start",
		Actor:    dockerEventActor{ID: cnt.ID, Attributes: actorAttributes(cnt)},
		Time:     sec,
		TimeNano: nsec,
	}, cnt.Labels)
}

func actorAttributes(cnt *containerState) map[string]string {
	attrs := map[string]string{"name": cnt.Names[0]}
	for k, v := range cnt.Labels {
		attrs[k] = v
	}
	return attrs
}

// exitContainer marks a container exited and broadcasts its die event.
// exitCode semantics follow production ephemeral runners: a clean job run
// exits 0.
func exitContainer(cnt *containerState, exitCode int) {
	mu.Lock()
	cnt.State = "exited"
	cnt.ExitCode = exitCode
	cnt.Status = fmt.Sprintf("Exited (%d) %s", exitCode, "1 second ago")
	mu.Unlock()
	broadcastDie(cnt)
}

func main() {
	mux := http.NewServeMux()

	// Ping endpoint
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.56")
		w.Header().Set("Docker-Experimental", "false")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	// Version endpoint
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Version":       "28.0.0",
			"ApiVersion":    "1.56",
			"MinAPIVersion": "1.24",
			"GitCommit":     "mockcommit",
			"GoVersion":     "go1.26.0",
			"Os":            "linux",
			"Arch":          "amd64",
		})
	})

	// Networks
	mux.HandleFunc("/networks", handleNetworks)
	mux.HandleFunc("/v1.56/networks", handleNetworks)

	// Containers
	mux.HandleFunc("/containers/create", handleContainerCreate)
	mux.HandleFunc("/v1.56/containers/create", handleContainerCreate)

	mux.HandleFunc("/containers/json", handleContainersList)
	mux.HandleFunc("/v1.56/containers/json", handleContainersList)

	mux.HandleFunc("/containers/prune", handleContainersPrune)
	mux.HandleFunc("/v1.56/containers/prune", handleContainersPrune)

	// Route individual container commands: start, stop, logs, remove
	mux.HandleFunc("/containers/", handleContainerRoute)
	mux.HandleFunc("/v1.56/containers/", handleContainerRoute)

	// Events stream with real lifecycle fan-out (RUN-165)
	mux.HandleFunc("/events", handleEvents)
	mux.HandleFunc("/v1.56/events", handleEvents)

	// Spec-facing admin API: inspect and drive container lifecycles.
	mux.HandleFunc("/_admin/containers", handleAdminContainers)
	mux.HandleFunc("/_admin/containers/", handleAdminContainerByName)

	// Catch-all
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[mock-docker] Unhandled %s %s", r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	port := ":2375"
	log.Printf("Starting mock Docker daemon on %s", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		log.Fatalf("Mock docker exited: %v", err)
	}
}

func handleNetworks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id": "net-e2e-mock-123456",
		})
		return
	}

	_ = json.NewEncoder(w).Encode([]map[string]any{
		{
			"Id":     "net-e2e-mock-123456",
			"Name":   "runnero-net",
			"Driver": "bridge",
			"Scope":  "local",
		},
	})
}

func handleContainerCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	mu.Lock()
	id := fmt.Sprintf("cnt-mock-%04d", counter)

	// The Docker API carries the requested container name as the ?name= query
	// parameter. Honoring it keeps the supervisor's spawn-time runner names
	// (runnero-<pool-slug>-<hex>) stable across audit cycles — the busy-state
	// sync (docs/19) and ghost sweep (docs/20) match registered runners by
	// exactly these names, and churned names would break that matching.
	name := r.URL.Query().Get("name")
	if name == "" {
		name = fmt.Sprintf("runnero-runner-%04d", counter)
	}
	counter++

	cnt := &containerState{
		ID:      id,
		Names:   []string{"/" + name},
		Image:   req.Image,
		State:   "created",
		Status:  "Created",
		Created: time.Now().Unix(),
		Labels:  req.Labels,
	}
	containers[id] = cnt
	mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"Id":       id,
		"Warnings": []string{},
	})
}

func handleContainersList(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()

	list := make([]*containerState, 0, len(containers))
	for _, c := range containers {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created < list[j].Created })

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

func handleContainersPrune(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	deleted := make([]string, 0)
	pruned := make([]*containerState, 0)
	for id, c := range containers {
		if c.State == "exited" || c.State == "dead" {
			deleted = append(deleted, id)
			pruned = append(pruned, c)
			delete(containers, id)
		}
	}
	mu.Unlock()

	// Real daemons emit destroy per reclaimed container (RUN-165).
	for _, c := range pruned {
		broadcastDestroy(c)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ContainersDeleted": deleted,
		"SpaceReclaimed":    0,
	})
}

func handleContainerRoute(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1.56/containers/")
	path = strings.TrimPrefix(path, "/containers/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}

	id := parts[0]
	action := parts[1]

	mu.Lock()
	cnt, exists := containers[id]
	mu.Unlock()

	if !exists && action != "logs" {
		http.NotFound(w, r)
		return
	}

	switch action {
	case "start":
		mu.Lock()
		cnt.State = "running"
		cnt.Status = "Up 5 seconds"
		cnt.ExitCode = 0
		mu.Unlock()
		broadcastStart(cnt)
		w.WriteHeader(http.StatusNoContent)

	case "stop":
		// docker stop: SIGTERM the main process; the resulting death
		// broadcasts die like any other exit (RUN-165).
		exitCode := 137
		if raw := r.URL.Query().Get("exitCode"); raw != "" {
			if code, err := strconv.Atoi(raw); err == nil {
				exitCode = code
			}
		}
		exitContainer(cnt, exitCode)
		w.WriteHeader(http.StatusNoContent)

	case "logs":
		handleContainerLogs(w, r, id)

	default:
		if r.Method == http.MethodDelete {
			// docker rm -f: a running container dies first, then the
			// container object is destroyed (RUN-165).
			mu.Lock()
			wasRunning := cnt.State == "running"
			delete(containers, id)
			mu.Unlock()
			if wasRunning {
				exitContainer(cnt, 137)
			}
			broadcastDestroy(cnt)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func handleContainerLogs(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
	w.WriteHeader(http.StatusOK)

	flusher, canFlush := w.(http.Flusher)

	// Write mock multiplexed runner log lines formatted as Docker stdcopy frames
	lines := []string{
		fmt.Sprintf("[Runner Initialization] Starting ephemeral runner container %s", id),
		"Fetching runner registration token from Git provider...",
		"Registration token acquired successfully. Token: mock-tok-****",
		"Connecting to GitHub Actions listener daemon...",
		"Runner successfully connected! Listening for Jobs.",
	}

	for _, line := range lines {
		// Header format: [1 byte stream type (1=stdout)][3 bytes 0][4 bytes size uint32]
		payload := []byte(line + "\n")
		header := make([]byte, 8)
		header[0] = 1 // Stdout
		binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))

		_, _ = w.Write(header)
		_, _ = w.Write(payload)
		if canFlush {
			flusher.Flush()
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// handleEvents streams lifecycle events to one subscriber, applying the
// requested filters server-side like the real daemon. The stream stays open
// until the client disconnects — EventListener reconnects with backoff.
func handleEvents(w http.ResponseWriter, r *http.Request) {
	filter := parseFilters(r.URL.Query().Get("filters"))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	flusher, canFlush := w.(http.Flusher)
	if canFlush {
		flusher.Flush()
	}

	subID, events := hub.subscribe()
	defer hub.unsubscribe(subID)

	for {
		select {
		case <-r.Context().Done():
			return
		case qe, ok := <-events:
			if !ok {
				return
			}
			if !filter.matches(qe.ev, qe.labels) {
				continue
			}
			if err := json.NewEncoder(w).Encode(qe.ev); err != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
	}
}

// handleAdminContainers lists every container the mock tracks — the spec-side
// view for assertions and debugging.
func handleAdminContainers(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()

	type adminContainer struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		State    string `json:"state"`
		ExitCode int    `json:"exitCode"`
	}
	list := make([]adminContainer, 0, len(containers))
	for _, c := range containers {
		name := c.Names[0]
		list = append(list, adminContainer{
			ID:       c.ID,
			Name:     strings.TrimPrefix(name, "/"),
			State:    c.State,
			ExitCode: c.ExitCode,
		})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

// handleAdminContainerByName drives a container's lifecycle by name or ID:
//
//	POST /_admin/containers/{name|id}/exit  {"exitCode": 0}
//
// exit is the E2E "job completed" primitive (RUN-165): the container exits
// itself like an ephemeral runner and the supervisor reaps it through the
// die-event path. Unknown containers 404 so spec typos fail loudly.
func handleAdminContainerByName(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/_admin/containers/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	nameOrID := parts[0]

	mu.Lock()
	var cnt *containerState
	for _, c := range containers {
		if c.ID == nameOrID || strings.TrimPrefix(c.Names[0], "/") == nameOrID {
			cnt = c
			break
		}
	}
	mu.Unlock()

	if cnt == nil {
		http.NotFound(w, r)
		return
	}

	switch parts[1] {
	case "exit":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		exitCode := 0
		var req struct {
			ExitCode int `json:"exitCode"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&req)
			exitCode = req.ExitCode
		}
		exitContainer(cnt, exitCode)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"exited": cnt.ID, "exitCode": exitCode})
	default:
		http.NotFound(w, r)
	}
}
