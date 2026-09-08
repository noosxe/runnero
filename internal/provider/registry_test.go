package provider_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/provider"
)

var testKey = []byte("01234567890123456789012345678901")

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(db.Options{
		Path:          dbPath,
		EncryptionKey: testKey,
	})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestMockProvider(t *testing.T) {
	ctx := context.Background()

	// 1. Default mock provider behaviors
	defaultMock := &provider.MockProvider{}
	tok, err := defaultMock.GetRegistrationToken(ctx, provider.ScopeRepo, "https://github.com/org/repo")
	if err != nil || tok != "mock-registration-token" {
		t.Fatalf("expected default token, got %q, err: %v", tok, err)
	}
	if err := defaultMock.ValidateCredentials(ctx); err != nil {
		t.Fatalf("expected nil credential validation, got %v", err)
	}
	if defaultMock.ScalingMode() != provider.ScalingWebhook {
		t.Fatalf("expected default scaling mode webhook, got %v", defaultMock.ScalingMode())
	}
	jobs, err := defaultMock.PollQueuedJobs(ctx, provider.PollTarget{URL: "https://github.com/org/repo"})
	if err != nil || jobs != 0 {
		t.Fatalf("expected 0 queued jobs, got %d, err: %v", jobs, err)
	}

	// 2. Custom function hooks
	customMock := &provider.MockProvider{
		RegistrationTokenFn: func(ctx context.Context, scope provider.RegistrationScope, targetURL string) (string, error) {
			if scope == provider.ScopeOrg {
				return "org-token", nil
			}
			return "", errors.New("unsupported scope")
		},
		ValidateCredentialsFn: func(ctx context.Context) error {
			return errors.New("auth failed")
		},
		ScalingModeFn: func() provider.ScalingMode {
			return provider.ScalingPolling
		},
		PollQueuedJobsFn: func(ctx context.Context, target provider.PollTarget) (int, error) {
			return 42, nil
		},
	}

	if tok, err := customMock.GetRegistrationToken(ctx, provider.ScopeOrg, "https://github.com/org"); err != nil || tok != "org-token" {
		t.Errorf("custom token failed: %v, tok: %q", err, tok)
	}
	if _, err := customMock.GetRegistrationToken(ctx, provider.ScopeRepo, "https://github.com/org/repo"); err == nil {
		t.Errorf("expected error on repo scope")
	}
	if err := customMock.ValidateCredentials(ctx); err == nil || err.Error() != "auth failed" {
		t.Errorf("expected auth failed error, got %v", err)
	}
	if customMock.ScalingMode() != provider.ScalingPolling {
		t.Errorf("expected polling scaling mode, got %v", customMock.ScalingMode())
	}
	if jobs, err := customMock.PollQueuedJobs(ctx, provider.PollTarget{URL: "url"}); err != nil || jobs != 42 {
		t.Errorf("expected 42 jobs, got %d, err: %v", jobs, err)
	}
}

func TestRegistry(t *testing.T) {
	ctx := context.Background()
	reg := provider.NewRegistry()

	// Register constructors for all methods
	reg.Register(provider.AuthMethodGitHubApp, func(ctx context.Context, profile db.DecryptedAuthProfile) (provider.GitProvider, error) {
		return &provider.MockProvider{
			RegistrationTokenFn: func(ctx context.Context, scope provider.RegistrationScope, targetURL string) (string, error) {
				return "gh-app-token-" + profile.PrivateKey, nil
			},
		}, nil
	})

	reg.Register(provider.AuthMethodGiteaToken, func(ctx context.Context, profile db.DecryptedAuthProfile) (provider.GitProvider, error) {
		return &provider.MockProvider{
			RegistrationTokenFn: func(ctx context.Context, scope provider.RegistrationScope, targetURL string) (string, error) {
				return "gitea-token-" + profile.Token, nil
			},
		}, nil
	})

	reg.Register(provider.AuthMethodForgejoToken, func(ctx context.Context, profile db.DecryptedAuthProfile) (provider.GitProvider, error) {
		return &provider.MockProvider{
			ScalingModeFn: func() provider.ScalingMode {
				return provider.ScalingPolling
			},
		}, nil
	})

	reg.Register(provider.AuthMethodPAT, func(ctx context.Context, profile db.DecryptedAuthProfile) (provider.GitProvider, error) {
		return &provider.MockProvider{
			RegistrationTokenFn: func(ctx context.Context, scope provider.RegistrationScope, targetURL string) (string, error) {
				return "pat-" + profile.Token, nil
			},
		}, nil
	})

	// Test Build with valid profiles
	p1, err := reg.Build(ctx, db.DecryptedAuthProfile{
		AuthProfile: db.AuthProfile{AuthMethod: string(provider.AuthMethodGitHubApp)},
		PrivateKey:  "secret-key",
	})
	if err != nil {
		t.Fatalf("failed to build github_app provider: %v", err)
	}
	tok, _ := p1.GetRegistrationToken(ctx, provider.ScopeRepo, "url")
	if tok != "gh-app-token-secret-key" {
		t.Errorf("unexpected token %q", tok)
	}

	p2, err := reg.Build(ctx, db.DecryptedAuthProfile{
		AuthProfile: db.AuthProfile{AuthMethod: string(provider.AuthMethodForgejoToken)},
	})
	if err != nil {
		t.Fatalf("failed to build forgejo provider: %v", err)
	}
	if p2.ScalingMode() != provider.ScalingPolling {
		t.Errorf("expected polling for forgejo, got %v", p2.ScalingMode())
	}

	// Test Build with unsupported auth method
	_, err = reg.Build(ctx, db.DecryptedAuthProfile{
		AuthProfile: db.AuthProfile{AuthMethod: "oauth2_unknown"},
	})
	if !errors.Is(err, provider.ErrUnsupportedAuthMethod) {
		t.Fatalf("expected ErrUnsupportedAuthMethod, got %v", err)
	}
}

func TestRegistry_BuildFromDB(t *testing.T) {
	ctx := context.Background()
	database := openTestDB(t)

	// Create an encrypted auth profile in the database
	created, err := database.CreateEncryptedAuthProfile(
		ctx,
		"Test Gitea Profile",
		string(provider.AuthMethodGiteaToken),
		sql.NullInt64{},
		"",
		"secret_gitea_pat_12345",
	)
	if err != nil {
		t.Fatalf("failed to create encrypted profile: %v", err)
	}

	reg := provider.NewRegistry()
	reg.Register(provider.AuthMethodGiteaToken, func(ctx context.Context, profile db.DecryptedAuthProfile) (provider.GitProvider, error) {
		if profile.Token != "secret_gitea_pat_12345" {
			return nil, errors.New("decrypted token mismatch")
		}
		return &provider.MockProvider{
			RegistrationTokenFn: func(ctx context.Context, scope provider.RegistrationScope, targetURL string) (string, error) {
				return "reg-tok-" + profile.Token, nil
			},
		}, nil
	})

	// Build from DB
	prov, err := reg.BuildFromDB(ctx, database, created.ID)
	if err != nil {
		t.Fatalf("BuildFromDB failed: %v", err)
	}

	tok, err := prov.GetRegistrationToken(ctx, provider.ScopeRepo, "https://gitea.com/repo")
	if err != nil || tok != "reg-tok-secret_gitea_pat_12345" {
		t.Fatalf("unexpected token %q, err: %v", tok, err)
	}

	// Non-existent profile ID returns error
	if _, err := reg.BuildFromDB(ctx, database, 999999); err == nil {
		t.Fatal("expected error for non-existent profile ID")
	}
}
