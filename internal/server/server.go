package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v5"
	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/web"
)

// DisabledJSONCodec explicitly overrides the default "json" codec in Connect,
// enforcing that JSON payloads are rejected because binary protocol is mandatory (docs/06 §1).
type DisabledJSONCodec struct{}

func (d DisabledJSONCodec) Name() string { return "json" }
func (d DisabledJSONCodec) Marshal(any) ([]byte, error) {
	return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("binary protocol mandatory: JSON transport is disabled per docs/06 §1"))
}
func (d DisabledJSONCodec) Unmarshal([]byte, any) error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New("binary protocol mandatory: JSON transport is disabled per docs/06 §1"))
}

// BinaryProtocolInterceptor inspects Connect requests to ensure no JSON content-type is permitted.
func BinaryProtocolInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ct := req.Header().Get("Content-Type")
			if strings.Contains(ct, "json") {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("binary protocol mandatory: JSON transport is disabled per docs/06 §1"))
			}
			return next(ctx, req)
		}
	}
}

// BinaryConnectHandlerOptions returns the standard Connect HandlerOptions enforcing
// mandatory binary protocol transport (docs/06 §1).
func BinaryConnectHandlerOptions() []connect.HandlerOption {
	return []connect.HandlerOption{
		connect.WithCodec(DisabledJSONCodec{}),
		connect.WithInterceptors(connect.UnaryInterceptorFunc(BinaryProtocolInterceptor())),
	}
}

// Options configures New.
type Options struct {
	// Port is the TCP port the HTTP server binds on (config.Port /
	// SUPERVISOR_PORT; 1..65535, enforced by internal/config validation).
	Port int

	// Health is the probe registry served by /healthz and /readyz. nil
	// creates an empty registry that components populate as they land:
	// the DB probe in M2, Docker and auditor probes in M5, the control
	// loop in M6 (OQ #19, RUN-10).
	Health *Health

	// StaticFS is the filesystem containing built SPA assets (index.html, js, css).
	// If nil, defaults to the embedded web.Dist() filesystem (docs/06 §2, RUN-44).
	StaticFS fs.FS

	// AuthDB is the database interface used for administrator authentication,
	// sessions, and audit logs. If nil, AuthService is not automatically mounted.
	AuthDB AuthDatabase

	// PoolDB is the database interface used for runner pools and audit logs.
	// If nil, PoolService is not automatically mounted.
	PoolDB PoolDatabase

	// PoolStats provides live runtime runner counts and reload capabilities (RUN-46).
	PoolStats PoolStatsProvider

	// RunnerMgr provides live runner container inspection and manual termination (RUN-58).
	RunnerMgr RunnerManager

	// AuthProfileDB is the database interface used for auth profiles.
	// If nil, AuthProfileService is not automatically mounted.
	AuthProfileDB AuthProfileDatabase

	// DBEncryptionKey is the 256-bit AES key derived from SUPERVISOR_DB_ENCRYPTION_KEY
	// used to encrypt auth profile secrets at rest (docs/05 §5, keys.LabelDBEncryption).
	DBEncryptionKey []byte

	// CredentialValidator optionally validates credentials against upstream providers on profile create.
	CredentialValidator CredentialValidator

	// OnboardingDB is the database interface used for onboarding checks and app settings.
	// If nil, OnboardingService is not automatically mounted.
	OnboardingDB OnboardingDatabase

	// AnalyticsDB is the database interface used for job history and metrics.
	// If nil, AnalyticsService is not automatically mounted.
	AnalyticsDB AnalyticsDatabase

	// ImageUpdateDB is the database interface used for runner pool image updates.
	// If nil and PoolDB is provided, PoolDB is used as ImageUpdateDB.
	ImageUpdateDB ImageUpdateDatabase

	// ImagePuller is the container image puller interface.
	ImagePuller ImagePuller

	// LocalImageInspector inspects local image digests on the container host (M10, RUN-66).
	LocalImageInspector LocalImageInspector

	// RegistryChecker queries remote OCI/Docker registries for latest digests (M10, RUN-66).
	RegistryChecker RegistryChecker

	// SystemStats provides live system-wide runner counts (RUN-48).
	SystemStats SystemStatsProvider

	// DataDir is the base directory where DATA_DIR/logs/ are located (RUN-49).
	DataDir string

	// LogStreamer is the provider for streaming live container logs (RUN-49).
	LogStreamer LogStreamer

	// JWTSigningSecret is the 256-bit HMAC key derived from SUPERVISOR_DB_ENCRYPTION_KEY
	// used to cryptographically sign session JWT tokens (docs/05 §5, keys.LabelJWTSigning).
	JWTSigningSecret []byte

	// IsSecureCookie sets the Secure attribute on the session cookie. Defaults to false
	// for local development/testing without TLS, set to true behind HTTPS.
	IsSecureCookie bool

	// CronScheduler provides scheduled task status and next-run queries (docs/03 §5, RUN-63, RUN-65).
	CronScheduler CronScheduler

	// RenovateExecutor handles Renovate bot task execution (docs/03 §5, RUN-64, RUN-65).
	RenovateExecutor RenovateExecutor

	// RenovateDB is the database interface used for Renovate runs and configurations (RUN-65).
	// If nil and PoolDB is provided, PoolDB is used as RenovateDB.
	RenovateDB RenovateDatabase

	// WebhookReceiver processes POST /hooks/{provider} incoming webhooks (M11, RUN-68).
	WebhookReceiver WebhookHandler
}

