package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/noosxe/runnero/internal/server"
)

func TestSecurityHeadersPresence(t *testing.T) {
	srv := server.New(server.Options{
		Port: 8080,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got: %d", resp.StatusCode)
	}

	// 1. Verify X-Frame-Options: DENY
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}

	// 2. Verify X-Content-Type-Options: nosniff
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}

	// 3. Verify Content-Security-Policy restricts to self
	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Error("Content-Security-Policy header is missing")
	}
	if got := csp; got != server.ValueCSP {
		t.Errorf("Content-Security-Policy = %q", got)
	}
	// The inline pre-paint theme script (web/index.html) must stay allowlisted.
	if !strings.Contains(csp, "'sha256-/bRTsAHXsuyQc/IEeJa0n8pJe74NAM94NwdrqChr4lk='") {
		t.Error("Content-Security-Policy does not allowlist the inline theme bootstrap script")
	}

	// 4. Verify Referrer-Policy: strict-origin-when-cross-origin
	if got := resp.Header.Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Errorf("Referrer-Policy = %q, want strict-origin-when-cross-origin", got)
	}

	// 5. Verify NO Strict-Transport-Security (TLS is reverse proxy's responsibility per OQ #25, #26)
	if hsts := resp.Header.Get("Strict-Transport-Security"); hsts != "" {
		t.Errorf("Strict-Transport-Security must not be set, got: %q", hsts)
	}
}

func TestCORSDeniedByDefault(t *testing.T) {
	srv := server.New(server.Options{
		Port: 8080,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	serverURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parsing test server URL: %v", err)
	}

	// 1. Cross-origin GET request is rejected with 403 Forbidden
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", "https://malicious-site.example.com")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("cross-origin GET failed: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin GET status = %d, want 403 Forbidden", resp.StatusCode)
	}
	if acao := resp.Header.Get("Access-Control-Allow-Origin"); acao != "" {
		t.Errorf("Access-Control-Allow-Origin must not be present, got: %q", acao)
	}

	// 2. Cross-origin OPTIONS preflight request is rejected with 403 Forbidden
	preflightReq, err := http.NewRequest(http.MethodOptions, ts.URL+"/supervisor.v1.AuthService/Login", nil)
	if err != nil {
		t.Fatalf("NewRequest OPTIONS: %v", err)
	}
	preflightReq.Header.Set("Origin", "https://attacker.org")
	preflightReq.Header.Set("Access-Control-Request-Method", "POST")

	preflightResp, err := ts.Client().Do(preflightReq)
	if err != nil {
		t.Fatalf("cross-origin OPTIONS failed: %v", err)
	}
	_ = preflightResp.Body.Close()

	if preflightResp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin OPTIONS status = %d, want 403 Forbidden", preflightResp.StatusCode)
	}
	if acao := preflightResp.Header.Get("Access-Control-Allow-Origin"); acao != "" {
		t.Errorf("preflight Access-Control-Allow-Origin must not be present, got: %q", acao)
	}

	// 3. Same-origin request with matching Origin is allowed
	sameOriginReq, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatalf("NewRequest same origin: %v", err)
	}
	sameOriginReq.Header.Set("Origin", "http://"+serverURL.Host)

	sameOriginResp, err := ts.Client().Do(sameOriginReq)
	if err != nil {
		t.Fatalf("same-origin GET failed: %v", err)
	}
	defer func() { _ = sameOriginResp.Body.Close() }()

	if sameOriginResp.StatusCode != http.StatusOK {
		t.Errorf("same-origin GET status = %d, want 200 OK", sameOriginResp.StatusCode)
	}

	// 4. Request without Origin header is allowed
	noOriginReq, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatalf("NewRequest no origin: %v", err)
	}

	noOriginResp, err := ts.Client().Do(noOriginReq)
	if err != nil {
		t.Fatalf("no-origin GET failed: %v", err)
	}
	defer func() { _ = noOriginResp.Body.Close() }()

	if noOriginResp.StatusCode != http.StatusOK {
		t.Errorf("no-origin GET status = %d, want 200 OK", noOriginResp.StatusCode)
	}
	body, _ := io.ReadAll(noOriginResp.Body)
	if len(body) == 0 {
		t.Error("expected non-empty health response")
	}
}
