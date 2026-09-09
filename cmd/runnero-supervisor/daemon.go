package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/noosxe/runnero/internal/cron"
	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/keys"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/orchestrator/docker"
	"github.com/noosxe/runnero/internal/provider"
	_ "github.com/noosxe/runnero/internal/provider/forgejo"
	_ "github.com/noosxe/runnero/internal/provider/gitea"
	_ "github.com/noosxe/runnero/internal/provider/github"
	"github.com/noosxe/runnero/internal/registry"
	"github.com/noosxe/runnero/internal/renovate"
	"github.com/noosxe/runnero/internal/server"
	"github.com/noosxe/runnero/internal/webhook"
)

// daemonShutdownTimeout bounds the HTTP drain window on SIGTERM/SIGINT:
// in-flight health requests are trivially short, and later milestones
// (M6, RUN-41) layer their own component shutdowns under the same budget.
const daemonShutdownTimeout = 10 * time.Second

func newDaemonCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the supervisor daemon (default when no subcommand is given)",
		RunE:  runDaemon,
	}
}

// runDaemon boots the supervisor daemon and blocks until SIGINT/SIGTERM.
func runDaemon(cmd *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runDaemonContext(ctx)
}

// runDaemonContext is the daemon body, parameterized by its shutdown
// context so tests drive boot and shutdown deterministically; runDaemon
// derives it from process signals.
func runDaemonContext(ctx context.Context) error {
	logger.Info("supervisor daemon starting",
		"version", version,
		"data_dir", cfg.DataDir,
		"db_path", cfg.DBPath,
		"port", cfg.Port,
	)

	derivedKeys, err := keys.Derive(cfg.DBEncryptionKey)
	if err != nil {
		return fmt.Errorf("daemon: deriving runtime keys: %w", err)
	}

	database, err := db.Open(db.Options{
		Path:          cfg.DBPath,
		EncryptionKey: derivedKeys.DBEncryptionKey,
	})
	if err != nil {
		return fmt.Errorf("daemon: database: %w", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Error("closing database", "err", err)
		}
	}()

	if err := checkAndImportSeed(ctx, database); err != nil {
		logger.Error("failed to import seed configuration on first boot", "err", err)
		return fmt.Errorf("daemon: seed import: %w", err)
	}

	backupMgr := db.NewBackupManager(database, cfg.DataDir, cfg.BackupIntervalHours, cfg.BackupRetentionCount)
	go backupMgr.Start(ctx)

	retentionScheduler := db.NewRetentionScheduler(database, nil)
	go retentionScheduler.Start(ctx)

	var dockerOpts []docker.Option
	if cfg.DockerHost != "" {
		dockerOpts = append(dockerOpts, docker.WithHost(cfg.DockerHost))
	}
	dockerClient, err := docker.NewClient(ctx, dockerOpts...)
	if err != nil {
		return fmt.Errorf("daemon: docker client: %w", err)
	}

	providerResolver := &orchestrator.RegistryAdapter{
		Database: database,
		Registry: provider.DefaultRegistry,
	}

	renovateExecutor, err := renovate.NewExecutor(renovate.ExecutorOptions{
		DB:        database,
		Providers: providerResolver,
		Spawner:   dockerClient,
		DataDir:   cfg.DataDir,
	})
	if err != nil {
		return fmt.Errorf("daemon: renovate executor: %w", err)
	}

	cronScheduler := cron.NewScheduler(cron.Options{})
	if err := cronScheduler.SyncFromDB(ctx, database, renovateExecutor.TaskFactory(), renovateExecutor.LastRunResolver()); err != nil {
		logger.Error("failed to initialize cron schedules from database", "err", err)
	}
	if err := cronScheduler.Start(ctx); err != nil {
		return fmt.Errorf("daemon: cron scheduler: %w", err)
	}
	defer cronScheduler.Stop()

	reconciler := orchestrator.NewReconciler(dockerClient)
	eventListener := orchestrator.NewEventListener(dockerClient, nil)
	poolCtrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:                database,
		JobRecorder:       database,
		ContainerEngine:   dockerClient,
		ProviderResolver:  providerResolver,
		Reconciler:        reconciler,
		EventListener:     eventListener,
		DataDir:           cfg.DataDir,
		EnrichConclusions: cfg.EnrichJobConclusions,
		TaskExitHandler:   renovateExecutor,
	})

	if err := poolCtrl.Boot(ctx); err != nil {
		logger.Warn("initial pool controller boot failed", "err", err)
	}

	go func() {
		for {
			if err := poolCtrl.Start(ctx); err != nil {
				if errors.Is(err, context.Canceled) || ctx.Err() != nil {
					return
				}
				logger.Warn("pool controller error, retrying in 5s", "err", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			} else {
				return
			}
		}
	}()

	health := server.NewHealth()
	registerHealthChecks(health, database, dockerClient, poolCtrl)

	regClient := registry.NewClient()
	serverOpts := server.Options{
		Port:                cfg.Port,
		Health:              health,
		AuthDB:              database,
		PoolDB:              database,
		PoolStats:           poolCtrl,
		RunnerMgr:           poolCtrl,
		AuthProfileDB:       database,
		OnboardingDB:        database,
		AnalyticsDB:         database,
		ImageUpdateDB:       database,
		ImagePuller:         dockerClient,
		LocalImageInspector: dockerClient,
		RegistryChecker:     regClient,
		SystemStats:         poolCtrl,
		LogStreamer:         dockerClient,
		RenovateExecutor:    renovateExecutor,
		CronScheduler:       cronScheduler,
		DataDir:             cfg.DataDir,
		DBEncryptionKey:     derivedKeys.DBEncryptionKey,
		JWTSigningSecret:    derivedKeys.JWTSigningSecret,
		IsSecureCookie:      cfg.SecureCookie,
	}

	// Webhook receiver (M11, RUN-68 / RUN-153): mount POST /hooks/{provider} only
	// when at least one provider HMAC secret is configured. Deployments without
	// secrets keep the route unmounted (POST answers 405) and demand detection stays
	// polling-only; verified events flow to the pool controller for webhook-driven
	// scaling (docs/03 §4). Rotation = restart (secrets come from the environment).
	webhookSecrets := configuredWebhookSecrets()
	if len(webhookSecrets) > 0 {
		providers := slices.Sorted(maps.Keys(webhookSecrets))
		logger.Info("webhook receiver enabled", "providers", strings.Join(providers, ", "))
		serverOpts.WebhookReceiver = webhook.NewReceiver(
			webhook.StaticSecretResolver(webhookSecrets),
			webhook.WithEventHandler(poolCtrl),
			webhook.WithLogger(logger),
		)
	}

	srv := server.New(serverOpts)

	// Start blocks, so serve from a goroutine and surface fatal errors
	// (port already bound, permission denied) through the select below.
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Start(); err != nil {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("daemon: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutdown signal received, draining http server and pool controller")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), daemonShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http server graceful shutdown error", "err", err)
	}
	if err := poolCtrl.GracefulShutdown(shutdownCtx); err != nil {
		logger.Warn("pool controller graceful shutdown error", "err", err)
	}
	logger.Info("supervisor daemon stopped")
	return nil
}

