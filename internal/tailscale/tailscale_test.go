package tailscale

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

// fakeNode is a hermetic Node: real loopback listeners, scripted Start
// failures, and counters for lifecycle assertions.
type fakeNode struct {
	mu        sync.Mutex
	startErrs []error // popped one per Start call; empty → success
	starts    int
	closed    bool
	failTLS   bool
	funnelLn  net.Listener
	tailnetLn net.Listener
}

func (f *fakeNode) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if len(f.startErrs) > 0 {
		err := f.startErrs[0]
		f.startErrs = f.startErrs[1:]
		return err
	}
	return nil
}

func (f *fakeNode) listen() (net.Listener, error) {
	if f.failTLS {
		return nil, errors.New("tls listen blocked by fake")
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

func (f *fakeNode) ListenFunnel(_, _ string) (net.Listener, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ln, err := f.listen()
	if err == nil {
		f.funnelLn = ln
	}
	return ln, err
}

func (f *fakeNode) ListenTLS(_, _ string) (net.Listener, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ln, err := f.listen()
	if err == nil {
		f.tailnetLn = ln
	}
	return ln, err
}

func (f *fakeNode) DNSName() string { return "runnero.example.ts.net" }

func (f *fakeNode) Close() error {
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

func (f *fakeNode) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeNode) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

// okHandler answers 200 to everything; okBody marks which listener served.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	_, _ = io.WriteString(w, "tailnet")
})

// startFake runs Start against a fake node with fast retries.
func startFake(t *testing.T, ctx context.Context, fake *fakeNode, cfg Config, handlers Handlers) (*Stack, error) {
	t.Helper()
	return Start(ctx, cfg, handlers, Options{
		NewNode:       func(Config) (Node, error) { return fake, nil },
		StartAttempts: 3,
		StartDelay:    time.Millisecond,
	})
}

// request walks plain HTTP against a loopback listener until it answers or
// the deadline passes (the serve goroutine races the first request).
func request(t *testing.T, ln net.Listener, method, path string) *http.Response {
	t.Helper()
	url := fmt.Sprintf("http://%s%s", ln.Addr().String(), path)
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(method, url, nil)
		if err != nil {
			t.Fatalf("building %s %s: %v", method, path, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			return resp
		}
		lastErr = err
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s %s never came up: %v", method, url, lastErr)
	return nil
}

func TestStartServesBothListenersWithExpectedHandlers(t *testing.T) {
	fake := &fakeNode{}
	stack, err := startFake(t, context.Background(), fake, Config{
		AuthKey: "k", Hostname: "runnero", StateDir: t.TempDir(), Funnel: true, UI: true,
	}, Handlers{Funnel: FunnelHandler(nil), Tailnet: okHandler})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = stack.Shutdown(context.Background()) })

	if fake.startCount() != 1 {
		t.Fatalf("node started %d times, want 1", fake.startCount())
	}

	// Funnel listener: webhook-only surface — root 404, unmounted hook 405.
	resp := request(t, fake.funnelLn, http.MethodGet, "/")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("funnel GET / = %d, want 404", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp = request(t, fake.funnelLn, http.MethodPost, "/hooks/github")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("funnel POST /hooks/github = %d, want 405 (receiver unmounted)", resp.StatusCode)
	}

	// Tailnet listener: full handler.
	resp = request(t, fake.tailnetLn, http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tailnet GET / = %d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// TestStartHonorsListenerToggles: UI-only boots open exactly one listener.
func TestStartHonorsListenerToggles(t *testing.T) {
	fake := &fakeNode{}
	stack, err := startFake(t, context.Background(), fake, Config{
		AuthKey: "k", Hostname: "runnero", StateDir: t.TempDir(), Funnel: false, UI: true,
	}, Handlers{Funnel: FunnelHandler(nil), Tailnet: okHandler})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = stack.Shutdown(context.Background()) })

	if fake.funnelLn != nil {
		t.Error("funnel listener opened despite Funnel=false")
	}
	if fake.tailnetLn == nil {
		t.Error("tailnet listener missing despite UI=true")
	}
}

// TestStartRetriesTransientStartFailures: two failures then success stay
// inside the 3-attempt budget.
func TestStartRetriesTransientStartFailures(t *testing.T) {
	boom := errors.New("control plane hiccup")
	fake := &fakeNode{startErrs: []error{boom, boom}}
	stack, err := startFake(t, context.Background(), fake, Config{
		AuthKey: "k", Hostname: "runnero", StateDir: t.TempDir(), Funnel: true, UI: true,
	}, Handlers{Funnel: FunnelHandler(nil), Tailnet: okHandler})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = stack.Shutdown(context.Background()) })
	if fake.startCount() != 3 {
		t.Fatalf("node started %d times, want 3 (two failures + success)", fake.startCount())
	}
}

