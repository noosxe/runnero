package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/tailscale"
)

// fakeTailscaleNode is a hermetic tailscale.Node: real loopback listeners
// instead of a real tailnet, so the production daemon wiring is exercised
// end-to-end without the Tailscale control plane.
type fakeTailscaleNode struct {
	mu        sync.Mutex
	closed    bool
	funnelLn  net.Listener
	tailnetLn net.Listener
}

func (f *fakeTailscaleNode) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = false
	return nil
}

func (f *fakeTailscaleNode) ListenFunnel(_, _ string) (net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err == nil {
		f.mu.Lock()
		f.funnelLn = ln
		f.mu.Unlock()
	}
	return ln, err
}

func (f *fakeTailscaleNode) ListenTLS(_, _ string) (net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err == nil {
		f.mu.Lock()
		f.tailnetLn = ln
		f.mu.Unlock()
	}
	return ln, err
}

func (f *fakeTailscaleNode) DNSName() string { return "runnero.example.ts.net" }

func (f *fakeTailscaleNode) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	for _, ln := range []net.Listener{f.funnelLn, f.tailnetLn} {
		if ln != nil {
			_ = ln.Close()
		}
	}
	return nil
}

func (f *fakeTailscaleNode) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// lastTailscaleBoot records what the daemon wiring passed down to
// startTailscale so tests can assert the config plumb-through and reach
// the fake's listeners.
var lastTailscaleBoot struct {
	mu          sync.Mutex
	cfg         tailscale.Config
	node        *fakeTailscaleNode
	constructed bool
}

func resetLastTailscaleBoot() {
	lastTailscaleBoot.mu.Lock()
	defer lastTailscaleBoot.mu.Unlock()
	lastTailscaleBoot.cfg = tailscale.Config{}
	lastTailscaleBoot.node = nil
	lastTailscaleBoot.constructed = false
}

// fakeStartTailscale replaces the production seam: same Start semantics,
// but the embedded node is the loopback fake, with a fast retry budget.
func fakeStartTailscale(ctx context.Context, cfg tailscale.Config, handlers tailscale.Handlers, opts tailscale.Options) (*tailscale.Stack, error) {
	lastTailscaleBoot.mu.Lock()
	lastTailscaleBoot.cfg = cfg
	lastTailscaleBoot.constructed = true
	lastTailscaleBoot.mu.Unlock()

	node := &fakeTailscaleNode{}
	opts.NewNode = func(tailscale.Config) (tailscale.Node, error) { return node, nil }
	opts.StartAttempts = 2
	opts.StartDelay = time.Millisecond

	stack, err := tailscale.Start(ctx, cfg, handlers, opts)
	if err == nil {
		lastTailscaleBoot.mu.Lock()
		lastTailscaleBoot.node = node
		lastTailscaleBoot.mu.Unlock()
	}
	return stack, err
}

func overrideStartTailscale(t *testing.T, fn func(context.Context, tailscale.Config, tailscale.Handlers, tailscale.Options) (*tailscale.Stack, error)) {
	t.Helper()
	resetLastTailscaleBoot()
	orig := startTailscale
	startTailscale = fn
	t.Cleanup(func() { startTailscale = orig })
}

// TestDaemonTailscaleOffByDefault pins the opt-in contract (docs/26 §4):
// without an auth key the integration is completely inert — no node is
// ever constructed, and stray tailscale variables never fail the boot.
func TestDaemonTailscaleOffByDefault(t *testing.T) {
	overrideStartTailscale(t, fakeStartTailscale)
	// Explicit empties beat any ambient values; the bogus bool must be
	// ignored because the feature is off.
	t.Setenv("SUPERVISOR_TAILSCALE_AUTHKEY", "")
	t.Setenv("SUPERVISOR_TAILSCALE_FUNNEL", "bogus")
	t.Setenv("SUPERVISOR_TAILSCALE_UI", "also-bogus")
	bootDaemonForTest(t)

	lastTailscaleBoot.mu.Lock()
	defer lastTailscaleBoot.mu.Unlock()
	if lastTailscaleBoot.constructed {
		t.Error("tailscale node constructed without an auth key")
	}
}

