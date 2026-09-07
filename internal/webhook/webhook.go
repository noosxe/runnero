package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

var (
	// ErrUnknownProvider indicates an unsupported Git provider in the webhook path.
	ErrUnknownProvider = errors.New("unknown or unsupported webhook provider")
	// ErrMissingSignature indicates the required signature header is missing.
	ErrMissingSignature = errors.New("missing signature header")
	// ErrInvalidSignature indicates the webhook HMAC signature did not match.
	ErrInvalidSignature = errors.New("invalid webhook signature")
	// ErrMissingSecret indicates no webhook secret is configured for the provider.
	ErrMissingSecret = errors.New("no webhook secret configured")
)

// SecretResolver retrieves the shared secret used for verifying webhook HMAC signatures.
type SecretResolver interface {
	ResolveSecret(ctx context.Context, provider string) (string, error)
}

// SecretResolverFunc is a functional adapter for SecretResolver.
type SecretResolverFunc func(ctx context.Context, provider string) (string, error)

// ResolveSecret calls the underlying function.
func (f SecretResolverFunc) ResolveSecret(ctx context.Context, provider string) (string, error) {
	return f(ctx, provider)
}

// StaticSecretResolver returns a SecretResolver from a static map of provider -> secret.
func StaticSecretResolver(secrets map[string]string) SecretResolver {
	return SecretResolverFunc(func(_ context.Context, provider string) (string, error) {
		if s, ok := secrets[strings.ToLower(provider)]; ok && s != "" {
			return s, nil
		}
		return "", fmt.Errorf("%w for provider %q", ErrMissingSecret, provider)
	})
}

// EventHandler processes verified webhook events.
type EventHandler interface {
	HandleWorkflowJob(ctx context.Context, provider string, event *WorkflowJobEvent) error
}

// EventHandlerFunc is a functional adapter for EventHandler.
type EventHandlerFunc func(ctx context.Context, provider string, event *WorkflowJobEvent) error

// HandleWorkflowJob calls the underlying function.
func (f EventHandlerFunc) HandleWorkflowJob(ctx context.Context, provider string, event *WorkflowJobEvent) error {
	return f(ctx, provider, event)
}

// WorkflowJobEvent represents a GitHub/Gitea workflow_job webhook payload.
type WorkflowJobEvent struct {
	Action      string             `json:"action"` // "queued", "in_progress", "completed"
	WorkflowJob WorkflowJobPayload `json:"workflow_job"`
	Repository  RepositoryPayload  `json:"repository"`
	Sender      SenderPayload      `json:"sender,omitempty"`
}

// WorkflowJobPayload represents the workflow_job object within the webhook payload.
type WorkflowJobPayload struct {
	ID           int64  `json:"id"`
	RunID        int64  `json:"run_id"`
	WorkflowName string `json:"workflow_name"`
	HeadBranch   string `json:"head_branch"`
	HeadSHA      string `json:"head_sha"`
	Status       string `json:"status"` // "queued", "in_progress", "completed"
	Conclusion   string `json:"conclusion,omitempty"`
	// Forge-provided RFC3339 timestamps used for job-history enrichment
	// (docs/21 §5.5). Parsed leniently by the orchestrator; empty or invalid
	// values leave the corresponding columns untouched.
	CreatedAt     string   `json:"created_at,omitempty"`
	StartedAt     string   `json:"started_at,omitempty"`
	CompletedAt   string   `json:"completed_at,omitempty"`
	Labels        []string `json:"labels"`
	RunnerID      int64    `json:"runner_id,omitempty"`
	RunnerName    string   `json:"runner_name,omitempty"`
	RunnerGroupID int64    `json:"runner_group_id,omitempty"`
}

