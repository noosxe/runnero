package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
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