// stubCheck is a placeholder probe reporting a fixed status: it exists so
// the health endpoints answer with the full probe set from day one, while
// the milestones that own each dependency swap in real probes.
type stubCheck struct {
	name   string
	status server.Status
}

func (c stubCheck) Name() string                          { return c.name }
func (c stubCheck) Check(_ context.Context) server.Status { return c.status }

// registerHealthChecks wires the daemon's probe set (OQ #19):
// the database backs both liveness and readiness via a real SQLite ping (RUN-12);
// Docker and the auditor / control loop back readiness via reachability and heartbeat probes.
func registerHealthChecks(h *server.Health, database *db.DB, dockerClient *docker.Client, poolCtrl *orchestrator.PoolController) {
	dbCheck := server.NewCheck("db", func(ctx context.Context) server.Status {
		if database == nil || database.Ping(ctx) != nil {
			return server.StatusFail
		}
		return server.StatusOK
	})
	h.RegisterLiveness(dbCheck)
	h.RegisterReadiness(dbCheck)

	if dockerClient != nil {
		h.RegisterReadiness(dockerClient.ReadinessCheck())
	} else {
		h.RegisterReadiness(stubCheck{name: "docker", status: server.StatusOK})
	}

	if poolCtrl != nil {
		h.RegisterReadiness(poolCtrl.ReadinessCheck())
	} else {
		h.RegisterReadiness(stubCheck{name: "auditor", status: server.StatusOK})
	}
}

// checkAndImportSeed handles the first-boot YAML seed import (docs/02 §4, OQ #2):
// if the database is empty and a configuration file exists at candidate locations,
// it is imported as seed data into the database. Once evaluated or imported,
// YAML is never re-read on subsequent boots.
func checkAndImportSeed(ctx context.Context, database *db.DB) error {
	should, err := database.ShouldAutoImportSeed(ctx)
	if err != nil {
		return fmt.Errorf("evaluating seed status: %w", err)
	}
	if !should {
		return nil
	}

	var candidatePaths []string
	if cfg.ConfigFile != "" {
		candidatePaths = append(candidatePaths, cfg.ConfigFile)
	}
	candidatePaths = append(candidatePaths,
		filepath.Join(cfg.DataDir, "config.yml"),
		filepath.Join(cfg.DataDir, "config.yaml"),
		"/config.yml",
		"/config.yaml",
	)

	var seedPath string
	for _, p := range candidatePaths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			seedPath = p
			break
		}
	}

	if seedPath == "" {
		// No seed config file mounted; mark evaluated so future boots skip
		return database.MarkSeedImported(ctx)
	}

	logger.Info("first boot detected with seed configuration; importing seed data", "path", seedPath)
	data, err := os.ReadFile(seedPath)
	if err != nil {
		return fmt.Errorf("reading seed file %q: %w", seedPath, err)
	}

	seedCfg, err := db.ParseSeedConfig(data)
	if err != nil {
		return fmt.Errorf("parsing seed file %q: %w", seedPath, err)
	}

	if err := database.ImportSeedConfig(ctx, seedCfg, db.ImportModeMerge); err != nil {
		return fmt.Errorf("importing seed config from %q: %w", seedPath, err)
	}

	logger.Info("seed configuration imported successfully on first boot", "path", seedPath)
	return nil
}

// configuredWebhookSecrets collects the per-provider webhook HMAC secrets from
// the environment contract (RUN-153). Empty or whitespace-only values count as
// unconfigured; keys are the canonical lowercase /hooks/{provider} segments
// the webhook receiver validates against.
func configuredWebhookSecrets() map[string]string {
	candidates := map[string]string{
		"github":  cfg.WebhookGitHubSecret,
		"gitea":   cfg.WebhookGiteaSecret,
		"forgejo": cfg.WebhookForgejoSecret,
	}
	secrets := make(map[string]string, len(candidates))
	for provider, secret := range candidates {
		if secret = strings.TrimSpace(secret); secret != "" {
			secrets[provider] = secret
		}
	}
	return secrets
}
