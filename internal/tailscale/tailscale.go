// Package tailscale embeds a Tailscale node in the supervisor process
// (RUN-155, docs/26): a public Funnel listener serving only the
// HMAC-verified webhook route group, and a tailnet-only HTTPS listener
// serving the full management handler. The integration is completely off
// unless the operator sets SUPERVISOR_TAILSCALE_AUTHKEY.
package tailscale

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"tailscale.com/tsnet"

	"github.com/noosxe/runnero/internal/logging"
)

var logger = logging.For("tailscale")

// Fixed listener addresses (docs/26 §3): the embedded node is dedicated to
// this supervisor, so port knobs would only add surface. Funnel and tailnet
// listeners cannot share a port (upstream serve/funnel limitation).
const (
	FunnelAddr  = ":443"
	TailnetAddr = ":8443"
)

// Boot retry defaults (docs/26 §3): ride transient control-plane hiccups,
// then fail the boot — the operator explicitly asked for Tailscale, so a
// silent degrade would hide breakage.
const (
	DefaultStartAttempts = 3
	DefaultStartDelay    = 10 * time.Second
)

// Node abstracts the embedded tsnet server behind the exact surface the
// supervisor needs, so daemon tests can substitute a fake
// (docs/26 §7). The concrete implementation always applies
// tsnet.FunnelOnly() to funnel listeners: even tailnet clients are meant
// to use the tailnet listener, never the public one.
type Node interface {
	// Start connects the node to the tailnet, blocking until it is online
	// (using AuthKey when the node is not yet enrolled; state in the state
	// dir short-circuits later boots).
	Start() error
	// ListenFunnel opens the public Funnel TLS listener (automatic ts.net
	// certificates).
	ListenFunnel(network, addr string) (net.Listener, error)
	// ListenTLS opens a tailnet-only TLS listener (automatic ts.net
	// certificates); reachable only inside the tailnet by construction.
	ListenTLS(network, addr string) (net.Listener, error)
	// DNSName returns the node's fully-qualified ts.net DNS name, or ""
	// before the node is online.
	DNSName() string
	// Close shuts the node down and releases its resources.
	Close() error
}

// Config carries the resolved supervisor configuration for the embedded
// node (from config.Config after validation).
type Config struct {
	// AuthKey enrolls the node on first boot; ignored on later boots while
	// node state exists (upstream behavior).
	AuthKey string
	// Hostname is the node name inside the tailnet; the final DNS name is
	// <hostname>.<tailnet>.ts.net.
	Hostname string
	// StateDir holds node identity and certificates; keep it inside the
	// supervisor data volume (created 0700).
	StateDir string
	// Funnel opens the public webhook listener on FunnelAddr.
	Funnel bool
	// UI opens the tailnet-only management listener on TailnetAddr.
	UI bool
}

// Handlers pairs the http.Handler served on each listener.
type Handlers struct {
	// Funnel serves POST /hooks/{provider} only (FunnelHandler).
	Funnel http.Handler
	// Tailnet serves the full management handler (server.Handler()).
	Tailnet http.Handler
}

// Options parameterizes Start. The zero value is production behavior.
type Options struct {
	// NewNode constructs the node; tests inject a fake. nil uses the real
	// tsnet implementation.
	NewNode func(Config) (Node, error)
	// StartAttempts bounds the boot retry (default DefaultStartAttempts).
	StartAttempts int
	// StartDelay separates the attempts (default DefaultStartDelay).
	StartDelay time.Duration
}

// Stack owns the embedded node and the HTTPS servers it serves.
type Stack struct {
	node    Node
	servers []*http.Server
}

// Start constructs the embedded node, connects it with a bounded retry,
// opens the configured listeners, and serves their handlers in background
// goroutines. It returns once everything is serving; any error aborts the
// caller's boot (docs/26 §3: on-mode boot is fail-fast).
func Start(ctx context.Context, cfg Config, handlers Handlers, opts Options) (*Stack, error) {
	if !cfg.Funnel && !cfg.UI {
		return nil, fmt.Errorf("tailscale: enabled with no listeners: open the funnel (%s) and/or the management listener (%s), or unset the auth key", FunnelAddr, TailnetAddr)
	}

	newNode := opts.NewNode
	if newNode == nil {
		newNode = newTSNetNode
	}
	node, err := newNode(cfg)
	if err != nil {
		return nil, fmt.Errorf("tailscale: constructing node: %w", err)
	}

	attempts := opts.StartAttempts
	if attempts <= 0 {
		attempts = DefaultStartAttempts
	}
	delay := opts.StartDelay
	if delay <= 0 {
		delay = DefaultStartDelay
	}
	for attempt := 1; ; attempt++ {
		err = node.Start()
		if err == nil {
			break
		}
		if attempt == attempts {
			return nil, fmt.Errorf("tailscale: node failed to start after %d attempts: %w", attempts, err)
		}
		logger.Warn("tailscale node failed to start, retrying",
			"attempt", attempt,
			"attempts", attempts,
			"retry_in", delay.String(),
			"err", err,
		)
		select {
		case <-ctx.Done():
			_ = node.Close()
			return nil, fmt.Errorf("tailscale: node start aborted: %w", ctx.Err())
		case <-time.After(delay):
		}
	}

	stack := &Stack{node: node}
	if err := stack.serve(cfg, handlers); err != nil {
		_ = stack.Shutdown(context.Background())
		return nil, err
	}
	return stack, nil
}

