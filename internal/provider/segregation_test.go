package provider_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/provider/forgejo"
	"github.com/noosxe/runnero/internal/provider/gitea"
	"github.com/noosxe/runnero/internal/provider/github"
)

func TestTokenSegregation_AllProvidersClean(t *testing.T) {
	scanner := provider.NewSegregationScanner()

	// 1. GitHub App Provider
	t.Run("GitHub_App", func(t *testing.T) {
		masterAppID := "987654"
		masterPrivateKey := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0mockkey...\n-----END RSA PRIVATE KEY-----"
		ephemeralRunnerToken := "gh_repo_runner_token_ephemeral_123"
		shortLivedRenovateToken := "ghs_installation_access_token_456"

		runnerSpec := provider.ContainerSpec{
			Env: provider.BuildRunnerEnv("github", ephemeralRunnerToken, "https://github.com/my-org/my-repo", "runner-1", []string{"self-hosted", "linux"}, "_work"),
			Labels: map[string]string{
				"com.runnero.managed":   "true",
				"com.runnero.pool-name": "gh-pool",
			},
			Mounts: []string{"/var/run/docker.sock:/var/run/docker.sock"},
			Image:  "ghcr.io/noosxe/runnero:latest",
		}

		if err := scanner.Scan(runnerSpec, masterPrivateKey, masterAppID); err != nil {
			t.Fatalf("clean GitHub runner spec failed scan: %v", err)
		}

		renovateSpec := provider.ContainerSpec{
			Env:   provider.BuildRenovateEnv("github", shortLivedRenovateToken, "https://github.com/my-org/my-repo"),
			Image: "renovate/renovate:latest",
		}
		if err := scanner.Scan(renovateSpec, masterPrivateKey, masterAppID); err != nil {
			t.Fatalf("clean GitHub Renovate spec failed scan: %v", err)
		}
	})

	// 2. GitHub PAT Provider
	t.Run("GitHub_PAT", func(t *testing.T) {
		masterPAT := "ghp_master_supervisor_pat_secret_9999"
		ephemeralRunnerToken := "gh_repo_runner_token_ephemeral_789"

		runnerSpec := provider.ContainerSpec{
			Env: provider.BuildRunnerEnv("github", ephemeralRunnerToken, "https://github.com/my-org/my-repo", "runner-2", []string{"self-hosted"}, "_work"),
			Labels: map[string]string{
				"com.runnero.managed": "true",
			},
			Image: "ghcr.io/noosxe/runnero:latest",
		}

		if err := scanner.Scan(runnerSpec, masterPAT); err != nil {
			t.Fatalf("clean GitHub PAT runner spec failed scan: %v", err)
		}
	})

	// 3. Gitea Provider
	t.Run("Gitea", func(t *testing.T) {
		masterGiteaPAT := "gitea_master_pat_admin_abcdef12345"
		ephemeralRunnerToken := "gitea_ephemeral_runner_token_333"

		runnerSpec := provider.ContainerSpec{
			Env: provider.BuildRunnerEnv("gitea", ephemeralRunnerToken, "https://gitea.example.com/my-org/repo", "runner-gitea", []string{"gitea"}, "_work"),
			Labels: map[string]string{
				"com.runnero.managed": "true",
			},
			Mounts: []string{"/var/run/docker.sock:/var/run/docker.sock"},
			Image:  "ghcr.io/noosxe/runnero:latest",
		}

		if err := scanner.Scan(runnerSpec, masterGiteaPAT); err != nil {
			t.Fatalf("clean Gitea runner spec failed scan: %v", err)
		}
	})

	// 4. Forgejo Provider
	t.Run("Forgejo", func(t *testing.T) {
		masterForgejoPAT := "forgejo_master_pat_admin_xyz98765"
		ephemeralRunnerToken := "forgejo_ephemeral_runner_token_444"

		runnerSpec := provider.ContainerSpec{
			Env: provider.BuildRunnerEnv("forgejo", ephemeralRunnerToken, "https://forgejo.example.com/my-org/repo", "runner-forgejo", []string{"forgejo"}, "_work"),
			Labels: map[string]string{
				"com.runnero.managed": "true",
			},
			Mounts: []string{"/var/run/docker.sock:/var/run/docker.sock"},
			Image:  "ghcr.io/noosxe/runnero:latest",
		}

		if err := scanner.Scan(runnerSpec, masterForgejoPAT); err != nil {
			t.Fatalf("clean Forgejo runner spec failed scan: %v", err)
		}
	})
}