// WebhookHandler handles incoming POST /hooks/{provider} requests (M11, RUN-68).
type WebhookHandler interface {
	Handle(ctx context.Context, provider string, req *http.Request, w http.ResponseWriter)
}

// CronScheduler provides status and scheduling queries for scheduled tasks (docs/03 §5).
type CronScheduler interface {
	NextRun(poolID int64) (time.Time, error)
}

// RenovateExecutor handles manual and scheduled executions of Renovate bot tasks (docs/03 §5, RUN-64, RUN-65).
type RenovateExecutor interface {
	Execute(ctx context.Context, poolID int64) (*db.RenovateRun, error)
	HandleContainerExit(ctx context.Context, containerID string, exitCode int, logPath string) (bool, error)
}

// Server is the supervisor's HTTP server: an Echo v5 instance (docs/06 §1)
// that serves health endpoints, ConnectRPC services (binary transport mandatory),
// and the embedded SPA.
type Server struct {
	echo             *echo.Echo
	http             *http.Server
	health           *Health
	staticFS         fs.FS
	authDB           AuthDatabase
	jwtSecret        []byte
	webhookReceiver  WebhookHandler
	cronScheduler    CronScheduler
	renovateExecutor RenovateExecutor
	imageUpdateSvc   *ImageUpdateService
}

