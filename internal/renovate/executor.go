package renovate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/noosxe/runnero/internal/cron"
	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
)

var (
	// ErrNilDB is returned when an executor is instantiated without a database.
	ErrNilDB = errors.New("renovate database cannot be nil")
	// ErrNilSpawner is returned when an executor is instantiated without a container spawner.
	ErrNilSpawner = errors.New("container spawner cannot be nil")
	// ErrNilResolver is returned when an executor is instantiated without a git provider resolver.
	ErrNilResolver = errors.New("git provider resolver cannot be nil")
)

// RenovateDatabase defines the database operations required for executing and recording Renovate runs.
type RenovateDatabase interface {
	GetRunnerPoolById(ctx context.Context, id int64) (db.RunnerPool, error)
	GetRenovateConfigByPoolId(ctx context.Context, poolID int64) (db.RenovateConfig, error)
	CreateRenovateRun(ctx context.Context, arg db.CreateRenovateRunParams) (db.RenovateRun, error)
	UpdateRenovateRunContainerID(ctx context.Context, arg db.UpdateRenovateRunContainerIDParams) error
	CompleteRenovateRun(ctx context.Context, arg db.CompleteRenovateRunParams) (db.RenovateRun, error)
	CompleteRenovateRunByContainerID(ctx context.Context, arg db.CompleteRenovateRunByContainerIDParams) (db.RenovateRun, error)
	GetRenovateRunByContainerID(ctx context.Context, containerID sql.NullString) (db.RenovateRun, error)
	GetLatestRenovateRunByPoolId(ctx context.Context, poolID int64) (db.RenovateRun, error)
	ListRenovateRunsByPoolId(ctx context.Context, arg db.ListRenovateRunsByPoolIdParams) ([]db.RenovateRun, error)
	CountRenovateRunsByPoolId(ctx context.Context, poolID int64) (int64, error)
}

// GitProviderResolver resolves a GitProvider instance given an auth profile ID.
type GitProviderResolver interface {
	ResolveProvider(ctx context.Context, authProfileID int64) (provider.GitProvider, error)
}

// TaskSpawner abstracts spawning one-off task containers such as Renovate.
type TaskSpawner interface {
	SpawnTask(ctx context.Context, config orchestrator.RunnerConfig) (string, error)
}

// ExecutorOptions configures an Executor.
type ExecutorOptions struct {
	DB        RenovateDatabase
	Providers GitProviderResolver
	Spawner   TaskSpawner
	DataDir   string
	Logger    *slog.Logger
}

// Executor manages Renovate bot task lifecycle: token acquisition, task container spawning,
// reap handling, and execution history recording (docs/03 §5, docs/05 §2).
type Executor struct {
	db        RenovateDatabase
	providers GitProviderResolver
	spawner   TaskSpawner
	dataDir   string
	logger    *slog.Logger
}

// NewExecutor creates a new Renovate task executor.
func NewExecutor(opts ExecutorOptions) (*Executor, error) {
	if opts.DB == nil {
		return nil, ErrNilDB
	}
	if opts.Spawner == nil {
		return nil, ErrNilSpawner
	}
	if opts.Providers == nil {
		return nil, ErrNilResolver
	}
	log := opts.Logger
	if log == nil {
		log = logger
	}

	return &Executor{
		db:        opts.DB,
		providers: opts.Providers,
		spawner:   opts.Spawner,
		dataDir:   opts.DataDir,
		logger:    log,
	}, nil
}

