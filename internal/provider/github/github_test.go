package github_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/provider/github"
)

func setupMockGitHubServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// 1. /app endpoint
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":12345,"name":"test-runner-app","slug":"my-awesome-app"}`))
	})

	// 1b. /app/installations endpoint
	mux.HandleFunc("/app/installations", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{
				"id": 7777,
				"account": {
					"login": "my-org",
					"type": "Organization"
				},
				"html_url": "https://github.com/organizations/my-org/settings/installations/7777",
				"repository_selection": "selected"
			}
		]`))
	})

	// 2. /user endpoint (PAT)
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer valid-pat-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"test-user"}`))
	})

	// 3. /repos/{owner}/{repo}/installation
	mux.HandleFunc("/repos/my-org/my-repo/installation", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":7777}`))
	})

	// 4. /orgs/{owner}/installation
	mux.HandleFunc("/orgs/my-org/installation", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":7777}`))
	})

	// 5. /app/installations/{id}/access_tokens
	mux.HandleFunc("/app/installations/7777/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		resp := map[string]any{
			"token":      "ghs_installation_access_token_xyz",
			"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	// 6. /repos/{owner}/{repo}/actions/runners/registration-token
	mux.HandleFunc("/repos/my-org/my-repo/actions/runners/registration-token", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer ghs_installation_access_token_xyz" && auth != "Bearer valid-pat-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Unauthorized runner token access"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"gh_repo_runner_token_abc123","expires_at":"2030-01-01T00:00:00Z"}`))
	})

	// 7. /orgs/{owner}/actions/runners/registration-token
	mux.HandleFunc("/orgs/my-org/actions/runners/registration-token", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer ghs_installation_access_token_xyz" && auth != "Bearer valid-pat-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Unauthorized runner token access"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"gh_org_runner_token_xyz789","expires_at":"2030-01-01T00:00:00Z"}`))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })
	return server
}

func TestGitHubAppProvider(t *testing.T) {
	pkcs1PEM, _, _ := generateTestKeyPEMs(t)
	server := setupMockGitHubServer(t)
	ctx := context.Background()

	client, err := github.NewAppProvider(12345, pkcs1PEM, github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewAppProvider failed: %v", err)
	}

	// 1. Credentials validation
	if err := client.ValidateCredentials(ctx); err != nil {
		t.Fatalf("ValidateCredentials failed: %v", err)
	}

	// 2. Scaling mode & queued jobs
	if client.ScalingMode() != provider.ScalingWebhook {
		t.Errorf("expected scaling mode webhook, got %v", client.ScalingMode())
	}
	if queued, err := client.PollQueuedJobs(ctx, "https://github.com/my-org/my-repo"); err != nil || queued != 0 {
		t.Errorf("expected 0 queued jobs, got %d, err: %v", queued, err)
	}

	// 3. Repo-scoped registration token
	tok, err := client.GetRegistrationToken(ctx, provider.ScopeRepo, "https://github.com/my-org/my-repo.git")
	if err != nil {
		t.Fatalf("GetRegistrationToken (repo) failed: %v", err)
	}
	if tok != "gh_repo_runner_token_abc123" {
		t.Errorf("unexpected repo token: %q", tok)
	}

	// 4. Org-scoped registration token
	orgTok, err := client.GetRegistrationToken(ctx, provider.ScopeOrg, "https://github.com/my-org")
	if err != nil {
		t.Fatalf("GetRegistrationToken (org) failed: %v", err)
	}
	if orgTok != "gh_org_runner_token_xyz789" {
		t.Errorf("unexpected org token: %q", orgTok)
	}

	// 5. Renovate token retrieval
	renovateTok, err := client.GetRenovateToken(ctx, "https://github.com/my-org/my-repo")
	if err != nil {
		t.Fatalf("GetRenovateToken failed: %v", err)
	}
	if renovateTok != "ghs_installation_access_token_xyz" {
		t.Errorf("unexpected renovate token: %q", renovateTok)
	}
}

func TestGitHubPATProvider(t *testing.T) {
	server := setupMockGitHubServer(t)
	ctx := context.Background()

	client, err := github.NewPATProvider("valid-pat-secret", github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPATProvider failed: %v", err)
	}

	// 1. Credentials validation
	if err := client.ValidateCredentials(ctx); err != nil {
		t.Fatalf("ValidateCredentials failed: %v", err)
	}

	// 2. Repo-scoped registration token
	tok, err := client.GetRegistrationToken(ctx, provider.ScopeRepo, "https://github.com/my-org/my-repo")
	if err != nil {
		t.Fatalf("GetRegistrationToken (repo) failed: %v", err)
	}
	if tok != "gh_repo_runner_token_abc123" {
		t.Errorf("unexpected repo token: %q", tok)
	}

	// 3. Renovate token (returns PAT)
	renovateTok, err := client.GetRenovateToken(ctx, "https://github.com/my-org/my-repo")
	if err != nil {
		t.Fatalf("GetRenovateToken failed: %v", err)
	}
	if renovateTok != "valid-pat-secret" {
		t.Errorf("unexpected renovate token: %q", renovateTok)
	}

	// 4. Invalid PAT
	badClient, err := github.NewPATProvider("invalid-pat", github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPATProvider failed: %v", err)
	}
	if err := badClient.ValidateCredentials(ctx); err == nil {
		t.Fatal("expected error with bad PAT")
	}
}