// New builds the server and its routes. Construction is infallible: routes
// are static, and bind failures surface from Start.
func New(opts Options) *Server {
	// Echo v5 logs through native slog; route it through this package's
	// module logger so its records carry module="server" like every other
	// supervisor component (RUN-8).
	e := echo.New()
	e.Logger = logger

	s := &Server{
		echo:             e,
		health:           opts.Health,
		staticFS:         opts.StaticFS,
		authDB:           opts.AuthDB,
		jwtSecret:        opts.JWTSigningSecret,
		webhookReceiver:  opts.WebhookReceiver,
		cronScheduler:    opts.CronScheduler,
		renovateExecutor: opts.RenovateExecutor,
	}
	if s.health == nil {
		s.health = NewHealth()
	}
	if s.staticFS == nil {
		if dist, err := web.Dist(); err == nil {
			s.staticFS = dist
		}
	}

	// Middleware: enforce HTTP security headers and deny CORS by default (RUN-50, OQ #25, #26)
	e.Use(s.securityMiddleware)

	// Middleware: enforce binary protocol on Connect RPC routes
	e.Use(s.enforceBinaryTransportMiddleware)

	e.GET("/healthz", s.handleHealthz)
	e.GET("/readyz", s.handleReadyz)

	// Mount AuthService if database and secret are provided (RUN-45)
	if s.authDB != nil && len(s.jwtSecret) > 0 {
		authSvc := NewAuthService(s.authDB, s.jwtSecret, opts.IsSecureCookie)
		path, handler := supervisorv1connect.NewAuthServiceHandler(authSvc, s.ConnectHandlerOptions()...)
		s.MountConnectHandler(path, handler)
	}

	// Mount PoolService if pool database is provided (RUN-46)
	if opts.PoolDB != nil {
		poolSvc := NewPoolService(opts.PoolDB, opts.PoolStats, opts.RunnerMgr)
		path, handler := supervisorv1connect.NewPoolServiceHandler(poolSvc, s.ConnectHandlerOptions()...)
		s.MountConnectHandler(path, handler)
	}

	// Mount AuthProfileService if auth profile database is provided (RUN-47)
	if opts.AuthProfileDB != nil {
		authProfileSvc := NewAuthProfileService(opts.AuthProfileDB, opts.DBEncryptionKey, opts.CredentialValidator)
		path, handler := supervisorv1connect.NewAuthProfileServiceHandler(authProfileSvc, s.ConnectHandlerOptions()...)
		s.MountConnectHandler(path, handler)
	}

	// Mount OnboardingService if onboarding database is provided (RUN-48)
	if opts.OnboardingDB != nil {
		onboardingSvc := NewOnboardingService(opts.OnboardingDB)
		path, handler := supervisorv1connect.NewOnboardingServiceHandler(onboardingSvc, s.ConnectHandlerOptions()...)
		s.MountConnectHandler(path, handler)
	}

	// Mount AnalyticsService if analytics database is provided (RUN-48)
	if opts.AnalyticsDB != nil {
		analyticsSvc := NewAnalyticsService(opts.AnalyticsDB, opts.SystemStats, opts.PoolStats)
		path, handler := supervisorv1connect.NewAnalyticsServiceHandler(analyticsSvc, s.ConnectHandlerOptions()...)
		s.MountConnectHandler(path, handler)
	}

	// Mount ImageUpdateService if image update database is available (RUN-62, RUN-66)
	imgDB := opts.ImageUpdateDB
	if imgDB == nil && opts.PoolDB != nil {
		imgDB = opts.PoolDB
	}
	if imgDB != nil {
		var imgOpts []ImageUpdateOption
		if opts.LocalImageInspector != nil {
			imgOpts = append(imgOpts, WithLocalInspector(opts.LocalImageInspector))
		}
		if opts.RegistryChecker != nil {
			imgOpts = append(imgOpts, WithRegistryChecker(opts.RegistryChecker))
		}
		imgSvc := NewImageUpdateService(imgDB, opts.ImagePuller, imgOpts...)
		s.imageUpdateSvc = imgSvc
		path, handler := supervisorv1connect.NewImageUpdateServiceHandler(imgSvc, s.ConnectHandlerOptions()...)
		s.MountConnectHandler(path, handler)
	}

	// Mount LogService if DataDir or LogStreamer is provided (RUN-49)
	if opts.DataDir != "" || opts.LogStreamer != nil {
		logSvc := NewLogService(opts.DataDir, opts.LogStreamer)
		path, handler := supervisorv1connect.NewLogServiceHandler(logSvc, s.ConnectHandlerOptions()...)
		s.MountConnectHandler(path, handler)
	}

	// Mount RenovateService if RenovateDB or PoolDB is provided (RUN-65)
	renovateDB := opts.RenovateDB
	if renovateDB == nil && opts.PoolDB != nil {
		if rdb, ok := opts.PoolDB.(RenovateDatabase); ok {
			renovateDB = rdb
		}
	}
	if renovateDB != nil {
		renovateSvc := NewRenovateService(renovateDB, opts.RenovateExecutor, opts.CronScheduler)
		path, handler := supervisorv1connect.NewRenovateServiceHandler(renovateSvc, s.ConnectHandlerOptions()...)
		s.MountConnectHandler(path, handler)
	}

	// Mount WebhookReceiver if provided (M11, RUN-68)
	if s.webhookReceiver != nil {
		e.POST("/hooks/:provider", s.handleWebhook)
	}

	// SPA fallback routes (catch-all GET/HEAD, falls back to index.html)
	e.GET("/*", s.serveSPA)
	e.HEAD("/*", s.serveSPA)

	s.http = &http.Server{
		Addr:    fmt.Sprintf(":%d", opts.Port),
		Handler: e,
	}
	return s
}

