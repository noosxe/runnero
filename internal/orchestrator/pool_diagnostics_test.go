package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/server"
)

func TestSanitizeDiagnosticError(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty error",
			input:    "",
			expected: "",
		},
		{
			name:     "clean error",
			input:    "failed to connect to host",
			expected: "failed to connect to host",
		},
		{
			name:     "redacts GitHub PAT",
			input:    "request failed with token ghp_123456789012345678901234567890123456",
			expected: "request failed with token [REDACTED_TOKEN]",
		},
		{
			name:     "redacts Bearer token",
			input:    "authorization header: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.token",
			expected: "authorization header: [REDACTED_TOKEN]",
		},
		{
			name:     "redacts private key PEM block",
			input:    "error loading key: -----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0\n-----END RSA PRIVATE KEY-----",
			expected: "error loading key: [REDACTED_PRIVATE_KEY]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := orchestrator.SanitizeDiagnosticError(tt.input)
			if got != tt.expected {
				t.Errorf("SanitizeDiagnosticError(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestClassifyReconcileError(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		expectedCode string
	}{
		{
			name:         "decryption failure",
			err:          errors.New("decryption failed: wrong key or tampered ciphertext"),
			expectedCode: orchestrator.ErrCodeAuthDecryptionFailed,
		},
		{
			name:         "provider 401 unauthorized",
			err:          errors.New("github API returned 401 Bad credentials"),
			expectedCode: orchestrator.ErrCodeProviderAuthFailed,
		},
		{
			name:         "target 404 not found",
			err:          errors.New("repository not found (404)"),
			expectedCode: orchestrator.ErrCodeTargetNotFound,
		},
		{
			name:         "registration token rate limit",
			err:          errors.New("getting registration token failed: rate limit exceeded"),
			expectedCode: orchestrator.ErrCodeRegistrationToken,
		},
		{
			name:         "image pull failure",
			err:          errors.New("image pull failed: manifest unknown for tag v1"),
			expectedCode: orchestrator.ErrCodeImagePullFailed,
		},
		{
			name:         "docker socket error",
			err:          errors.New("docker daemon socket connection refused"),
			expectedCode: orchestrator.ErrCodeDockerEngineError,
		},
		{
			name:         "global quota saturated",
			err:          errors.New("global runner quota saturated"),
			expectedCode: orchestrator.ErrCodeGlobalQuotaSaturated,
		},
		{
			name:         "generic reconcile error",
			err:          errors.New("unknown error during reconcile"),
			expectedCode: orchestrator.ErrCodeReconcileFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _ := orchestrator.ClassifyReconcileError(tt.err)
			if code != tt.expectedCode {
				t.Errorf("ClassifyReconcileError(%v) code = %q, want %q", tt.err, code, tt.expectedCode)
			}
		})
	}
}

func TestPoolControllerDiagnosticsLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockEngine := orchestrator.NewMockContainerProvider()
	mockRepo := &mockPoolRepo{
		pools: []db.RunnerPool{
			{
				ID:             150,
				Name:           "pool-test-diag",
				Provider:       "github",
				RepositoryUrl:  "https://github.com/org/repo",
				MinIdleRunners: 1,
				MaxConcurrency: 5,
				AuthProfileID:  10,
			},
		},
	}

	// 1. Initial State before reconcile with Decryption failure
	gitResolver := &mockGitProviderResolver{
		err: errors.New("decryption failed: wrong key or tampered ciphertext"),
	}

	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               mockRepo,
		ContainerEngine:  mockEngine,
		ProviderResolver: gitResolver,
	})

	// Initial default diagnostics
	initDiag := ctrl.PoolDiagnostics(150)
	if initDiag.HealthStatus != "healthy" && initDiag.HealthStatus != "provisioning" {
		t.Errorf("expected initial health status, got %s", initDiag.HealthStatus)
	}

	// 2. Boot with Decryption failure -> DEGRADED + AUTH_DECRYPTION_FAILED
	_ = ctrl.Boot(ctx)

	diag := ctrl.PoolDiagnostics(150)
	if diag.HealthStatus != "degraded" {
		t.Errorf("expected health degraded, got %s", diag.HealthStatus)
	}
	if diag.LastErrorCode != orchestrator.ErrCodeAuthDecryptionFailed {
		t.Errorf("expected error code %s, got %s", orchestrator.ErrCodeAuthDecryptionFailed, diag.LastErrorCode)
	}
	if !strings.Contains(diag.LastError, "decryption failed") {
		t.Errorf("expected error message to contain 'decryption failed', got %s", diag.LastError)
	}

	// 3. Reconcile with Engine failure -> DEGRADED + DOCKER_ENGINE_ERROR
	gitResolver.err = nil
	gitResolver.providers = map[int64]provider.GitProvider{
		10: &mockGitProvider{},
	}
	mockEngine.SpawnRunnerFn = func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
		return "", errors.New("docker daemon socket connection refused")
	}

	_ = ctrl.Reconcile(ctx)
	diag = ctrl.PoolDiagnostics(150)
	if diag.HealthStatus != "degraded" {
		t.Errorf("expected health degraded on engine fail, got %s", diag.HealthStatus)
	}
	if diag.LastErrorCode != orchestrator.ErrCodeDockerEngineError {
		t.Errorf("expected error code %s, got %s", orchestrator.ErrCodeDockerEngineError, diag.LastErrorCode)
	}

	// 4. Successful Reconcile -> HEALTHY
	mockEngine.SpawnRunnerFn = nil
	_ = ctrl.Reconcile(ctx)
	diag = ctrl.PoolDiagnostics(150)
	if diag.HealthStatus != "healthy" {
		t.Errorf("expected health healthy on success, got %s", diag.HealthStatus)
	}
	if diag.LastError != "" || diag.LastErrorCode != "" {
		t.Errorf("expected cleared error, got code=%s err=%s", diag.LastErrorCode, diag.LastError)
	}
	if !strings.Contains(diag.CurrentIntent, "satisfied") {
		t.Errorf("expected intent to mention 'satisfied', got %q", diag.CurrentIntent)
	}

	// Verify server interface compatibility
	var _ server.PoolStatsProvider = ctrl
}
