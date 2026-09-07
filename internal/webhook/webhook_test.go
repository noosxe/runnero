package webhook_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/noosxe/runnero/internal/webhook"
)

func computeSignature(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func samplePayload(action string, jobID int64) []byte {
	evt := webhook.WorkflowJobEvent{
		Action: action,
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:           jobID,
			RunID:        9999,
			WorkflowName: "CI Workflow",
			Status:       action,
			Labels:       []string{"self-hosted", "linux", "arm64"},
		},
		Repository: webhook.RepositoryPayload{
			ID:       123,
			Name:     "my-repo",
			FullName: "owner/my-repo",
			HTMLURL:  "https://github.com/owner/my-repo",
			CloneURL: "https://github.com/owner/my-repo.git",
		},
	}
	b, _ := json.Marshal(evt)
	return b
}

func TestWebhook_GitHub_ValidSignatureAccepted(t *testing.T) {
	secret := "super-secret-github-key"
	resolver := webhook.StaticSecretResolver(map[string]string{
		"github": secret,
	})

	var handledEvent *webhook.WorkflowJobEvent
	var handledProvider string
	handler := webhook.EventHandlerFunc(func(ctx context.Context, provider string, event *webhook.WorkflowJobEvent) error {
		handledProvider = provider
		handledEvent = event
		return nil
	})

	receiver := webhook.NewReceiver(resolver, webhook.WithEventHandler(handler))

	payload := samplePayload("queued", 1001)
	sig := computeSignature([]byte(secret), payload)

	req := httptest.NewRequest(http.MethodPost, "/hooks/github", bytes.NewReader(payload))
	req.Header.Set("X-Hub-Signature-256", "sha256="+sig)
	req.Header.Set("X-GitHub-Event", "workflow_job")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	receiver.Handle(context.Background(), "github", req, rec)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response JSON: %v", err)
	}
	if resp["status"] != "accepted" {
		t.Errorf("expected status accepted, got %v", resp["status"])
	}
	if resp["action"] != "queued" {
		t.Errorf("expected action queued, got %v", resp["action"])
	}

	if handledProvider != "github" {
		t.Errorf("expected provider github, got %s", handledProvider)
	}
	if handledEvent == nil {
		t.Fatalf("expected handledEvent to be populated")
	}
	if handledEvent.WorkflowJob.ID != 1001 {
		t.Errorf("expected job ID 1001, got %d", handledEvent.WorkflowJob.ID)
	}
	if handledEvent.Repository.FullName != "owner/my-repo" {
		t.Errorf("expected repo owner/my-repo, got %s", handledEvent.Repository.FullName)
	}
}

func TestWebhook_GitHub_TamperedSignatureRejected(t *testing.T) {
	secret := "super-secret-github-key"
	resolver := webhook.StaticSecretResolver(map[string]string{
		"github": secret,
	})

	handled := false
	handler := webhook.EventHandlerFunc(func(ctx context.Context, provider string, event *webhook.WorkflowJobEvent) error {
		handled = true
		return nil
	})

	receiver := webhook.NewReceiver(resolver, webhook.WithEventHandler(handler))

	payload := samplePayload("queued", 1002)
	// Compute valid signature for a different body
	tamperedSig := computeSignature([]byte(secret), []byte("tampered-data"))

	req := httptest.NewRequest(http.MethodPost, "/hooks/github", bytes.NewReader(payload))
	req.Header.Set("X-Hub-Signature-256", "sha256="+tamperedSig)
	req.Header.Set("X-GitHub-Event", "workflow_job")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	receiver.Handle(context.Background(), "github", req, rec)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for tampered signature, got %d", rec.Code)
	}
	if handled {
		t.Fatalf("event handler must not be called when signature is tampered")
	}
}

func TestWebhook_GitHub_MissingSignatureRejected(t *testing.T) {
	secret := "super-secret-github-key"
	resolver := webhook.StaticSecretResolver(map[string]string{
		"github": secret,
	})

	receiver := webhook.NewReceiver(resolver)

	payload := samplePayload("queued", 1003)
	req := httptest.NewRequest(http.MethodPost, "/hooks/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Event", "workflow_job")

	rec := httptest.NewRecorder()
	receiver.Handle(context.Background(), "github", req, rec)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for missing signature, got %d", rec.Code)
	}
}