// Execute initiates a Renovate run for the given pool ID: resolves credentials,
// obtains a short-lived token, creates a running history record, and spawns the container.
func (e *Executor) Execute(ctx context.Context, poolID int64) (*db.RenovateRun, error) {
	e.logger.Info("starting renovate task execution", "pool_id", poolID)

	pool, err := e.db.GetRunnerPoolById(ctx, poolID)
	if err != nil {
		return nil, fmt.Errorf("getting pool %d: %w", poolID, err)
	}

	renovateConfig, err := e.db.GetRenovateConfigByPoolId(ctx, poolID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("getting renovate config for pool %d: %w", poolID, err)
	}

	image := "renovate/renovate:latest"
	if renovateConfig.Image != "" {
		image = renovateConfig.Image
	}

	prov, err := e.providers.ResolveProvider(ctx, pool.AuthProfileID)
	if err != nil {
		return nil, fmt.Errorf("resolving git provider for pool %d: %w", poolID, err)
	}

	var token string
	if rtp, ok := prov.(provider.RenovateTokenProvider); ok {
		token, err = rtp.GetRenovateToken(ctx, pool.RepositoryUrl)
	} else {
		token, err = prov.GetRegistrationToken(ctx, provider.ScopeRepo, pool.RepositoryUrl)
	}
	if err != nil {
		return nil, fmt.Errorf("retrieving renovate token for pool %d: %w", poolID, err)
	}

	now := time.Now().UTC()
	run, err := e.db.CreateRenovateRun(ctx, db.CreateRenovateRunParams{
		PoolID:    poolID,
		Status:    "running",
		StartedAt: now,
		Summary:   "Renovate task running",
		ContainerID: sql.NullString{
			String: "",
			Valid:  false,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("recording renovate run in database: %w", err)
	}

	containerName := orchestrator.GenerateContainerName(pool.Name)
	taskConfig := orchestrator.RunnerConfig{
		Name:        containerName,
		PoolName:    pool.Name,
		RepoURL:     pool.RepositoryUrl,
		Token:       token,
		Image:       image,
		AllowDocker: pool.AllowDocker,
	}
	if pool.CpuLimit.Valid {
		taskConfig.CPULimit = pool.CpuLimit.String
	}
	if pool.MemoryLimit.Valid {
		taskConfig.MemoryLimit = pool.MemoryLimit.String
	}

	containerID, err := e.spawner.SpawnTask(ctx, taskConfig)
	if err != nil {
		completeTime := time.Now().UTC()
		_, _ = e.db.CompleteRenovateRun(ctx, db.CompleteRenovateRunParams{
			ID:          run.ID,
			Status:      "failure",
			CompletedAt: sql.NullTime{Time: completeTime, Valid: true},
			Summary:     fmt.Sprintf("Failed to spawn container: %v", err),
		})
		return nil, fmt.Errorf("spawning renovate task container: %w", err)
	}

	if err := e.db.UpdateRenovateRunContainerID(ctx, db.UpdateRenovateRunContainerIDParams{
		ID:          run.ID,
		ContainerID: sql.NullString{String: containerID, Valid: true},
	}); err != nil {
		e.logger.Error("failed to associate container ID with renovate run", "run_id", run.ID, "container_id", containerID, "err", err)
	}

	run.ContainerID = sql.NullString{String: containerID, Valid: true}
	e.logger.Info("renovate task spawned successfully", "pool_id", poolID, "run_id", run.ID, "container_id", containerID)
	return &run, nil
}

// HandleContainerExit processes a container termination event. If containerID corresponds to an active
// Renovate run, it updates the run record with final status, completion timestamp, and parsed summary.
// Returns true if the container belonged to a Renovate run, false otherwise.
func (e *Executor) HandleContainerExit(ctx context.Context, containerID string, exitCode int, logPath string) (bool, error) {
	if containerID == "" {
		return false, nil
	}

	run, err := e.db.GetRenovateRunByContainerID(ctx, sql.NullString{String: containerID, Valid: true})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil // Not a Renovate run
		}
		return false, fmt.Errorf("querying renovate run for container %s: %w", containerID, err)
	}

	if run.Status != "running" {
		return true, nil // Already marked completed
	}

	var entries []orchestrator.LogEntry
	if logPath != "" {
		if parsed, err := orchestrator.ReadGzippedJSONLLogs(logPath); err == nil {
			entries = parsed
		} else {
			e.logger.Debug("could not read compressed logs for summary extraction", "path", logPath, "err", err)
		}
	}

	status := "success"
	if exitCode != 0 {
		status = "failure"
	}
	summary := ExtractSummary(entries, exitCode)
	now := time.Now().UTC()

	_, err = e.db.CompleteRenovateRun(ctx, db.CompleteRenovateRunParams{
		ID:          run.ID,
		Status:      status,
		CompletedAt: sql.NullTime{Time: now, Valid: true},
		Summary:     summary,
	})
	if err != nil {
		return true, fmt.Errorf("completing renovate run %d: %w", run.ID, err)
	}

	e.logger.Info("renovate run completed",
		"pool_id", run.PoolID,
		"run_id", run.ID,
		"status", status,
		"exit_code", exitCode,
		"summary", summary,
	)

	return true, nil
}

// TaskFactory creates a TaskFunc generator for internal/cron scheduler registration.
func (e *Executor) TaskFactory() func(poolID int64, image string) cron.TaskFunc {
	return func(poolID int64, image string) cron.TaskFunc {
		return func(ctx context.Context) error {
			e.logger.Info("cron schedule triggered renovate execution", "pool_id", poolID)
			_, err := e.Execute(ctx, poolID)
			return err
		}
	}
}

// LastRunResolver returns a function resolving the most recent Renovate run timestamp for a pool.
func (e *Executor) LastRunResolver() func(ctx context.Context, poolID int64) time.Time {
	return func(ctx context.Context, poolID int64) time.Time {
		latest, err := e.db.GetLatestRenovateRunByPoolId(ctx, poolID)
		if err != nil {
			return time.Time{}
		}
		return latest.StartedAt
	}
}

// ExtractSummary inspects captured container logs and exit code to formulate a human-readable summary
// of Renovate activity (PRs created, failures, or no-op).
func ExtractSummary(entries []orchestrator.LogEntry, exitCode int) string {
	if exitCode != 0 {
		for i := len(entries) - 1; i >= 0; i-- {
			content := strings.TrimSpace(entries[i].Content)
			if content == "" {
				continue
			}
			lower := strings.ToLower(content)
			if strings.Contains(lower, "error") || strings.Contains(lower, "fatal") || strings.Contains(lower, "failed") {
				if len(content) > 140 {
					content = content[:137] + "..."
				}
				return fmt.Sprintf("Renovate failed (exit code %d): %s", exitCode, content)
			}
		}
		return fmt.Sprintf("Renovate failed with exit code %d", exitCode)
	}

	var prCount int
	for _, entry := range entries {
		line := strings.TrimSpace(entry.Content)
		if strings.Contains(line, "Created PR") ||
			strings.Contains(line, "Created pull request") ||
			strings.Contains(line, "Branch created") ||
			strings.Contains(line, "create PR") {
			prCount++
		}
	}

	if prCount > 0 {
		return fmt.Sprintf("Renovate created %d pull request(s)", prCount)
	}

	return "Renovate completed successfully (no pull requests needed)"
}
