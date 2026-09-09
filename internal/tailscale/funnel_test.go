package tailscale

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/noosxe/runnero/internal/webhook"
)

// recordingReceiver is a stub WebhookHandler capturing the provider it was
// handed and answering with a fixed status (pure funnel-mux tests never
// touch the real receiver).
type recordingReceiver struct {
	provider string
	calls    int
	status   int
}

func (r *recordingReceiver) Handle(_ context.Context, provider string, _ *http.Request, w http.ResponseWriter) {
	r.provider = provider
	r.calls++
	w.WriteHeader(r.status)
}

// TestFunnelHandlerRoutesDelegatesToReceiver pins the mounted behavior:
// every POST /hooks/{segment} reaches the receiver verbatim — including
// unsupported providers, which the receiver itself answers 400 (RUN-153
// semantics, docs/26 §3).
func TestFunnelHandlerRoutesDelegatesToReceiver(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     string
		provider string
	}{
		{"github", "/hooks/github", "github"},
		{"gitea", "/hooks/gitea", "gitea"},
		{"forgejo", "/hooks/forgejo", "forgejo"},
		{"unsupported provider still reaches receiver", "/hooks/gitlab", "gitlab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receiver := &recordingReceiver{status: http.StatusAccepted}
			srv := httptest.NewServer(FunnelHandler(receiver))
			t.Cleanup(srv.Close)

			resp, err := http.Post(srv.URL+tc.path, "application/json", bytes.NewReader([]byte("{}")))
			if err != nil {
				t.Fatalf("POST %s: %v", tc.path, err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("POST %s = %d, want %d from the receiver", tc.path, resp.StatusCode, http.StatusAccepted)
			}
			if receiver.calls != 1 || receiver.provider != tc.provider {
				t.Fatalf("receiver got (%q, %d calls), want (%q, 1 call)", receiver.provider, receiver.calls, tc.provider)
			}
		})
	}
}

// TestFunnelHandlerUnmountedAnswers405 pins the nil-receiver behavior: the
// route group answers 405 (mirroring the unmounted LAN route), never a 2xx
// a provider would read as delivered.
func TestFunnelHandlerUnmountedAnswers405(t *testing.T) {
	srv := httptest.NewServer(FunnelHandler(nil))
	t.Cleanup(srv.Close)

	for _, path := range []string{"/hooks/github", "/hooks/gitlab"} {
		resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader([]byte("{}")))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s on unmounted funnel = %d, want 405", path, resp.StatusCode)
		}
	}
}

// TestFunnelHandlerEverythingElseAnswers404 pins the locked-down surface:
// every non-hook path and method answers 404 — no SPA, no API, no health,
// no trailing-slash or prefix evasions.
func TestFunnelHandlerEverythingElseAnswers404(t *testing.T) {
	srv := httptest.NewServer(FunnelHandler(&recordingReceiver{status: http.StatusAccepted}))
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"GET root", http.MethodGet, "/"},
		{"GET hooks path", http.MethodGet, "/hooks/github"},
		{"POST root", http.MethodPost, "/"},
		{"POST other path", http.MethodPost, "/other"},
		{"POST hooks without provider", http.MethodPost, "/hooks/"},
		{"POST hooks subpath", http.MethodPost, "/hooks/github/extra"},
		{"POST prefix lookalike", http.MethodPost, "/hooksawesome"},
		{"PUT hooks path", http.MethodPut, "/hooks/github"},
		{"HEAD hooks path", http.MethodHead, "/hooks/github"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, srv.URL+tc.path, nil)
			if err != nil {
				t.Fatalf("building %s %s: %v", tc.method, tc.path, err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", tc.method, tc.path, err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("%s %s = %d, want 404", tc.method, tc.path, resp.StatusCode)
			}
		})
	}
}

// TestFunnelHandlerDelegatesRealReceiver end-to-end spot check: the real
// webhook receiver behind the funnel mux keeps the exact RUN-153 statuses
// (signed → 202, unsupported provider → 400, unconfigured provider → 500).
func TestFunnelHandlerDelegatesRealReceiver(t *testing.T) {
	const secret = "run155-funnel-secret-0123456789abcdef"
	receiver := webhook.NewReceiver(
		webhook.StaticSecretResolver(map[string]string{"github": secret}),
	)
	srv := httptest.NewServer(FunnelHandler(receiver))
	t.Cleanup(srv.Close)

	post := func(t *testing.T, path, sig string) int {
		t.Helper()
		payload := []byte(`{"action":"queued"}`)
		req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if sig != "" {
			req.Header.Set("X-Hub-Signature-256", "sha256="+webhook.SignPayload(payload, []byte(secret)))
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	t.Run("signed github accepted", func(t *testing.T) {
		if got := post(t, "/hooks/github", "sign"); got != http.StatusAccepted {
			t.Fatalf("signed webhook = %d, want 202", got)
		}
	})
	t.Run("missing signature rejected", func(t *testing.T) {
		if got := post(t, "/hooks/github", ""); got != http.StatusUnauthorized {
			t.Fatalf("unsigned webhook = %d, want 401", got)
		}
	})
	t.Run("unsupported provider rejected", func(t *testing.T) {
		if got := post(t, "/hooks/gitlab", "sign"); got != http.StatusBadRequest {
			t.Fatalf("gitlab webhook = %d, want 400", got)
		}
	})
	t.Run("unconfigured provider rejected", func(t *testing.T) {
		if got := post(t, "/hooks/gitea", "sign"); got != http.StatusInternalServerError {
			t.Fatalf("gitea webhook = %d, want 500", got)
		}
	})
}
