package tailscale

import (
	"context"
	"net/http"
	"strings"
)

// WebhookHandler mirrors internal/server.WebhookHandler (structural
// interface; *webhook.Receiver satisfies both): everything the funnel
// listener needs of the webhook receiver without importing the server
// package.
type WebhookHandler interface {
	Handle(ctx context.Context, provider string, req *http.Request, w http.ResponseWriter)
}

// FunnelHandler serves the public funnel listener: the webhook route group
// and nothing else (docs/26 §3). With a receiver, POST /hooks/{provider}
// delegates to it verbatim (signed → 202, ping → 200, tampered/missing →
// 401, unconfigured provider → 500, unsupported provider → 400 — RUN-153
// semantics). With a nil receiver (no secrets configured) the route group
// answers 405, mirroring the unmounted LAN route. Every other path and
// method answers 404: no SPA, no API, no cookies, no health.
func FunnelHandler(receiver WebhookHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if provider, ok := webhookRoute(r); ok {
			if receiver != nil {
				receiver.Handle(r.Context(), provider, r, w)
				return
			}
			// Route group unmounted (no webhook secrets configured): the
			// listener only ever answers 405/404 (docs/26 §3 boot note).
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		http.NotFound(w, r)
	})
}

// webhookRoute reports whether the request is a POST on the
// /hooks/{provider} route group, returning the provider segment. The
// segment is not filtered here: unsupported providers are the receiver's
// business (it answers 400, RUN-153 semantics), while non-hook paths and
// methods fall through to 404.
func webhookRoute(r *http.Request) (string, bool) {
	if r.Method != http.MethodPost {
		return "", false
	}
	const prefix = "/hooks/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return "", false
	}
	provider := strings.TrimPrefix(r.URL.Path, prefix)
	if provider == "" || strings.Contains(provider, "/") {
		return "", false
	}
	return provider, true
}