func TestWebhook_Gitea_ValidSignatureAccepted(t *testing.T) {
	secret := "super-secret-gitea-key"
	resolver := webhook.StaticSecretResolver(map[string]string{
		"gitea": secret,
	})

	var handledEvent *webhook.WorkflowJobEvent
	handler := webhook.EventHandlerFunc(func(ctx context.Context, provider string, event *webhook.WorkflowJobEvent) error {
		handledEvent = event
		return nil
	})

	receiver := webhook.NewReceiver(resolver, webhook.WithEventHandler(handler))

	payload := samplePayload("in_progress", 2002)
	sig := computeSignature([]byte(secret), payload)

	req := httptest.NewRequest(http.MethodPost, "/hooks/gitea", bytes.NewReader(payload))
	req.Header.Set("X-Gitea-Signature", sig) // Gitea header format
	req.Header.Set("X-Gitea-Event", "workflow_job")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	receiver.Handle(context.Background(), "gitea", req, rec)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for Gitea webhook, got %d: %s", rec.Code, rec.Body.String())
	}
	if handledEvent == nil || handledEvent.WorkflowJob.ID != 2002 {
		t.Fatalf("expected handled event for Gitea job 2002")
	}
}

func TestWebhook_Gitea_TamperedSignatureRejected(t *testing.T) {
	secret := "super-secret-gitea-key"
	resolver := webhook.StaticSecretResolver(map[string]string{
		"gitea": secret,
	})

	receiver := webhook.NewReceiver(resolver)

	payload := samplePayload("completed", 2003)
	req := httptest.NewRequest(http.MethodPost, "/hooks/gitea", bytes.NewReader(payload))
	req.Header.Set("X-Gitea-Signature", "deadbeef0000111122223333444455556666777788889999aaaabbbbccccdddd")
	req.Header.Set("X-Gitea-Event", "workflow_job")

	rec := httptest.NewRecorder()
	receiver.Handle(context.Background(), "gitea", req, rec)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for tampered Gitea signature, got %d", rec.Code)
	}
}

func TestWebhook_PingAcknowledged(t *testing.T) {
	secret := "ping-secret"
	resolver := webhook.StaticSecretResolver(map[string]string{
		"github": secret,
	})

	receiver := webhook.NewReceiver(resolver)

	payload := []byte(`{"zen": "Favor focus over features."}`)
	sig := computeSignature([]byte(secret), payload)

	req := httptest.NewRequest(http.MethodPost, "/hooks/github", bytes.NewReader(payload))
	req.Header.Set("X-Hub-Signature-256", "sha256="+sig)
	req.Header.Set("X-GitHub-Event", "ping")

	rec := httptest.NewRecorder()
	receiver.Handle(context.Background(), "github", req, rec)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for ping, got %d", rec.Code)
	}

	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["message"] != "pong" {
		t.Errorf("expected pong response, got %v", resp["message"])
	}
}

func TestWebhook_UnknownProviderRejected(t *testing.T) {
	resolver := webhook.StaticSecretResolver(map[string]string{})
	receiver := webhook.NewReceiver(resolver)

	req := httptest.NewRequest(http.MethodPost, "/hooks/bitbucket", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	receiver.Handle(context.Background(), "bitbucket", req, rec)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for unknown provider, got %d", rec.Code)
	}
}

func TestWebhook_MissingSecretError(t *testing.T) {
	resolver := webhook.StaticSecretResolver(map[string]string{})
	receiver := webhook.NewReceiver(resolver)

	payload := samplePayload("queued", 3001)
	req := httptest.NewRequest(http.MethodPost, "/hooks/github", bytes.NewReader(payload))
	req.Header.Set("X-Hub-Signature-256", "sha256=12345")

	rec := httptest.NewRecorder()
	receiver.Handle(context.Background(), "github", req, rec)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when secret is not configured, got %d", rec.Code)
	}
}

func TestWebhook_MalformedPayload(t *testing.T) {
	secret := "secret-123"
	resolver := webhook.StaticSecretResolver(map[string]string{
		"github": secret,
	})
	receiver := webhook.NewReceiver(resolver)

	malformed := []byte(`{invalid-json}`)
	sig := computeSignature([]byte(secret), malformed)

	req := httptest.NewRequest(http.MethodPost, "/hooks/github", bytes.NewReader(malformed))
	req.Header.Set("X-Hub-Signature-256", "sha256="+sig)
	req.Header.Set("X-GitHub-Event", "workflow_job")

	rec := httptest.NewRecorder()
	receiver.Handle(context.Background(), "github", req, rec)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for malformed payload, got %d", rec.Code)
	}
}

func TestWebhook_MissingAction(t *testing.T) {
	secret := "secret-123"
	resolver := webhook.StaticSecretResolver(map[string]string{
		"github": secret,
	})
	receiver := webhook.NewReceiver(resolver)

	noAction := []byte(`{"workflow_job": {"id": 123}}`)
	sig := computeSignature([]byte(secret), noAction)

	req := httptest.NewRequest(http.MethodPost, "/hooks/github", bytes.NewReader(noAction))
	req.Header.Set("X-Hub-Signature-256", "sha256="+sig)
	req.Header.Set("X-GitHub-Event", "workflow_job")

	rec := httptest.NewRecorder()
	receiver.Handle(context.Background(), "github", req, rec)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for missing action, got %d", rec.Code)
	}
}