// TestStartFailsAfterExhaustedBudget: permanent failure aborts the boot.
func TestStartFailsAfterExhaustedBudget(t *testing.T) {
	fake := &fakeNode{startErrs: []error{errors.New("down"), errors.New("down"), errors.New("down")}}
	_, err := startFake(t, context.Background(), fake, Config{
		AuthKey: "k", Hostname: "runnero", StateDir: t.TempDir(), Funnel: true, UI: true,
	}, Handlers{Funnel: FunnelHandler(nil), Tailnet: okHandler})
	if err == nil {
		t.Fatal("Start succeeded, want failure after the retry budget")
	}
	if got := fake.startCount(); got != 3 {
		t.Fatalf("node started %d times, want exactly the 3-attempt budget", got)
	}
}

// TestStartAbortOnContextCancel: a canceled context during the retry wait
// stops the boot and closes the node.
func TestStartAbortOnContextCancel(t *testing.T) {
	fake := &fakeNode{startErrs: []error{errors.New("down")}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err := Start(ctx, Config{
		AuthKey: "k", Hostname: "runnero", StateDir: t.TempDir(), Funnel: true, UI: true,
	}, Handlers{Funnel: FunnelHandler(nil), Tailnet: okHandler}, Options{
		NewNode:       func(Config) (Node, error) { return fake, nil },
		StartAttempts: 3,
		StartDelay:    50 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("Start succeeded, want abort after context cancellation")
	}
	if !fake.isClosed() {
		t.Error("node not closed after abort")
	}
}

// TestStartRejectsNoListenerConfig: a node with neither listener serves
// nothing; the defensive error keeps the misconfiguration from booting
// even if config validation was bypassed.
func TestStartRejectsNoListenerConfig(t *testing.T) {
	constructed := false
	_, err := Start(context.Background(), Config{AuthKey: "k", StateDir: t.TempDir()},
		Handlers{}, Options{NewNode: func(Config) (Node, error) {
			constructed = true
			return &fakeNode{}, nil
		}})
	if err == nil {
		t.Fatal("Start succeeded with no listeners, want error")
	}
	if constructed {
		t.Error("node constructed despite no-listener config")
	}
}

// TestStartListenerFailureShutsEverythingDown: if the second listener
// cannot open, the first is torn down and the error aborts the boot.
func TestStartListenerFailureShutsEverythingDown(t *testing.T) {
	fake := &fakeNode{failTLS: true}
	_, err := startFake(t, context.Background(), fake, Config{
		AuthKey: "k", Hostname: "runnero", StateDir: t.TempDir(), Funnel: true, UI: true,
	}, Handlers{Funnel: FunnelHandler(nil), Tailnet: okHandler})
	if err == nil {
		t.Fatal("Start succeeded with a failing listener, want error")
	}
	if !fake.isClosed() {
		t.Error("node not closed after listener failure")
	}
}

// TestShutdownDrainsServersAndClosesNode: after Shutdown the listeners are
// gone and the node is closed.
func TestShutdownDrainsServersAndClosesNode(t *testing.T) {
	fake := &fakeNode{}
	stack, err := startFake(t, context.Background(), fake, Config{
		AuthKey: "k", Hostname: "runnero", StateDir: t.TempDir(), Funnel: true, UI: true,
	}, Handlers{Funnel: FunnelHandler(nil), Tailnet: okHandler})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := stack.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !fake.isClosed() {
		t.Error("node not closed after shutdown")
	}
	addr := fake.funnelLn.Addr().String()
	if resp, err := http.Get(fmt.Sprintf("http://%s/", addr)); err == nil {
		_ = resp.Body.Close()
		t.Error("funnel listener still accepting connections after shutdown")
	}
}

// TestNewTSNetNodeCreatesStateDir0700 pins the security posture of the
// state dir (node keys live there, docs/26 §5).
func TestNewTSNetNodeCreatesStateDir0700(t *testing.T) {
	dir := t.TempDir() + "/tailscale"
	node, err := newTSNetNode(Config{StateDir: dir, Hostname: "runnero"})
	if err != nil {
		t.Fatalf("newTSNetNode: %v", err)
	}
	tsn, ok := node.(*tsnetNode)
	if !ok {
		t.Fatalf("newTSNetNode returned %T, want *tsnetNode", node)
	}
	if tsn.srv.Dir != dir || tsn.srv.Hostname != "runnero" || tsn.srv.AuthKey != "" {
		t.Errorf("tsnet.Server fields = (dir=%q hostname=%q authkey set: %v), unexpected", tsn.srv.Dir, tsn.srv.Hostname, tsn.srv.AuthKey != "")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("state dir mode = %o, want 700", got)
	}
}
