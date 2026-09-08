package orchestrator

import (
	"regexp"
	"strings"
	"time"
)

// PoolHealthStatus represents the operational health of a runner pool.
type PoolHealthStatus string

const (
	HealthHealthy      PoolHealthStatus = "healthy"
	HealthProvisioning PoolHealthStatus = "provisioning"
	HealthDegraded     PoolHealthStatus = "degraded"
	HealthPaused       PoolHealthStatus = "paused"
)

// Standard diagnostic error codes for structured reporting (docs/16 §3).
const (
	ErrCodeAuthDecryptionFailed = "AUTH_DECRYPTION_FAILED"
	ErrCodeProviderAuthFailed   = "PROVIDER_AUTH_FAILED"
	ErrCodeTargetNotFound       = "TARGET_NOT_FOUND"
	ErrCodeRegistrationToken    = "REGISTRATION_TOKEN_FAILED"
	ErrCodeDockerEngineError    = "DOCKER_ENGINE_ERROR"
	ErrCodeImagePullFailed      = "IMAGE_PULL_FAILED"
	ErrCodeGlobalQuotaSaturated = "GLOBAL_QUOTA_SATURATED"
	ErrCodeReconcileFailed      = "RECONCILE_FAILED"
)

// PoolDiagnosticState captures the live operational health, intent, and error diagnostics of a runner pool.
type PoolDiagnosticState struct {
	PoolID              int64            `json:"pool_id"`
	PoolName            string           `json:"pool_name"`
	HealthStatus        PoolHealthStatus `json:"health_status"`
	CurrentIntent       string           `json:"current_intent"`
	LastError           string           `json:"last_error,omitempty"`
	LastErrorCode       string           `json:"last_error_code,omitempty"`
	LastErrorTimestamp  time.Time        `json:"last_error_timestamp,omitempty"`
	LastReconciledAt    time.Time        `json:"last_reconciled_at"`
	LastPollAt          time.Time        `json:"last_poll_at,omitempty"`
	LastPollQueuedCount int              `json:"last_poll_queued_count,omitempty"`
	LastPollError       string           `json:"last_poll_error,omitempty"`
}

var (
	tokenRegex  = regexp.MustCompile(`(?i)(gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_\-]{20,}|bearer\s+[A-Za-z0-9\-._~+/]+=*)`)
	pemRegex    = regexp.MustCompile(`(?s)-----BEGIN[^-]+-----.*?-----END[^-]+-----`)
	secretRegex = regexp.MustCompile(`(?i)\b(password|secret|api_key)[:=]\s*["']?[^"'\s]+["']?`)
)

// SanitizeDiagnosticError strips sensitive credentials, private keys, and tokens from error messages (docs/16 §5).
func SanitizeDiagnosticError(raw string) string {
	if raw == "" {
		return ""
	}
	s := pemRegex.ReplaceAllString(raw, "[REDACTED_PRIVATE_KEY]")
	s = tokenRegex.ReplaceAllString(s, "[REDACTED_TOKEN]")
	s = secretRegex.ReplaceAllString(s, "$1=[REDACTED]")

	// Clean up redundant error wrappers for cleaner display
	s = strings.TrimSpace(s)
	return s
}

// ClassifyReconcileError categorizes a reconciliation error into a structured error code and clean message.
func ClassifyReconcileError(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	raw := err.Error()
	lower := strings.ToLower(raw)

	code := ErrCodeReconcileFailed
	switch {
	case strings.Contains(lower, "decryption") || strings.Contains(lower, "cipher") || strings.Contains(lower, "wrong key"):
		code = ErrCodeAuthDecryptionFailed
	case strings.Contains(lower, "401") || strings.Contains(lower, "bad credentials") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "invalid token"):
		code = ErrCodeProviderAuthFailed
	case strings.Contains(lower, "404") || strings.Contains(lower, "not found") || strings.Contains(lower, "could not resolve host"):
		code = ErrCodeTargetNotFound
	case strings.Contains(lower, "registration token") || strings.Contains(lower, "rate limit"):
		code = ErrCodeRegistrationToken
	case strings.Contains(lower, "image") && (strings.Contains(lower, "pull") || strings.Contains(lower, "manifest") || strings.Contains(lower, "not found")):
		code = ErrCodeImagePullFailed
	case strings.Contains(lower, "docker") || strings.Contains(lower, "daemon") || strings.Contains(lower, "socket") || strings.Contains(lower, "container"):
		code = ErrCodeDockerEngineError
	case strings.Contains(lower, "quota") || strings.Contains(lower, "saturated"):
		code = ErrCodeGlobalQuotaSaturated
	}

	clean := SanitizeDiagnosticError(raw)
	return code, clean
}