func TestRegistryIntegration(t *testing.T) {
	pkcs1PEM, _, _ := generateTestKeyPEMs(t)
	ctx := context.Background()

	// Test building AuthMethodGitHubApp via DefaultRegistry
	p1, err := provider.DefaultRegistry.Build(ctx, db.DecryptedAuthProfile{
		AuthProfile: db.AuthProfile{
			AuthMethod: string(provider.AuthMethodGitHubApp),
			AppID:      sql.NullInt64{Int64: 9876, Valid: true},
		},
		PrivateKey: pkcs1PEM,
	})
	if err != nil {
		t.Fatalf("failed to build GitHub App provider from registry: %v", err)
	}
	if p1.ScalingMode() != provider.ScalingWebhook {
		t.Errorf("expected webhook scaling mode")
	}

	// Test building AuthMethodPAT via DefaultRegistry
	p2, err := provider.DefaultRegistry.Build(ctx, db.DecryptedAuthProfile{
		AuthProfile: db.AuthProfile{
			AuthMethod: string(provider.AuthMethodPAT),
		},
		Token: "ghp_some_valid_pat",
	})
	if err != nil {
		t.Fatalf("failed to build GitHub PAT provider from registry: %v", err)
	}
	if p2.ScalingMode() != provider.ScalingWebhook {
		t.Errorf("expected webhook scaling mode")
	}
}

func TestGitHubAppMetadata(t *testing.T) {
	server := setupMockGitHubServer(t)
	pkcs1PEM, _, _ := generateTestKeyPEMs(t)
	ctx := context.Background()

	client, err := github.NewAppProvider(12345, pkcs1PEM, github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewAppProvider failed: %v", err)
	}

	installURL, insts, err := client.GetAppMetadata(ctx)
	if err != nil {
		t.Fatalf("GetAppMetadata failed: %v", err)
	}

	expectedURL := server.URL + "/apps/my-awesome-app/installations/new"
	if installURL != expectedURL {
		t.Errorf("unexpected installURL: got %s, want %s", installURL, expectedURL)
	}
	if len(insts) != 1 {
		t.Fatalf("expected 1 installation, got %d", len(insts))
	}
	if insts[0].AccountLogin != "my-org" || insts[0].AccountType != "Organization" {
		t.Errorf("unexpected installation: %+v", insts[0])
	}
}

