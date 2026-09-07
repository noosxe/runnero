package gitea_test

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
	"github.com/noosxe/runnero/internal/provider/gitea"
)

func setupMockGiteaServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// 1. /api/v1/user
	mux.HandleFunc("/api/v1/user", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "token valid-gitea-pat" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"username":"gitea-admin"}`))
	})

	// 2. /api/v1/repos/{owner}/{repo}/actions/runners/registration-token
	mux.HandleFunc("/api/v1/repos/my-org/my-repo/actions/runners/registration-token", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "token valid-gitea-pat" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"gitea-repo-runner-token-123"}`))
	})

	// 3. /api/v1/orgs/{org}/actions/runners/registration-token (simulating 405 on GET, 200 on POST)
	mux.HandleFunc("/api/v1/orgs/my-org/actions/runners/registration-token", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "token valid-gitea-pat" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"message":"Method Not Allowed"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"gitea-org-runner-token-456"}`))
	})

	// 4. /api/v1/admin/actions/runners/registration-token
	mux.HandleFunc("/api/v1/admin/actions/runners/registration-token", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "token valid-gitea-pat" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"gitea-global-runner-token-789"}`))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })
	return server
}

func TestGiteaClient(t *testing.T) {
	server := setupMockGiteaServer(t)
	ctx := context.Background()

	client, err := gitea.NewClient("valid-gitea-pat", gitea.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	// 1. ValidateCredentials
	if err := client.ValidateCredentials(ctx); err != nil {
		t.Fatalf("ValidateCredentials failed: %v", err)
	}

	// Bad credentials
	badClient, err := gitea.NewClient("bad-pat", gitea.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient bad-pat failed: %v", err)
	}
	if err := badClient.ValidateCredentials(ctx); err == nil {
		t.Fatal("expected error with bad PAT")
	}

	// 2. ScalingMode & PollQueuedJobs
	if client.ScalingMode() != provider.ScalingWebhook {
		t.Errorf("expected ScalingWebhook, got %v", client.ScalingMode())
	}
	if q, err := client.PollQueuedJobs(ctx, server.URL+"/my-org/my-repo"); err != nil || q != 0 {
		t.Errorf("expected 0 queued jobs, got %d, err: %v", q, err)
	}

	// 3. Repo scope registration token (with .git suffix)
	repoURL := server.URL + "/my-org/my-repo.git"
	repoToken, err := client.GetRegistrationToken(ctx, provider.ScopeRepo, repoURL)
	if err != nil {
		t.Fatalf("GetRegistrationToken (repo) failed: %v", err)
	}
	if repoToken != "gitea-repo-runner-token-123" {
		t.Errorf("unexpected repo token: %q", repoToken)
	}

	// 4. Org scope registration token (tests 405 GET -> fallback POST)
	orgURL := server.URL + "/my-org"
	orgToken, err := client.GetRegistrationToken(ctx, provider.ScopeOrg, orgURL)
	if err != nil {
		t.Fatalf("GetRegistrationToken (org) failed: %v", err)
	}
	if orgToken != "gitea-org-runner-token-456" {
		t.Errorf("unexpected org token: %q", orgToken)
	}

	// 5. Global scope registration token
	globalToken, err := client.GetRegistrationToken(ctx, provider.ScopeGlobal, server.URL)
	if err != nil {
		t.Fatalf("GetRegistrationToken (global) failed: %v", err)
	}
	if globalToken != "gitea-global-runner-token-789" {
		t.Errorf("unexpected global token: %q", globalToken)
	}

	// 6. Renovate token
	renovateToken, err := client.GetRenovateToken(ctx, repoURL)
	if err != nil {
		t.Fatalf("GetRenovateToken failed: %v", err)
	}
	if renovateToken != "valid-gitea-pat" {
		t.Errorf("unexpected renovate token: %q", renovateToken)
	}
}

func TestGiteaURLParsing(t *testing.T) {
	server := setupMockGiteaServer(t)
	ctx := context.Background()

	// Client without explicit base URL: extracts base URL from target URL
	client, err := gitea.NewClient("valid-gitea-pat")
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	repoToken, err := client.GetRegistrationToken(ctx, provider.ScopeRepo, server.URL+"/my-org/my-repo")
	if err != nil {
		t.Fatalf("GetRegistrationToken failed with derived base URL: %v", err)
	}
	if repoToken != "gitea-repo-runner-token-123" {
		t.Errorf("unexpected token %q", repoToken)
	}

	// Invalid target URLs
	if _, err := client.GetRegistrationToken(ctx, provider.ScopeRepo, "not-a-url"); err == nil {
		t.Fatal("expected error on invalid url")
	}
	if _, err := client.GetRegistrationToken(ctx, provider.ScopeRepo, server.URL); err == nil {
		t.Fatal("expected error on repo scope without repo")
	}
}

func TestGiteaRegistryIntegration(t *testing.T) {
	ctx := context.Background()

	prov, err := provider.DefaultRegistry.Build(ctx, db.DecryptedAuthProfile{
		AuthProfile: db.AuthProfile{
			AuthMethod: string(provider.AuthMethodGiteaToken),
		},
		Token: "my-gitea-pat-token",
	})
	if err != nil {
		t.Fatalf("failed to build gitea provider from registry: %v", err)
	}
	if prov.ScalingMode() != provider.ScalingWebhook {
		t.Errorf("expected webhook scaling mode")
	}

	// Empty token fails
	_, err = provider.DefaultRegistry.Build(ctx, db.DecryptedAuthProfile{
		AuthProfile: db.AuthProfile{
			AuthMethod: string(provider.AuthMethodGiteaToken),
		},
		Token: "",
	})
	if err == nil || !strings.Contains(err.Error(), "token is required") {
		t.Fatalf("expected missing credentials error, got %v", err)
	}
}

func TestGiteaDiscoveryPagination(t *testing.T) {
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
					"html_url":  fmt.Sprintf("https://gitea.example.com/org/repo-%d", i),
					"private":   false,
				})
			}
			_ = json.NewEncoder(w).Encode(repos)
			return
		}
		if page == "2" {
			var repos []map[string]any
			for i := 101; i <= 130; i++ {
				repos = append(repos, map[string]any{
					"name":      fmt.Sprintf("repo-%d", i),
					"full_name": fmt.Sprintf("org/repo-%d", i),
					"html_url":  fmt.Sprintf("https://gitea.example.com/org/repo-%d", i),
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
			for i := 101; i <= 118; i++ {
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
	client, err := gitea.NewClient("valid-gitea-pat", gitea.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	repos, err := client.DiscoverRepositories(ctx)
	if err != nil {
		t.Fatalf("DiscoverRepositories failed: %v", err)
	}
	if len(repos) != 130 {
		t.Fatalf("expected 130 repos, got %d", len(repos))
	}

	orgs, err := client.DiscoverOrganizations(ctx)
	if err != nil {
		t.Fatalf("DiscoverOrganizations failed: %v", err)
	}
	if len(orgs) != 118 {
		t.Fatalf("expected 118 orgs, got %d", len(orgs))
	}
}
