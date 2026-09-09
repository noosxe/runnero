package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/webhook"
)

// testWebhookSecret is a high-entropy-looking placeholder shared secret for
// the wiring tests below. It signs and verifies only against test fakes.
const testWebhookSecret = "run153-webhook-secret-0123456789abcdef"

// bootDaemonForTest boots runDaemonContext hermetically (fake Docker engine,
// free port, temp data dir) after the caller has set any extra SUPERVISOR_*
// environment via t.Setenv, waits for readiness, and registers cleanup that
// shuts the daemon down. Returns the daemon base URL.
func bootDaemonForTest(t *testing.T) string {
	t.Helper()
	validKeyEnv(t)
	setFakeDocker(t)
	port := freePort(t)
	t.Setenv("SUPERVISOR_DATA_DIR", t.TempDir())
	t.Setenv("SUPERVISOR_PORT", strconv.Itoa(port))

	root := NewRootCommand()
	if err := bindFlagsToConfig(root, nil); err != nil {
		t.Fatalf("loading configuration: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runDaemonContext(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon returned %v after shutdown", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not shut down after context cancellation")
		}
	})

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	// Drain boot: /healthz answers once the HTTP listener is up.
	getHealth(t, base+"/healthz")
	return base
}

// signedWebhook builds a POST /hooks/{provider} request whose body is signed
// with the given secret using the GitHub header convention (the receiver
// accepts both raw hex and the "sha256=" prefix).
func signedWebhook(t *testing.T, url, secret string, payload []byte, event string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	if secret != "" {
		req.Header.Set("X-Hub-Signature-256", "sha256="+webhook.SignPayload(payload, []byte(secret)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

// queuedJobPayload is a minimal workflow_job queued event for org/repo.
var queuedJobPayload = []byte(`{"action":"queued","workflow_job":{"id":42,"run_id":7,"head_branch":"main"},"repository":{"id":1,"name":"repo","full_name":"org/repo"}}`)

// TestDaemonWebhookReceiverMountedWhenSecretConfigured is the RUN-153
// acceptance test: with a provider secret in the environment the production
// daemon mounts POST /hooks/{provider}, enforces HMAC signatures end-to-end,
// and hands verified workflow_job events to the pool controller.
func TestDaemonWebhookReceiverMountedWhenSecretConfigured(t *testing.T) {
	t.Setenv("SUPERVISOR_WEBHOOK_GITHUB_SECRET", testWebhookSecret)
	t.Setenv("SUPERVISOR_WEBHOOK_FORGEJO_SECRET", testWebhookSecret)
	base := bootDaemonForTest(t)

	t.Run("signed workflow_job event accepted", func(t *testing.T) {
		resp := signedWebhook(t, base+"/hooks/github", testWebhookSecret, queuedJobPayload, "workflow_job")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("POST /hooks/github = %d, want 202 accepted", resp.StatusCode)
		}
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decoding acceptance body: %v", err)
		}
		if body["status"] != "accepted" || body["provider"] != "github" || body["repo"] != "org/repo" {
			t.Errorf("acceptance body = %v, want accepted github event for org/repo", body)
		}
	})

	t.Run("ping handshake acknowledged", func(t *testing.T) {
		resp := signedWebhook(t, base+"/hooks/github", testWebhookSecret, []byte(`{"zen":"ok"}`), "ping")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("ping = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("tampered signature rejected", func(t *testing.T) {
		resp := signedWebhook(t, base+"/hooks/github", "not-the-configured-secret", queuedJobPayload, "workflow_job")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("webhook with wrong secret = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("missing signature rejected", func(t *testing.T) {
		resp := signedWebhook(t, base+"/hooks/github", "", queuedJobPayload, "workflow_job")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("webhook without signature = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("unconfigured provider on mounted receiver rejected", func(t *testing.T) {
		resp := signedWebhook(t, base+"/hooks/gitea", testWebhookSecret, queuedJobPayload, "workflow_job")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("gitea webhook without configured secret = %d, want 500 (secret not configured)", resp.StatusCode)
		}
	})

	t.Run("unsupported provider rejected", func(t *testing.T) {
		resp := signedWebhook(t, base+"/hooks/gitlab", testWebhookSecret, queuedJobPayload, "workflow_job")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("gitlab webhook = %d, want 400 (unsupported provider)", resp.StatusCode)
		}
	})
}

// TestDaemonWebhookRouteUnmountedWithoutSecrets pins the wire-default: a
// deployment without any webhook secrets boots exactly as before — the
// /hooks/{provider} route stays unmounted: Echo routes by method, so a POST
// on the unmounted path answers 405 (never the SPA, never a 2xx a provider
// would read as delivered).
func TestDaemonWebhookRouteUnmountedWithoutSecrets(t *testing.T) {
	// Explicit empties beat any ambient values in the environment.
	t.Setenv("SUPERVISOR_WEBHOOK_GITHUB_SECRET", "")
	t.Setenv("SUPERVISOR_WEBHOOK_GITEA_SECRET", "")
	t.Setenv("SUPERVISOR_WEBHOOK_FORGEJO_SECRET", "")
	base := bootDaemonForTest(t)

	resp, err := http.Post(base+"/hooks/github", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /hooks/github: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /hooks/github without configured secrets = %d, want 405 (route unmounted)", resp.StatusCode)
	}
}

// TestConfiguredWebhookSecrets unit-checks the secret collection helper:
// whitespace-only values count as unconfigured, keys use the canonical
// lowercase provider segments, and the merged config (not raw environment)
// is the source.
func TestConfiguredWebhookSecrets(t *testing.T) {
	// configuredWebhookSecrets reads the package cfg, so bind a fresh load
	// from the current environment for each case.
	bind := func(t *testing.T) {
		t.Helper()
		validKeyEnv(t)
		root := NewRootCommand()
		if err := bindFlagsToConfig(root, nil); err != nil {
			t.Fatalf("loading configuration: %v", err)
		}
	}

	t.Run("all empty yields no receiver input", func(t *testing.T) {
		t.Setenv("SUPERVISOR_WEBHOOK_GITHUB_SECRET", "")
		t.Setenv("SUPERVISOR_WEBHOOK_GITEA_SECRET", "   ")
		t.Setenv("SUPERVISOR_WEBHOOK_FORGEJO_SECRET", "")
		bind(t)
		if got := configuredWebhookSecrets(); len(got) != 0 {
			t.Errorf("configuredWebhookSecrets() = %v, want empty map", got)
		}
	})

	t.Run("configured subset wins with canonical keys", func(t *testing.T) {
		t.Setenv("SUPERVISOR_WEBHOOK_GITHUB_SECRET", "gh-secret")
		t.Setenv("SUPERVISOR_WEBHOOK_FORGEJO_SECRET", "fj-secret")
		bind(t)
		got := configuredWebhookSecrets()
		want := map[string]string{"github": "gh-secret", "forgejo": "fj-secret"}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("configuredWebhookSecrets() = %v, want %v", got, want)
		}
	})
}