// RepositoryPayload represents the repository object within the webhook payload.
type RepositoryPayload struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"full_name"` // e.g. "owner/repo"
	HTMLURL  string `json:"html_url"`  // e.g. "https://github.com/owner/repo"
	CloneURL string `json:"clone_url"`
}

// SenderPayload represents the sender object within the webhook payload.
type SenderPayload struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

// Option configures Receiver.
type Option func(*Receiver)

// WithEventHandler sets the event handler.
func WithEventHandler(handler EventHandler) Option {
	return func(r *Receiver) {
		r.eventHandler = handler
	}
}

// WithLogger sets the logger for Receiver.
func WithLogger(logger *slog.Logger) Option {
	return func(r *Receiver) {
		r.logger = logger
	}
}

// Receiver handles POST /hooks/{provider} webhooks with signature verification,
// event validation, rejection logging, and 2xx fast-ack.
type Receiver struct {
	secretResolver SecretResolver
	eventHandler   EventHandler
	logger         *slog.Logger
}

// NewReceiver creates a new webhook Receiver.
func NewReceiver(secretResolver SecretResolver, opts ...Option) *Receiver {
	r := &Receiver{
		secretResolver: secretResolver,
		logger:         slog.Default(),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// SignPayload computes the HMAC-SHA256 hex signature for payload and secret.
func SignPayload(payload []byte, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks the HMAC-SHA256 signature against the payload using constant-time comparison.
func VerifySignature(secret []byte, payload []byte, signatureHeader string) bool {
	if len(secret) == 0 || signatureHeader == "" {
		return false
	}

	sigHex := strings.TrimSpace(signatureHeader)
	if strings.HasPrefix(strings.ToLower(sigHex), "sha256=") {
		sigHex = sigHex[7:]
	}

	expectedMAC := hmac.New(sha256.New, secret)
	expectedMAC.Write(payload)
	expectedHex := hex.EncodeToString(expectedMAC.Sum(nil))

	return subtle.ConstantTimeCompare([]byte(sigHex), []byte(expectedHex)) == 1
}

// ExtractSignatureHeader finds the HMAC signature header based on provider conventions.
func ExtractSignatureHeader(req *http.Request, provider string) string {
	switch strings.ToLower(provider) {
	case "github":
		return req.Header.Get("X-Hub-Signature-256")
	case "gitea":
		if sig := req.Header.Get("X-Gitea-Signature"); sig != "" {
			return sig
		}
		return req.Header.Get("X-Hub-Signature-256")
	case "forgejo":
		if sig := req.Header.Get("X-Forgejo-Signature"); sig != "" {
			return sig
		}
		if sig := req.Header.Get("X-Gitea-Signature"); sig != "" {
			return sig
		}
		return req.Header.Get("X-Hub-Signature-256")
	default:
		// Fallback checking common headers
		if sig := req.Header.Get("X-Hub-Signature-256"); sig != "" {
			return sig
		}
		return req.Header.Get("X-Gitea-Signature")
	}
}

// ExtractEventHeader extracts the event type header based on provider conventions.
func ExtractEventHeader(req *http.Request, provider string) string {
	switch strings.ToLower(provider) {
	case "github":
		return req.Header.Get("X-GitHub-Event")
	case "gitea":
		if ev := req.Header.Get("X-Gitea-Event"); ev != "" {
			return ev
		}
		return req.Header.Get("X-GitHub-Event")
	case "forgejo":
		if ev := req.Header.Get("X-Forgejo-Event"); ev != "" {
			return ev
		}
		return req.Header.Get("X-Gitea-Event")
	default:
		if ev := req.Header.Get("X-GitHub-Event"); ev != "" {
			return ev
		}
		return req.Header.Get("X-Gitea-Event")
	}
}

// Handle processes an incoming webhook request for a given provider.
func (r *Receiver) Handle(ctx context.Context, provider string, req *http.Request, w http.ResponseWriter) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider != "github" && provider != "gitea" && provider != "forgejo" {
		r.logger.Warn("rejected webhook for unknown provider", "provider", provider, "remote_ip", req.RemoteAddr)
		http.Error(w, `{"error": "unsupported provider"}`, http.StatusBadRequest)
		return
	}

	secret, err := r.secretResolver.ResolveSecret(ctx, provider)
	if err != nil || secret == "" {
		r.logger.Error("failed resolving webhook secret", "provider", provider, "err", err)
		http.Error(w, `{"error": "webhook secret not configured"}`, http.StatusInternalServerError)
		return
	}

	sigHeader := ExtractSignatureHeader(req, provider)
	if sigHeader == "" {
		r.logger.Warn("rejected webhook missing signature header", "provider", provider, "remote_ip", req.RemoteAddr)
		http.Error(w, `{"error": "missing signature"}`, http.StatusUnauthorized)
		return
	}

	// Limit body read to 10MB
	body, err := io.ReadAll(io.LimitReader(req.Body, 10*1024*1024))
	if err != nil {
		r.logger.Warn("failed reading webhook body", "provider", provider, "err", err)
		http.Error(w, `{"error": "failed reading request body"}`, http.StatusBadRequest)
		return
	}

	// Constant-time HMAC signature verification
	if !VerifySignature([]byte(secret), body, sigHeader) {
		r.logger.Warn("rejected webhook invalid or tampered signature", "provider", provider, "remote_ip", req.RemoteAddr)
		http.Error(w, `{"error": "invalid signature"}`, http.StatusUnauthorized)
		return
	}

	eventHeader := ExtractEventHeader(req, provider)
	if eventHeader == "ping" {
		r.logger.Info("acknowledged webhook ping", "provider", provider)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "ok", "message": "pong"}`))
		return
	}

	// Parse workflow_job payload
	var event WorkflowJobEvent
	if err := json.Unmarshal(body, &event); err != nil {
		r.logger.Warn("failed parsing webhook JSON body", "provider", provider, "err", err)
		http.Error(w, `{"error": "invalid json payload"}`, http.StatusBadRequest)
		return
	}

	// Validate action
	switch event.Action {
	case "queued", "in_progress", "completed", "waiting":
		// Valid workflow job action
	case "":
		r.logger.Warn("webhook payload missing action field", "provider", provider)
		http.Error(w, `{"error": "action is required"}`, http.StatusBadRequest)
		return
	default:
		// Unsupported or unhandled action (e.g. non-job event sent to workflow_job endpoint)
		r.logger.Info("ignoring unhandled webhook action", "provider", provider, "action", event.Action)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"status": "ignored", "action": %q}`, event.Action)
		return
	}

	// Fast-ack response (202 Accepted)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "accepted",
		"provider": provider,
		"action":   event.Action,
		"job_id":   event.WorkflowJob.ID,
		"repo":     event.Repository.FullName,
	})

	r.logger.Info("accepted webhook workflow_job event",
		"provider", provider,
		"action", event.Action,
		"job_id", event.WorkflowJob.ID,
		"repo", event.Repository.FullName,
	)

	// Dispatch to event handler if configured
	if r.eventHandler != nil {
		if err := r.eventHandler.HandleWorkflowJob(ctx, provider, &event); err != nil {
			r.logger.Error("error handling webhook workflow_job event", "provider", provider, "job_id", event.WorkflowJob.ID, "err", err)
		}
	}
}
