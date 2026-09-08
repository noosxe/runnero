package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type containerState struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Created int64             `json:"Created"`
	Labels  map[string]string `json:"Labels"`
}

var (
	mu         sync.Mutex
	containers = make(map[string]*containerState)
	counter    = 1
)

// Mock Docker Daemon HTTP engine simulating Docker Engine API (v1.40 - v1.56).
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

	// Events stream
	mux.HandleFunc("/events", handleEvents)
	mux.HandleFunc("/v1.56/events", handleEvents)

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

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

func handleContainersPrune(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()

	deleted := make([]string, 0)
	for id, c := range containers {
		if c.State == "exited" || c.State == "dead" {
			deleted = append(deleted, id)
			delete(containers, id)
		}
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
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	case "stop":
		mu.Lock()
		cnt.State = "exited"
		cnt.Status = "Exited (0) 1 second ago"
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	case "logs":
		handleContainerLogs(w, r, id)

	default:
		if r.Method == http.MethodDelete {
			mu.Lock()
			delete(containers, id)
			mu.Unlock()
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

func handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	flusher, canFlush := w.(http.Flusher)

	// Keep stream open emitting periodic heartbeat
	event := map[string]any{
		"Type":   "container",
		"Action": "status",
		"Actor": map[string]any{
			"ID": "heartbeat",
		},
		"time": time.Now().Unix(),
	}
	_ = json.NewEncoder(w).Encode(event)
	if canFlush {
		flusher.Flush()
	}
}
