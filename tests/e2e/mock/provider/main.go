package main

import (
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

// Mock Git Provider HTTP Server simulating GitHub, Gitea, and Forgejo APIs.
func main() {
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// GitHub API Endpoints
	mux.HandleFunc("/user", handleGitHubUser)
	mux.HandleFunc("/user/repos", handleGitHubUserRepos)
	mux.HandleFunc("/app", handleGitHubApp)
	mux.HandleFunc("/app/installations", handleGitHubInstallations)
	mux.HandleFunc("/repos/", handleGitHubRepos)
	mux.HandleFunc("/orgs/", handleGitHubOrgs)
	mux.HandleFunc("/enterprises/", handleGitHubEnterprises)

	// Specs' control surface over the registered-runners registry below: lets
	// E2E tests seed runner state (name/busy/online) without real runner
	// processes registering themselves at the forge.
	mux.HandleFunc("/_admin/runners", handleAdminRunners)
	mux.HandleFunc("/_admin/runners/", handleAdminRunnerByName)
	// Gitea / Forgejo API Endpoints
	mux.HandleFunc("/api/v1/user", handleGitUser)
	mux.HandleFunc("/api/v1/repos/", handleGitRepos)

	// Catch-all route to dump unhandled requests for easier debugging
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[mock-provider] Unhandled %s %s", r.Method, r.URL.Path)
		// Generic success or fallback
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "mock provider fallback response",
			"path":    r.URL.Path,
		})
	})

	port := ":8095"
	log.Printf("Starting mock Git Provider server on %s", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		log.Fatalf("Server exited: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Registered-runners registry (docs/19 §2.2, docs/20 §4.3)
//
// The E2E stack runs no real runner processes: containers exist only in the
// mock Docker daemon and never register themselves at the forge. Specs seed
// this registry through the /_admin/runners control endpoints so the GitHub
// runners-listing surface — polled by the supervisor for busy-state sync and
// consumed by the ghost sweep and the recycle/deregistration paths — can be
// simulated, including busy-flag flips.

type mockRunner struct {
	ID     int64             `json:"id"`
	Name   string            `json:"name"`
	Busy   bool              `json:"busy"`
	Status string            `json:"status"` // "online" | "offline"
	OS     string            `json:"os"`
	Labels []mockRunnerLabel `json:"labels"`
}

type mockRunnerLabel struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

var (
	runnersMu sync.Mutex
	runners         = make(map[string]*mockRunner) // keyed by runner name
	runnerSeq int64 = 9000
)

// upsertMockRunner creates or updates a registry entry; a non-"offline"
// status normalizes to "online".
func upsertMockRunner(name string, busy bool, status string) *mockRunner {
	runnersMu.Lock()
	defer runnersMu.Unlock()

	if status != "offline" {
		status = "online"
	}
	if r, ok := runners[name]; ok {
		r.Busy = busy
		r.Status = status
		return r
	}
	runnerSeq++
	runners[name] = &mockRunner{
		ID:     runnerSeq,
		Name:   name,
		Busy:   busy,
		Status: status,
		OS:     "Linux",
		Labels: []mockRunnerLabel{
			{ID: 1, Name: "self-hosted", Type: "read-only"},
			{ID: 2, Name: "Linux", Type: "read-only"},
		},
	}
	return runners[name]
}

func deleteMockRunner(name string) {
	runnersMu.Lock()
	defer runnersMu.Unlock()
	delete(runners, name)
}

// deleteMockRunnerByID removes by the listing-assigned id, mirroring the
// GitHub deregistration endpoint (DELETE .../actions/runners/{id}).
func deleteMockRunnerByID(id int64) {
	runnersMu.Lock()
	defer runnersMu.Unlock()
	for name, r := range runners {
		if r.ID == id {
			delete(runners, name)
			return
		}
	}
}

// listMockRunners returns the registry sorted by name for deterministic
// listings.
func listMockRunners() []*mockRunner {
	runnersMu.Lock()
	defer runnersMu.Unlock()

	out := make([]*mockRunner, 0, len(runners))
	for _, r := range runners {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// handleActionsRunnersPath serves the GitHub-style registered-runners
// collection from the shared registry for any scope (repo/org/enterprise):
// GET .../actions/runners lists ({total_count, runners[]}); DELETE
// .../actions/runners/{id} deregisters (docs/20 §4.3). Returns false when
// the path is neither shape, so the scope handlers fall through.
func handleActionsRunnersPath(w http.ResponseWriter, r *http.Request) bool {
	if strings.HasSuffix(r.URL.Path, "/actions/runners") {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return true
		}
		list := listMockRunners()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count": len(list),
			"runners":     list,
		})
		return true
	}

	if r.Method == http.MethodDelete {
		_, idSuffix, found := strings.Cut(r.URL.Path, "/actions/runners/")
		if found && idSuffix != "" {
			id, err := strconv.ParseInt(idSuffix, 10, 64)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return true
			}
			deleteMockRunnerByID(id)
			// GitHub answers 204; an unknown id counts as already deregistered.
			w.WriteHeader(http.StatusNoContent)
			return true
		}
	}
	return false
}