// ConnectHandlerOptions returns the standard Connect HandlerOptions enforcing binary transport
// and (if configured) authentication interception on protected RPCs.
func (s *Server) ConnectHandlerOptions() []connect.HandlerOption {
	opts := BinaryConnectHandlerOptions()
	if s.authDB != nil && len(s.jwtSecret) > 0 {
		opts = append(opts, connect.WithInterceptors(NewAuthInterceptor(s.authDB, s.jwtSecret)))
	}
	return opts
}

// Health exposes the probe registry so the daemon (and later milestones)
// register their checks without holding a second reference.
func (s *Server) Health() *Health { return s.health }

// Handler exposes the routing http.Handler. Tests drive the endpoints
// through it without binding a port.
func (s *Server) Handler() http.Handler { return s.echo }

// Start binds the configured port and serves until Shutdown is called. It
// blocks; callers run it in its own goroutine. A clean shutdown yields a
// nil return, anything else (port in use, permission denied) the error.
func (s *Server) Start() error {
	logger.Info("http server listening", "addr", s.http.Addr)
	if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully drains in-flight requests. The ctx bounds the drain
// window; after it expires Shutdown returns ctx.Err() and the caller
// decides whether to force-quit.
func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.http.Shutdown(ctx); err != nil {
		return err
	}
	logger.Info("http server stopped")
	return nil
}

// handleHealthz serves GET /healthz: liveness. The response itself proves
// the process is alive; the registered liveness probes (DB once M2 lands)
// decide the body. Unhealthy answers with 503 so orchestrators restart the
// supervisor rather than routing to it.
func (s *Server) handleHealthz(c *echo.Context) error {
	report := s.health.LivenessReport(c.Request().Context())
	return c.JSON(reportHTTPStatus(report.Status, statusHealthy), report)
}

// handleReadyz serves GET /readyz: readiness. Failed probes answer 503
// (do not route work here yet); degraded probes — the Docker socket being
// down (OQ #19) — keep the endpoint ready and merely flag the check.
func (s *Server) handleReadyz(c *echo.Context) error {
	report := s.health.ReadinessReport(c.Request().Context())
	return c.JSON(reportHTTPStatus(report.Status, statusReady), report)
}

// reportHTTPStatus maps an aggregate status to its response code: 200 when
// the endpoint is satisfied, 503 otherwise.
func reportHTTPStatus(status, okStatus string) int {
	if status != okStatus {
		return http.StatusServiceUnavailable
	}
	return http.StatusOK
}

// Echo returns the underlying Echo instance.
func (s *Server) Echo() *echo.Echo {
	return s.echo
}

// MountConnectHandler registers a ConnectRPC service handler on Echo.
func (s *Server) MountConnectHandler(pattern string, handler http.Handler) {
	prefix := strings.TrimSuffix(pattern, "/")
	wrapped := echo.WrapHandler(handler)
	s.echo.Any(prefix, wrapped)
	s.echo.Any(prefix+"/*", wrapped)
}

func (s *Server) serveSPA(c *echo.Context) error {
	if s.staticFS == nil {
		return c.String(http.StatusNotFound, "frontend assets not found")
	}

	reqPath := strings.TrimPrefix(c.Request().URL.Path, "/")
	// Never serve SPA index.html for API / Connect RPC / webhook endpoints
	if strings.HasPrefix(reqPath, "supervisor.v1.") || strings.HasPrefix(reqPath, "healthz") || strings.HasPrefix(reqPath, "readyz") || strings.HasPrefix(reqPath, "hooks") {
		return c.String(http.StatusNotFound, "not found")
	}

	if reqPath == "" {
		reqPath = "index.html"
	}

	// If the file exists in staticFS, serve it directly
	if f, err := s.staticFS.Open(reqPath); err == nil {
		_ = f.Close()
		return c.FileFS(reqPath, s.staticFS)
	}

	// Fallback to index.html for client-side routing (TanStack Router)
	return c.FileFS("index.html", s.staticFS)
}

func (s *Server) enforceBinaryTransportMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		path := c.Request().URL.Path
		if strings.HasPrefix(path, "/supervisor.v1.") {
			ct := c.Request().Header.Get("Content-Type")
			if strings.Contains(ct, "json") {
				return c.JSON(http.StatusUnsupportedMediaType, map[string]string{
					"code":    "invalid_argument",
					"message": "binary protocol mandatory: JSON transport is disabled per docs/06 §1",
				})
			}
		}
		return next(c)
	}
}

