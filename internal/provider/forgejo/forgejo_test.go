package forgejo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/provider/forgejo"
)

func setupMockForgejoServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// 1. /api/v1/user
	mux.HandleFunc("/api/v1/user", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "token valid-forgejo-pat" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"username":"forgejo-admin"}`))
	})

	// 2. /api/v1/repos/{owner}/{repo}/actions/runners/registration-token
	mux.HandleFunc("/api/v1/repos/my-org/my-repo/actions/runners/registration-token", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "token valid-forgejo-pat" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"forgejo-repo-runner-token-123"}`))
	})

	// 3. /api/v1/orgs/{org}/actions/runners/registration-token
	mux.HandleFunc("/api/v1/orgs/my-org/actions/runners/registration-token", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "token valid-forgejo-pat" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"forgejo-org-runner-token-456"}`))
	})

	// 4. /api/v1/admin/actions/runners/registration-token
	mux.HandleFunc("/api/v1/admin/actions/runners/registration-token", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "token valid-forgejo-pat" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"forgejo-global-runner-token-789"}`))
	})

	// 5. /api/v1/repos/{owner}/{repo}/actions/tasks (queued tasks list)
	mux.HandleFunc("/api/v1/repos/my-org/my-repo/actions/tasks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Returns 2 waiting tasks and 1 running task
		_, _ = w.Write([]byte(`[
			{"id": 1, "status": "waiting"},
			{"id": 2, "status": "running"},
			{"id": 3, "status": "waiting"}
		]`))
	})

	// 6. /api/v1/orgs/{org}/actions/tasks (queued tasks object with total_count)
	mux.HandleFunc("/api/v1/orgs/my-org/actions/tasks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count": 5, "tasks": []}`))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })
	return server
}

func TestForgejoClient(t *testing.T) {
	server := setupMockForgejoServer(t)
	ctx := context.Background()

	client, err := forgejo.NewClient("valid-forgejo-pat", forgejo.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	// 1. ValidateCredentials
	if err := client.ValidateCredentials(ctx); err != nil {
		t.Fatalf("ValidateCredentials failed: %v", err)
	}

	// Bad PAT
	badClient, err := forgejo.NewClient("bad-pat", forgejo.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient bad-pat failed: %v", err)
	}
	if err := badClient.ValidateCredentials(ctx); err == nil {
		t.Fatal("expected error with bad PAT")
	}

	// 2. ScalingMode is ScalingPolling
	if client.ScalingMode() != provider.ScalingPolling {
		t.Errorf("expected ScalingPolling, got %v", client.ScalingMode())
	}

	// 3. PollQueuedJobs for Repo (returns 2 waiting tasks)
	repoURL := server.URL + "/my-org/my-repo"
	count, err := client.PollQueuedJobs(ctx, provider.PollTarget{URL: repoURL, Scope: provider.ScopeRepo})
	if err != nil {
		t.Fatalf("PollQueuedJobs repo failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 queued jobs, got %d", count)
	}

	// 4. PollQueuedJobs for Org (returns total_count 5)
	orgURL := server.URL + "/my-org"
	orgCount, err := client.PollQueuedJobs(ctx, provider.PollTarget{URL: orgURL, Scope: provider.ScopeOrg})
	if err != nil {
		t.Fatalf("PollQueuedJobs org failed: %v", err)
	}
	if orgCount != 5 {
		t.Errorf("expected 5 queued jobs, got %d", orgCount)
	}

	// 5. GetRegistrationToken across scopes
	repoToken, err := client.GetRegistrationToken(ctx, provider.ScopeRepo, repoURL+".git")
	if err != nil {
		t.Fatalf("GetRegistrationToken repo failed: %v", err)
	}
	if repoToken != "forgejo-repo-runner-token-123" {
		t.Errorf("unexpected repo token: %q", repoToken)
	}

	orgToken, err := client.GetRegistrationToken(ctx, provider.ScopeOrg, orgURL)
	if err != nil {
		t.Fatalf("GetRegistrationToken org failed: %v", err)
	}
	if orgToken != "forgejo-org-runner-token-456" {
		t.Errorf("unexpected org token: %q", orgToken)
	}

	globalToken, err := client.GetRegistrationToken(ctx, provider.ScopeGlobal, server.URL)
	if err != nil {
		t.Fatalf("GetRegistrationToken global failed: %v", err)
	}
	if globalToken != "forgejo-global-runner-token-789" {
		t.Errorf("unexpected global token: %q", globalToken)
	}

	// 6. GetRenovateToken returns PAT
	renovateToken, err := client.GetRenovateToken(ctx, repoURL)
	if err != nil {
		t.Fatalf("GetRenovateToken failed: %v", err)
	}
	if renovateToken != "valid-forgejo-pat" {
		t.Errorf("unexpected renovate token: %q", renovateToken)
	}
}

func TestForgejoRegistryIntegration(t *testing.T) {
	ctx := context.Background()

	prov, err := provider.DefaultRegistry.Build(ctx, db.DecryptedAuthProfile{
		AuthProfile: db.AuthProfile{
			AuthMethod: string(provider.AuthMethodForgejoToken),
		},
		Token: "forgejo-secret-pat",
	})
	if err != nil {
		t.Fatalf("failed to build forgejo provider from registry: %v", err)
	}
	if prov.ScalingMode() != provider.ScalingPolling {
		t.Errorf("expected polling scaling mode")
	}

	// Empty token fails
	_, err = provider.DefaultRegistry.Build(ctx, db.DecryptedAuthProfile{
		AuthProfile: db.AuthProfile{
			AuthMethod: string(provider.AuthMethodForgejoToken),
		},
		Token: "",
	})
	if err == nil || !strings.Contains(err.Error(), "token is required") {
		t.Fatalf("expected missing credentials error, got %v", err)
	}
}

func TestForgejoDiscoveryPagination(t *testing.T) {
	mux := http.NewServeMux()

	// 1. /api/v1/user/repos
	mux.HandleFunc("/api/v1/user/repos", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		if page == "1" || page == "" {
			var repos []map[string]any
			for i := 1; i <= 100; i++ {
				repos = append(repos, map[string]any{
					"name":      fmt.Sprintf("repo-%d", i),
					"full_name": fmt.Sprintf("org/repo-%d", i),
					"html_url":  fmt.Sprintf("https://forgejo.example.com/org/repo-%d", i),
					"private":   false,
				})
			}
			_ = json.NewEncoder(w).Encode(repos)
			return
		}
		if page == "2" {
			var repos []map[string]any
			for i := 101; i <= 122; i++ {
				repos = append(repos, map[string]any{
					"name":      fmt.Sprintf("repo-%d", i),
					"full_name": fmt.Sprintf("org/repo-%d", i),
					"html_url":  fmt.Sprintf("https://forgejo.example.com/org/repo-%d", i),
					"private":   false,
				})
			}
			_ = json.NewEncoder(w).Encode(repos)
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})

	// 2. /api/v1/user/orgs
	mux.HandleFunc("/api/v1/user/orgs", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		if page == "1" || page == "" {
			var orgs []map[string]any
			for i := 1; i <= 100; i++ {
				orgs = append(orgs, map[string]any{
					"username": fmt.Sprintf("org-%d", i),
				})
			}
			_ = json.NewEncoder(w).Encode(orgs)
			return
		}
		if page == "2" {
			var orgs []map[string]any
			for i := 101; i <= 114; i++ {
				orgs = append(orgs, map[string]any{
					"username": fmt.Sprintf("org-%d", i),
				})
			}
			_ = json.NewEncoder(w).Encode(orgs)
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	ctx := context.Background()
	client, err := forgejo.NewClient("valid-forgejo-pat", forgejo.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	repos, err := client.DiscoverRepositories(ctx)
	if err != nil {
		t.Fatalf("DiscoverRepositories failed: %v", err)
	}
	if len(repos) != 122 {
		t.Fatalf("expected 122 repos, got %d", len(repos))
	}

	orgs, err := client.DiscoverOrganizations(ctx)
	if err != nil {
		t.Fatalf("DiscoverOrganizations failed: %v", err)
	}
	if len(orgs) != 114 {
		t.Fatalf("expected 114 orgs, got %d", len(orgs))
	}
}