// handleAdminRunners is the specs' control surface over the registry: GET
// dumps it, PUT upserts one runner ({name, busy, status}).
func handleAdminRunners(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"runners": listMockRunners()})
	case http.MethodPut:
		var req struct {
			Name   string `json:"name"`
			Busy   bool   `json:"busy"`
			Status string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"body must be {name, busy, status}"}`))
			return
		}
		runner := upsertMockRunner(strings.TrimSpace(req.Name), req.Busy, req.Status)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(runner)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleAdminRunnerByName removes one registry entry by name (idempotent).
func handleAdminRunnerByName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/_admin/runners/"))
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	deleteMockRunner(name)
	w.WriteHeader(http.StatusNoContent)
}

func handleGitHubApp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":   123456,
		"name": "runnero-e2e-app",
		"slug": "runnero-e2e-app",
	})
}

func handleGitHubInstallations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(r.URL.Path, "/access_tokens") {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_mock_installation_token_1234567890",
			"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		})
		return
	}

	// Listing installations
	_ = json.NewEncoder(w).Encode([]map[string]any{
		{
			"id": 987654,
			"account": map[string]any{
				"login": "test-org",
				"type":  "Organization",
			},
		},
	})
}

func handleGitHubRepos(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Check for runner registration token endpoint
	if strings.HasSuffix(r.URL.Path, "/actions/runners/registration-token") {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "mock_registration_token_gh_actions_xyz",
			"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		})
		return
	}

	// Registered-runners listing / deregistration (docs/19 §2.2, docs/20 §4.3)
	if handleActionsRunnersPath(w, r) {
		return
	}

	if strings.HasSuffix(r.URL.Path, "/installation") {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 987654,
		})
		return
	}

	// Repository detail
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	repoName := "test-repo"
	if len(parts) >= 2 {
		repoName = parts[1]
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":        1001,
		"name":      repoName,
		"full_name": fmt.Sprintf("test-org/%s", repoName),
		"private":   true,
		"permissions": map[string]bool{
			"admin": true,
			"push":  true,
			"pull":  true,
		},
	})
}

func handleGitHubOrgs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/actions/runners/registration-token") {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "mock_registration_token_org_gh_actions_xyz",
			"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		})
		return
	}

	// Registered-runners listing / deregistration (docs/19 §2.2, docs/20 §4.3)
	if handleActionsRunnersPath(w, r) {
		return
	}

	if strings.HasSuffix(r.URL.Path, "/installation") {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 987654,
		})
		return
	}

	_ = json.NewEncoder(w).Encode([]map[string]any{
		{
			"id":        1001,
			"name":      "test-repo",
			"full_name": "test-org/test-repo",
			"private":   true,
		},
	})
}

// handleGitHubEnterprises serves GET /enterprises/{e} plus the enterprise-
// scoped registration-token and registered-runners endpoints (docs/19 §2.2).
func handleGitHubEnterprises(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/actions/runners/registration-token") {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "mock_registration_token_enterprise_xyz",
			"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		})
		return
	}

	if handleActionsRunnersPath(w, r) {
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":   987654,
		"slug": strings.Trim(strings.TrimPrefix(r.URL.Path, "/enterprises/"), "/"),
	})
}

// handleGitHubUser serves GET /user for GitHub PAT credential validation.
func handleGitHubUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"login": "e2e-user",
		"id":    1,
	})
}

// handleGitHubUserRepos serves GET /user/repos for GitHub PAT repository discovery.
func handleGitHubUserRepos(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode([]map[string]any{
		{
			"name":        "test-repo",
			"full_name":   "test-org/test-repo",
			"html_url":    "https://github.com/test-org/test-repo",
			"description": "Primary E2E test repository",
			"private":     true,
			"owner":       map[string]any{"avatar_url": ""},
		},
		{
			"name":        "backend-core",
			"full_name":   "test-org/backend-core",
			"html_url":    "https://github.com/test-org/backend-core",
			"description": "Core backend services",
			"private":     false,
			"owner":       map[string]any{"avatar_url": ""},
		},
	})
}

func handleGitUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":       5001,
		"username": "e2e-git-admin",
		"email":    "e2e-git-admin@example.com",
	})
}

func handleGitRepos(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/actions/runners/registration-token") {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "mock_gitea_runner_registration_token_123",
		})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":        6001,
		"name":      "test-gitea-repo",
		"full_name": "e2e-git-admin/test-gitea-repo",
	})
}