// TestDaemonTailscaleEnabledServesBothListeners is the RUN-155 acceptance
// test: an enabled boot constructs the embedded node, serves the funnel
// mux (webhooks only) and the full tailnet handler, plumbs the resolved
// config through, and tears everything down on shutdown.
func TestDaemonTailscaleEnabledServesBothListeners(t *testing.T) {
	overrideStartTailscale(t, fakeStartTailscale)
	// The daemon shut down by bootDaemonForTest's cleanup before this
	// assertion runs (cleanups are LIFO).
	t.Cleanup(func() {
		lastTailscaleBoot.mu.Lock()
		node := lastTailscaleBoot.node
		lastTailscaleBoot.mu.Unlock()
		if node == nil {
			t.Error("no tailscale node was constructed")
			return
		}
		if !node.isClosed() {
			t.Error("tailscale node not closed after daemon shutdown")
		}
	})
	t.Setenv("SUPERVISOR_TAILSCALE_AUTHKEY", "tskey-authority-0123456789abcdef")
	t.Setenv("SUPERVISOR_WEBHOOK_GITHUB_SECRET", testWebhookSecret)
	_ = bootDaemonForTest(t)

	lastTailscaleBoot.mu.Lock()
	if !lastTailscaleBoot.constructed || lastTailscaleBoot.node == nil {
		lastTailscaleBoot.mu.Unlock()
		t.Fatal("enabled boot did not construct the tailscale node")
	}
	cfg := lastTailscaleBoot.cfg
	node := lastTailscaleBoot.node
	lastTailscaleBoot.mu.Unlock()

	// Config plumb-through: defaults resolved by internal/config.
	if cfg.AuthKey != "tskey-authority-0123456789abcdef" {
		t.Errorf("AuthKey = %q, want the configured key", cfg.AuthKey)
	}
	if cfg.Hostname != "runnero" {
		t.Errorf("Hostname = %q, want default %q", cfg.Hostname, "runnero")
	}
	if !cfg.Funnel || !cfg.UI {
		t.Errorf("Funnel/UI = %t/%t, want both true by default", cfg.Funnel, cfg.UI)
	}
	if base := filepath.Base(cfg.StateDir); base != "tailscale" || !filepath.IsAbs(cfg.StateDir) {
		t.Errorf("StateDir = %q, want an absolute <data-dir>/tailscale", cfg.StateDir)
	}

	// Funnel listener: webhook-only surface over the real daemon wiring.
	resp, err := http.Get(fmt.Sprintf("http://%s/", node.funnelLn.Addr().String()))
	if err != nil {
		t.Fatalf("GET funnel /: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("funnel GET / = %d, want 404", resp.StatusCode)
	}
	signResp := signedWebhook(t, fmt.Sprintf("http://%s/hooks/github", node.funnelLn.Addr()), testWebhookSecret, queuedJobPayload, "workflow_job")
	defer func() { _ = signResp.Body.Close() }()
	if signResp.StatusCode != http.StatusAccepted {
		t.Fatalf("signed webhook through the funnel = %d, want 202", signResp.StatusCode)
	}

	// Tailnet listener: the full server handler (health answers).
	healthResp, err := http.Get(fmt.Sprintf("http://%s/healthz", node.tailnetLn.Addr().String()))
	if err != nil {
		t.Fatalf("GET tailnet /healthz: %v", err)
	}
	defer func() { _ = healthResp.Body.Close() }()
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("tailnet GET /healthz = %d, want 200", healthResp.StatusCode)
	}
}

// TestDaemonTailscaleBootFailureAbortsBoot pins fail-fast (docs/26 §3): a
// node that cannot start aborts the daemon boot with a clear error — no
// silent degrade to LAN-only.
func TestDaemonTailscaleBootFailureAbortsBoot(t *testing.T) {
	overrideStartTailscale(t, func(context.Context, tailscale.Config, tailscale.Handlers, tailscale.Options) (*tailscale.Stack, error) {
		return nil, errors.New("tailscale: node failed to start after 2 attempts: control plane unreachable")
	})
	t.Setenv("SUPERVISOR_TAILSCALE_AUTHKEY", "tskey-authority-0123456789abcdef")

	validKeyEnv(t)
	setFakeDocker(t)
	t.Setenv("SUPERVISOR_DATA_DIR", t.TempDir())
	t.Setenv("SUPERVISOR_PORT", strconv.Itoa(freePort(t)))
	root := NewRootCommand()
	if err := bindFlagsToConfig(root, nil); err != nil {
		t.Fatalf("loading configuration: %v", err)
	}

	err := runDaemonContext(context.Background())
	if err == nil {
		t.Fatal("daemon booted despite tailscale failure, want abort")
	}
	if !strings.Contains(err.Error(), "tailscale") {
		t.Fatalf("boot error = %v, want it to name tailscale", err)
	}
}