func TestTokenSegregation_LeakageDetection(t *testing.T) {
	scanner := provider.NewSegregationScanner()
	masterSecret := "super_secret_master_credential_12345"

	// 1. Master credential leaked in Env Value
	t.Run("Env_Value_Leak", func(t *testing.T) {
		badSpec := provider.ContainerSpec{
			Env: []string{
				"RUNNER_PROVIDER=github",
				"RUNNER_TOKEN=" + masterSecret,
			},
		}
		err := scanner.Scan(badSpec, masterSecret)
		if !errors.Is(err, provider.ErrCredentialLeakage) {
			t.Fatalf("expected ErrCredentialLeakage, got %v", err)
		}
	})

	// 2. Forbidden Env Key
	t.Run("Forbidden_Env_Key", func(t *testing.T) {
		forbiddenKeys := []string{
			"DB_ENCRYPTION_KEY=secret",
			"SUPERVISOR_DB_ENCRYPTION_KEY=secret",
			"JWT_SECRET=secret",
			"SUPERVISOR_PAT=secret",
			"GITHUB_APP_PRIVATE_KEY=secret",
		}
		for _, env := range forbiddenKeys {
			badSpec := provider.ContainerSpec{
				Env: []string{env},
			}
			err := scanner.Scan(badSpec)
			if !errors.Is(err, provider.ErrForbiddenEnvKey) {
				t.Errorf("expected ErrForbiddenEnvKey for %s, got %v", env, err)
			}
		}
	})

	// 3. Master credential leaked in Labels
	t.Run("Label_Leak", func(t *testing.T) {
		badSpec := provider.ContainerSpec{
			Labels: map[string]string{
				"supervisor.audit": fmt.Sprintf("auth:%s", masterSecret),
			},
		}
		err := scanner.Scan(badSpec, masterSecret)
		if !errors.Is(err, provider.ErrCredentialLeakage) {
			t.Fatalf("expected ErrCredentialLeakage in labels, got %v", err)
		}
	})

	// 4. Dangerous Mount
	t.Run("Dangerous_Mount", func(t *testing.T) {
		badMounts := []string{
			"/data:/data",
			"/data/supervisor.db:/app/db.sqlite",
			"/etc/supervisor/keys:/keys",
		}
		for _, m := range badMounts {
			badSpec := provider.ContainerSpec{
				Mounts: []string{m},
			}
			err := scanner.Scan(badSpec)
			if !errors.Is(err, provider.ErrDangerousMount) {
				t.Errorf("expected ErrDangerousMount for %s, got %v", m, err)
			}
		}
	})

	// 5. Master credential in Command
	t.Run("Command_Arg_Leak", func(t *testing.T) {
		badSpec := provider.ContainerSpec{
			Cmd: []string{"entrypoint.sh", "--token", masterSecret},
		}
		err := scanner.Scan(badSpec, masterSecret)
		if !errors.Is(err, provider.ErrCredentialLeakage) {
			t.Fatalf("expected ErrCredentialLeakage in Cmd, got %v", err)
		}
	})
}

func TestProviderTokenIsolationHarness(t *testing.T) {
	ctx := context.Background()
	scanner := provider.NewSegregationScanner()

	// Ensure MockProvider never returns master secret as registration token
	masterSecret := "master_pat_admin_never_leak_this_token"
	mock := &provider.MockProvider{
		RegistrationTokenFn: func(ctx context.Context, scope provider.RegistrationScope, targetURL string) (string, error) {
			return "fresh-ephemeral-runner-token-12345", nil
		},
	}

	tok, err := mock.GetRegistrationToken(ctx, provider.ScopeRepo, "https://example.com/org/repo")
	if err != nil {
		t.Fatalf("token retrieval failed: %v", err)
	}

	spec := provider.ContainerSpec{
		Env: provider.BuildRunnerEnv("gitea", tok, "https://example.com/org/repo", "runner-1", []string{"test"}, "_work"),
	}

	if err := scanner.Scan(spec, masterSecret); err != nil {
		t.Fatalf("segregation scan failed: %v", err)
	}
}

// Suppress unused imports
var _ = github.DefaultBaseURL
var _ = gitea.ErrEmptyTokenReceived
var _ = forgejo.ErrEmptyTokenReceived
