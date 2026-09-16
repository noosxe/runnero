package server

import (
	"net"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
)

// clientIPFromRequest resolves the client IP for rate limiting and audit
// (docs/32 §4.1): only behind a configured trusted proxy may the first
// X-Forwarded-For entry be trusted; otherwise the socket remote address is
// used and a client-supplied header can never poison the value.
func clientIPFromRequest(r *http.Request, trustedProxy bool) string {
	if trustedProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
				return first
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// requestIsSecure reports whether the request arrived over HTTPS for
// Secure=auto cookie resolution (docs/32 §3.4): direct TLS, or
// X-Forwarded-Proto: https behind a configured trusted proxy.
func requestIsSecure(r *http.Request, trustedProxy bool) bool {
	if r.TLS != nil {
		return true
	}
	return trustedProxy && r.Header.Get("X-Forwarded-Proto") == "https"
}

// requestInfoMiddleware resolves the per-request transport facts once and
// stores them in the request context, where the auth interceptor (cookies)
// and handlers (audit) read them (docs/32 §3.4, §4.1).
func (s *Server) requestInfoMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		req := c.Request()
		info := &requestInfo{
			ClientIP: clientIPFromRequest(req, s.sessionCfg.TrustedProxy),
			Secure:   requestIsSecure(req, s.sessionCfg.TrustedProxy),
		}
		c.SetRequest(req.WithContext(withRequestInfo(req.Context(), info)))
		return next(c)
	}
}
