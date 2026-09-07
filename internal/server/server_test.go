package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/webhook"
)

// do executes a request against the server's routing handler without
// binding a port and returns the recorder.
func do(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// body decodes a Report from a recorder body.
func body(t *testing.T, rec *httptest.ResponseRecorder) Report {
	t.Helper()
	var report Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decoding response body %q: %v", rec.Body.String(), err)
	}
	return report
}

// TestHealthzServesLiveness verifies GET /healthz answers 200 with the
// healthy aggregate once the liveness probe passes.
func TestHealthzServesLiveness(t *testing.T) {
	h := NewHealth()
	h.RegisterLiveness(fixedCheck{name: "db", status: StatusOK})
	s := New(Options{Port: 8080, Health: h})

	rec := do(t, s, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if report := body(t, rec); report.Status != "healthy" || report.Checks["db"] != "ok" {
		t.Errorf("report = %+v, want status healthy and db ok", report)
	}
}

// TestHealthzUnhealthyAnswers503 verifies a failing liveness probe answers
// 503 so orchestrators (compose healthcheck, load balancers) treat the
// process as a restart candidate.
func TestHealthzUnhealthyAnswers503(t *testing.T) {
	h := NewHealth()
	h.RegisterLiveness(fixedCheck{name: "db", status: StatusFail})
	s := New(Options{Port: 8080, Health: h})

	rec := do(t, s, "/healthz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /healthz = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if report := body(t, rec); report.Status != "unhealthy" {
		t.Errorf("status = %q, want unhealthy", report.Status)
	}
}

// TestReadyzDegradedPathViaFakeProbe is the RUN-10 acceptance test: the
// degraded path is exercised through a fake probe, and the body matches
// the OQ #19 example byte-for-byte.
func TestReadyzDegradedPathViaFakeProbe(t *testing.T) {
	h := daemonProbeSet(t, StatusDegraded)
	s := New(Options{Port: 8080, Health: h})

	rec := do(t, s, "/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /readyz = %d, want %d: degraded docker must stay ready (body %s)",
			rec.Code, http.StatusOK, rec.Body.String())
	}
	// encoding/json orders struct fields by declaration and map keys
	// alphabetically, so the wire format is deterministic.
	const want = `{"status":"ready","checks":{"auditor":"ok","db":"ok","docker":"degraded"}}` + "\n"
	if rec.Body.String() != want {
		t.Errorf("GET /readyz body = %s, want %s", rec.Body.String(), want)
	}
}

// TestReadyzNotReadyAnswers503 verifies a failed readiness probe answers
// 503 so no work is routed to the instance.
func TestReadyzNotReadyAnswers503(t *testing.T) {
	h := NewHealth()
	h.RegisterReadiness(fixedCheck{name: "db", status: StatusFail})
	s := New(Options{Port: 8080, Health: h})

	rec := do(t, s, "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if report := body(t, rec); report.Status != "not_ready" {
		t.Errorf("status = %q, want not_ready", report.Status)
	}
}