// Security header constants (RUN-50, OQ #25, #26).
const (
	HeaderXFrameOptions       = "X-Frame-Options"
	HeaderXContentTypeOptions = "X-Content-Type-Options"
	HeaderCSP                 = "Content-Security-Policy"
	HeaderReferrerPolicy      = "Referrer-Policy"

	ValueXFrameOptions       = "DENY"
	ValueXContentTypeOptions = "nosniff"
	// The script-src hash allowlists exactly one inline script: the pre-paint
	// theme bootstrap in web/index.html (it must run before first render to
	// avoid a light flash for dark-theme users, so it cannot be an external
	// file). If you edit that script, the hash changes and the browser will
	// block it while printing the new expected value in devtools — update it
	// here in the same commit.
	ValueCSP            = "default-src 'self'; script-src 'self' 'sha256-/bRTsAHXsuyQc/IEeJa0n8pJe74NAM94NwdrqChr4lk='; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none';"
	ValueReferrerPolicy = "strict-origin-when-cross-origin"
)

// securityMiddleware sets standard HTTP security headers and strictly denies CORS (same-origin only, OQ #25, #26).
func (s *Server) securityMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		req := c.Request()
		res := c.Response()

		// 1. Set mandatory security hardening headers
		res.Header().Set(HeaderXFrameOptions, ValueXFrameOptions)
		res.Header().Set(HeaderXContentTypeOptions, ValueXContentTypeOptions)
		res.Header().Set(HeaderCSP, ValueCSP)
		res.Header().Set(HeaderReferrerPolicy, ValueReferrerPolicy)

		// Bypass CORS for server-to-server webhook receiver endpoints (docs/03 §4, RUN-68)
		if strings.HasPrefix(req.URL.Path, "/hooks") {
			return next(c)
		}

		// 2. Strict CORS denial: same-origin only (SPA + API share origin)
		origin := req.Header.Get("Origin")
		if origin != "" {
			expectedHost := req.Header.Get("X-Forwarded-Host")
			if expectedHost == "" {
				expectedHost = req.Host
			}

			if !isSameOrigin(origin, expectedHost) {
				return echo.NewHTTPError(http.StatusForbidden, "cross-origin requests are forbidden")
			}
		}

		// Reject CORS preflight requests
		if req.Method == http.MethodOptions && origin != "" {
			return echo.NewHTTPError(http.StatusForbidden, "CORS preflight forbidden")
		}

		return next(c)
	}
}

func isSameOrigin(originStr, expectedHost string) bool {
	if originStr == "" {
		return true
	}
	u, err := url.Parse(originStr)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, expectedHost) {
		return true
	}

	oHostname := u.Hostname()
	eHostname := expectedHost
	if h, _, err := net.SplitHostPort(expectedHost); err == nil {
		eHostname = h
	}
	if !strings.EqualFold(oHostname, eHostname) {
		return false
	}

	oPort := u.Port()
	if oPort == "" {
		switch u.Scheme {
		case "https":
			oPort = "443"
		case "http":
			oPort = "80"
		}
	}
	_, ePort, _ := net.SplitHostPort(expectedHost)
	if ePort == "" {
		ePort = oPort
	}
	return oPort == ePort
}

// CronScheduler returns the configured cron scheduler, if any (RUN-63, RUN-65).
func (s *Server) CronScheduler() CronScheduler {
	return s.cronScheduler
}

// RenovateExecutor returns the configured renovate executor, if any (RUN-64, RUN-65).
func (s *Server) RenovateExecutor() RenovateExecutor {
	return s.renovateExecutor
}

// WebhookReceiver returns the configured webhook receiver, if any (M11, RUN-68).
func (s *Server) WebhookReceiver() WebhookHandler {
	return s.webhookReceiver
}

// ImageUpdateService returns the configured image update service, if mounted (RUN-62, RUN-66).
func (s *Server) ImageUpdateService() *ImageUpdateService {
	return s.imageUpdateSvc
}

func (s *Server) handleWebhook(c *echo.Context) error {
	provider := c.Param("provider")
	s.webhookReceiver.Handle(c.Request().Context(), provider, c.Request(), c.Response())
	return nil
}