func TestGitHubDiscoveryPagination(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":12345,"name":"test-app","slug":"test-app"}`))
	})

	// 1. Installations endpoint (2 pages: 100 on page 1, 25 on page 2 -> 125 total)
	mux.HandleFunc("/app/installations", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		if page == "1" || page == "" {
			var insts []map[string]any
			for i := 1; i <= 100; i++ {
				insts = append(insts, map[string]any{
					"id": int64(1000 + i),
					"account": map[string]any{
						"login": fmt.Sprintf("org-%d", i),
						"type":  "Organization",
					},
					"repository_selection": "all",
				})
			}
			_ = json.NewEncoder(w).Encode(insts)
			return
		}
		if page == "2" {
			var insts []map[string]any
			for i := 101; i <= 125; i++ {
				insts = append(insts, map[string]any{
					"id": int64(1000 + i),
					"account": map[string]any{
						"login": fmt.Sprintf("org-%d", i),
						"type":  "Organization",
					},
					"repository_selection": "all",
				})
			}
			_ = json.NewEncoder(w).Encode(insts)
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})

	mux.HandleFunc("/app/installations/9999/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_single_inst_token",
			"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	})

	// 2. Installation Repositories endpoint (2 pages: 100 on page 1, 35 on page 2 -> 135 total)
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		if page == "1" || page == "" {
			var repos []map[string]any
			for i := 1; i <= 100; i++ {
				repos = append(repos, map[string]any{
					"name":      fmt.Sprintf("repo-%d", i),
					"full_name": fmt.Sprintf("my-org/repo-%d", i),
					"html_url":  fmt.Sprintf("https://github.com/my-org/repo-%d", i),
					"private":   false,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": repos})
			return
		}
		if page == "2" {
			var repos []map[string]any
			for i := 101; i <= 135; i++ {
				repos = append(repos, map[string]any{
					"name":      fmt.Sprintf("repo-%d", i),
					"full_name": fmt.Sprintf("my-org/repo-%d", i),
					"html_url":  fmt.Sprintf("https://github.com/my-org/repo-%d", i),
					"private":   false,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": repos})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"repositories": []any{}})
	})

	// 3. User repos (PAT mode: 2 pages: 100 on page 1, 15 on page 2 -> 115 total)
	mux.HandleFunc("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		if page == "1" || page == "" {
			var repos []map[string]any
			for i := 1; i <= 100; i++ {
				repos = append(repos, map[string]any{
					"name":      fmt.Sprintf("pat-repo-%d", i),
					"full_name": fmt.Sprintf("user/pat-repo-%d", i),
					"html_url":  fmt.Sprintf("https://github.com/user/pat-repo-%d", i),
					"private":   true,
				})
			}
			_ = json.NewEncoder(w).Encode(repos)
			return
		}
		if page == "2" {
			var repos []map[string]any
			for i := 101; i <= 115; i++ {
				repos = append(repos, map[string]any{
					"name":      fmt.Sprintf("pat-repo-%d", i),
					"full_name": fmt.Sprintf("user/pat-repo-%d", i),
					"html_url":  fmt.Sprintf("https://github.com/user/pat-repo-%d", i),
					"private":   true,
				})
			}
			_ = json.NewEncoder(w).Encode(repos)
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})

	// 4. User orgs (PAT mode: 2 pages: 100 on page 1, 12 on page 2 -> 112 total)
	mux.HandleFunc("/user/orgs", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		if page == "1" || page == "" {
			var orgs []map[string]any
			for i := 1; i <= 100; i++ {
				orgs = append(orgs, map[string]any{
					"login": fmt.Sprintf("pat-org-%d", i),
				})
			}
			_ = json.NewEncoder(w).Encode(orgs)
			return
		}
		if page == "2" {
			var orgs []map[string]any
			for i := 101; i <= 112; i++ {
				orgs = append(orgs, map[string]any{
					"login": fmt.Sprintf("pat-org-%d", i),
				})
			}
			_ = json.NewEncoder(w).Encode(orgs)
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	pkcs1PEM, _, _ := generateTestKeyPEMs(t)
	ctx := context.Background()

	// Test GitHub App pagination for DiscoverOrganizations & GetAppMetadata
	appClient, err := github.NewAppProvider(12345, pkcs1PEM, github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewAppProvider failed: %v", err)
	}

	orgs, err := appClient.DiscoverOrganizations(ctx)
	if err != nil {
		t.Fatalf("DiscoverOrganizations (App) failed: %v", err)
	}
	if len(orgs) != 125 {
		t.Fatalf("expected 125 organizations, got %d", len(orgs))
	}

	_, metaInsts, err := appClient.GetAppMetadata(ctx)
	if err != nil {
		t.Fatalf("GetAppMetadata failed: %v", err)
	}
	if len(metaInsts) != 125 {
		t.Fatalf("expected 125 installations in metadata, got %d", len(metaInsts))
	}

	// Test GitHub App pagination for DiscoverRepositories with a single installation
	singleInstMux := http.NewServeMux()
	singleInstMux.HandleFunc("/app/installations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id": int64(9999),
				"account": map[string]any{
					"login": "my-org",
					"type":  "Organization",
				},
				"repository_selection": "all",
			},
		})
	})
	singleInstMux.HandleFunc("/app/installations/9999/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "ghs_single_inst_token",
		})
	})
	singleInstMux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		if page == "1" || page == "" {
			var repos []map[string]any
			for i := 1; i <= 100; i++ {
				repos = append(repos, map[string]any{
					"name":      fmt.Sprintf("repo-%d", i),
					"full_name": fmt.Sprintf("my-org/repo-%d", i),
					"html_url":  fmt.Sprintf("https://github.com/my-org/repo-%d", i),
					"private":   false,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": repos})
			return
		}
		if page == "2" {
			var repos []map[string]any
			for i := 101; i <= 135; i++ {
				repos = append(repos, map[string]any{
					"name":      fmt.Sprintf("repo-%d", i),
					"full_name": fmt.Sprintf("my-org/repo-%d", i),
					"html_url":  fmt.Sprintf("https://github.com/my-org/repo-%d", i),
					"private":   false,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": repos})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"repositories": []any{}})
	})
	singleInstServer := httptest.NewServer(singleInstMux)
	defer singleInstServer.Close()

	appSingleClient, err := github.NewAppProvider(12345, pkcs1PEM, github.WithBaseURL(singleInstServer.URL))
	if err != nil {
		t.Fatalf("NewAppProvider single failed: %v", err)
	}

	repos, err := appSingleClient.DiscoverRepositories(ctx)
	if err != nil {
		t.Fatalf("DiscoverRepositories (App) failed: %v", err)
	}
	if len(repos) != 135 {
		t.Fatalf("expected 135 repositories, got %d", len(repos))
	}

	// Test PAT mode pagination
	patClient, err := github.NewPATProvider("valid-pat", github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPATProvider failed: %v", err)
	}

	patRepos, err := patClient.DiscoverRepositories(ctx)
	if err != nil {
		t.Fatalf("DiscoverRepositories (PAT) failed: %v", err)
	}
	if len(patRepos) != 115 {
		t.Fatalf("expected 115 pat repos, got %d", len(patRepos))
	}

	patOrgs, err := patClient.DiscoverOrganizations(ctx)
	if err != nil {
		t.Fatalf("DiscoverOrganizations (PAT) failed: %v", err)
	}
	if len(patOrgs) != 112 {
		t.Fatalf("expected 112 pat orgs, got %d", len(patOrgs))
	}
}