// TestHealthEndpointsProbeStateIsLive verifies each request re-evaluates
// its probes: a status change between two GETs is reflected immediately,
// which is what real M2/M5 probes rely on.
func TestHealthEndpointsProbeStateIsLive(t *testing.T) {
	status := StatusOK
	h := NewHealth()
	h.RegisterLiveness(NewCheck("db", func(context.Context) Status { return status }))
	h.RegisterReadiness(NewCheck("db", func(context.Context) Status { return status }))
	s := New(Options{Port: 8080, Health: h})

	if rec := do(t, s, "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("first GET /healthz = %d, want %d", rec.Code, http.StatusOK)
	}

	status = StatusFail

	if rec := do(t, s, "/healthz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("second GET /healthz = %d, want %d after probe flipped", rec.Code, http.StatusServiceUnavailable)
	}
	if rec := do(t, s, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz = %d, want %d after probe flipped", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestUnknownRouteAnswers404 verifies unknown API routes answer 404 rather than falling back to SPA index.
func TestUnknownRouteAnswers404(t *testing.T) {
	s := New(Options{Port: 8080})
	if rec := do(t, s, "/supervisor.v1.UnknownService/Method"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /supervisor.v1.UnknownService/Method = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestDefaultHealthRegistryMounted verifies New builds a usable empty
// registry when none is supplied: endpoints answer without probes rather
// than panicking on a nil map.
func TestDefaultHealthRegistryMounted(t *testing.T) {
	s := New(Options{Port: 8080})

	if rec := do(t, s, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want %d with no probes", rec.Code, http.StatusOK)
	}
	if rec := do(t, s, "/readyz"); rec.Code != http.StatusOK {
		t.Errorf("GET /readyz = %d, want %d with no probes", rec.Code, http.StatusOK)
	}
}

func TestSPAFallbackRouting(t *testing.T) {
	s := New(Options{Port: 8080})

	// 1. Root path serves index.html
	rec := do(t, s, "/")
	if rec.Code != http.StatusOK {
		t.Errorf("GET / = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "AIO Supervisor") {
		t.Errorf("expected SPA index content, got: %s", rec.Body.String())
	}

	// 2. Client-side routes fallback to index.html
	recPools := do(t, s, "/pools")
	if recPools.Code != http.StatusOK {
		t.Errorf("GET /pools = %d, want 200", recPools.Code)
	}
	if !strings.Contains(recPools.Body.String(), "AIO Supervisor") {
		t.Errorf("expected SPA fallback for /pools, got: %s", recPools.Body.String())
	}

	// 3. Unknown API route returns 404, not index.html
	recAPI := do(t, s, "/supervisor.v1.UnknownService/Method")
	if recAPI.Code != http.StatusNotFound {
		t.Errorf("unknown API route should return 404, got: %d", recAPI.Code)
	}
}

type mockAuthService struct {
	supervisorv1connect.UnimplementedAuthServiceHandler
}

func (m *mockAuthService) GetSession(ctx context.Context, req *connect.Request[supervisorv1.GetSessionRequest]) (*connect.Response[supervisorv1.GetSessionResponse], error) {
	return connect.NewResponse(&supervisorv1.GetSessionResponse{
		Username: "admin",
		IsAdmin:  true,
	}), nil
}

func TestConnectBinaryTransportWiring(t *testing.T) {
	s := New(Options{Port: 8080})
	authPath, authHandler := supervisorv1connect.NewAuthServiceHandler(
		&mockAuthService{},
		BinaryConnectHandlerOptions()...,
	)
	s.MountConnectHandler(authPath, authHandler)

	// Start a test server with s.Handler()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// 1. Acceptance: Connect unary call round-trips binary successfully
	client := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
	res, err := client.GetSession(context.Background(), connect.NewRequest(&supervisorv1.GetSessionRequest{}))
	if err != nil {
		t.Fatalf("binary Connect unary call failed: %v", err)
	}
	if res.Msg.Username != "admin" || !res.Msg.IsAdmin {
		t.Errorf("unexpected response message: %+v", res.Msg)
	}

	// 2. Acceptance: JSON transport is rejected (binary protocol mandatory per docs/06 §1)
	jsonReq, err := http.NewRequest(http.MethodPost, ts.URL+"/supervisor.v1.AuthService/GetSession", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("failed to create JSON request: %v", err)
	}
	jsonReq.Header.Set("Content-Type", "application/json")
	jsonResp, err := ts.Client().Do(jsonReq)
	if err != nil {
		t.Fatalf("JSON request failed: %v", err)
	}
	_ = jsonResp.Body.Close()
	if jsonResp.StatusCode != http.StatusUnsupportedMediaType && jsonResp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected JSON transport to be rejected with 415 or 400, got: %d", jsonResp.StatusCode)
	}
}

func TestWebhookReceiverRouting(t *testing.T) {
	secret := "test-secret"
	recv := webhook.NewReceiver(
		webhook.StaticSecretResolver(map[string]string{"github": secret}),
		webhook.WithEventHandler(webhook.EventHandlerFunc(func(ctx context.Context, provider string, event *webhook.WorkflowJobEvent) error {
			return nil
		})),
	)

	s := New(Options{
		Port:            8080,
		WebhookReceiver: recv,
	})

	if s.WebhookReceiver() != recv {
		t.Fatalf("WebhookReceiver() does not match expected receiver")
	}

	payload := []byte(`{"action":"queued","workflow_job":{"id":123,"status":"queued","name":"test-job"}}`)
	sig := webhook.SignPayload(payload, []byte(secret))

	// 1. Valid signature
	req := httptest.NewRequest(http.MethodPost, "/hooks/github", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "workflow_job")
	req.Header.Set("X-Hub-Signature-256", "sha256="+sig)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. Tampered signature
	reqBad := httptest.NewRequest(http.MethodPost, "/hooks/github", strings.NewReader(string(payload)))
	reqBad.Header.Set("Content-Type", "application/json")
	reqBad.Header.Set("X-GitHub-Event", "workflow_job")
	reqBad.Header.Set("X-Hub-Signature-256", "sha256=invalidhex00000000000000000000000000000000000000000000000000000000")
	recBad := httptest.NewRecorder()
	s.Handler().ServeHTTP(recBad, reqBad)

	if recBad.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for bad signature, got %d: %s", recBad.Code, recBad.Body.String())
	}

	// 3. Unknown provider
	reqUnknown := httptest.NewRequest(http.MethodPost, "/hooks/unknown", strings.NewReader(string(payload)))
	reqUnknown.Header.Set("Content-Type", "application/json")
	recUnknown := httptest.NewRecorder()
	s.Handler().ServeHTTP(recUnknown, reqUnknown)

	if recUnknown.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for unknown provider, got %d: %s", recUnknown.Code, recUnknown.Body.String())
	}
}