// serve opens the configured listeners and serves them. Any failure is
// fatal for the stack; the caller shuts down what already came up.
func (s *Stack) serve(cfg Config, handlers Handlers) error {
	dns := s.node.DNSName()
	if cfg.Funnel {
		if err := s.serveOne(FunnelAddr, handlers.Funnel, s.node.ListenFunnel); err != nil {
			return err
		}
		logger.Info("tailscale funnel listener serving",
			"url", "https://"+dns+FunnelAddr,
			"routes", "POST /hooks/{github,gitea,forgejo}",
		)
	}
	if cfg.UI {
		if err := s.serveOne(TailnetAddr, handlers.Tailnet, s.node.ListenTLS); err != nil {
			return err
		}
		logger.Info("tailscale management listener serving",
			"url", "https://"+dns+TailnetAddr,
		)
	}
	return nil
}

// serveOne opens one listener and serves handler on it in a goroutine.
func (s *Stack) serveOne(addr string, handler http.Handler, listen func(network, addr string) (net.Listener, error)) error {
	if handler == nil {
		return fmt.Errorf("tailscale: no handler configured for listener %s", addr)
	}
	ln, err := listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("tailscale: opening listener %s: %w", addr, err)
	}
	srv := &http.Server{Handler: handler} //nolint:gosec // read-header timeout comes from tsnet's TLS listener; the funnel serves one HMAC-verified route group and the tailnet listener is tailnet-only
	s.servers = append(s.servers, srv)
	go func() {
		if err := srv.Serve(ln); err != nil && !strings.Contains(err.Error(), "Server closed") {
			logger.Error("tailscale listener failed", "addr", addr, "err", err)
		}
	}()
	return nil
}

// Shutdown drains the HTTPS servers, then closes the node. Tailscale
// failure must never block the supervisor's core shutdown path, so callers
// treat any returned error as warn-and-continue.
func (s *Stack) Shutdown(ctx context.Context) error {
	var firstErr error
	for _, srv := range s.servers {
		if err := srv.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := s.node.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if firstErr != nil {
		return fmt.Errorf("tailscale: shutdown: %w", firstErr)
	}
	return nil
}

// tsnetNode is the real Node implementation backed by tailscale.com/tsnet.
type tsnetNode struct {
	srv *tsnet.Server
}

// newTSNetNode builds the embedded node: state under the (0700) state dir,
// tsnet's own logs routed into the supervisor logger so enrollment and
// boot problems are visible locally (docs/26 §5).
func newTSNetNode(cfg Config) (Node, error) {
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating state dir %q: %w", cfg.StateDir, err)
	}
	logf := func(format string, args ...any) {
		logger.Info(strings.TrimSpace(fmt.Sprintf(format, args...)))
	}
	return &tsnetNode{
		srv: &tsnet.Server{
			Dir:      cfg.StateDir,
			Hostname: cfg.Hostname,
			AuthKey:  cfg.AuthKey,
			UserLogf: logf,
			Logf:     logf,
		},
	}, nil
}

func (n *tsnetNode) Start() error { return n.srv.Start() }

func (n *tsnetNode) ListenFunnel(network, addr string) (net.Listener, error) {
	return n.srv.ListenFunnel(network, addr, tsnet.FunnelOnly())
}

func (n *tsnetNode) ListenTLS(network, addr string) (net.Listener, error) {
	return n.srv.ListenTLS(network, addr)
}

// DNSName reports the node's first certificate DNS name
// (<hostname>.<tailnet>.ts.net), or "" before the node is online.
func (n *tsnetNode) DNSName() string {
	if domains := n.srv.CertDomains(); len(domains) > 0 {
		return domains[0]
	}
	return ""
}

func (n *tsnetNode) Close() error { return n.srv.Close() }
