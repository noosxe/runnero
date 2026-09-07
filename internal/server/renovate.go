package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
)

// RenovateDatabase defines the database operations required by RenovateService.
type RenovateDatabase interface {
	GetRunnerPoolById(ctx context.Context, id int64) (db.RunnerPool, error)
	GetRenovateConfigByPoolId(ctx context.Context, poolID int64) (db.RenovateConfig, error)
	GetLatestRenovateRunByPoolId(ctx context.Context, poolID int64) (db.RenovateRun, error)
	ListRenovateRunsByPoolId(ctx context.Context, arg db.ListRenovateRunsByPoolIdParams) ([]db.RenovateRun, error)
	CountRenovateRunsByPoolId(ctx context.Context, poolID int64) (int64, error)
	CreateAuditLog(ctx context.Context, arg db.CreateAuditLogParams) (db.AuditLog, error)
}

// RenovateService implements supervisorv1connect.RenovateServiceHandler (docs/03 §5, docs/08, OQ #18).
type RenovateService struct {
	supervisorv1connect.UnimplementedRenovateServiceHandler
	db       RenovateDatabase
	executor RenovateExecutor
	cron     CronScheduler
}

// NewRenovateService creates a new RenovateService.
func NewRenovateService(database RenovateDatabase, executor RenovateExecutor, cron CronScheduler) *RenovateService {
	return &RenovateService{
		db:       database,
		executor: executor,
		cron:     cron,
	}
}

// TriggerRenovateRun handles manual on-demand execution of Renovate for a pool.
func (s *RenovateService) TriggerRenovateRun(
	ctx context.Context,
	req *connect.Request[supervisorv1.TriggerRenovateRunRequest],
) (*connect.Response[supervisorv1.TriggerRenovateRunResponse], error) {
	if req.Msg == nil || req.Msg.PoolId <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("pool_id is required"))
	}
	poolID := req.Msg.PoolId

	if _, err := s.db.GetRunnerPoolById(ctx, poolID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("pool %d not found", poolID))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("retrieving pool %d: %w", poolID, err))
	}

	if s.executor == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("renovate executor is not configured"))
	}

	// Guard against concurrent executions for the same repository/pool
	if latest, err := s.db.GetLatestRenovateRunByPoolId(ctx, poolID); err == nil && latest.Status == "running" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("a renovate run is already in progress for this pool"))
	}

	run, err := s.executor.Execute(ctx, poolID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("triggering renovate run: %w", err))
	}

	recordAuditLog(ctx, s.db, "renovate.trigger", "runner_pool", &poolID, map[string]any{
		"run_id":  run.ID,
		"pool_id": poolID,
	})

	return connect.NewResponse(&supervisorv1.TriggerRenovateRunResponse{
		Success: true,
		RunId:   run.ID,
	}), nil
}

// GetRenovateStatus returns the current/latest execution status and next scheduled cron run for a pool.
func (s *RenovateService) GetRenovateStatus(
	ctx context.Context,
	req *connect.Request[supervisorv1.GetRenovateStatusRequest],
) (*connect.Response[supervisorv1.GetRenovateStatusResponse], error) {
	if req.Msg == nil || req.Msg.PoolId <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("pool_id is required"))
	}
	poolID := req.Msg.PoolId

	if _, err := s.db.GetRunnerPoolById(ctx, poolID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("pool %d not found", poolID))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("retrieving pool %d: %w", poolID, err))
	}

	var lastRun *supervisorv1.RenovateRun
	latest, err := s.db.GetLatestRenovateRunByPoolId(ctx, poolID)
	if err == nil {
		lastRun = &supervisorv1.RenovateRun{
			Id:        latest.ID,
			PoolId:    latest.PoolID,
			Status:    latest.Status,
			StartedAt: latest.StartedAt.Format(time.RFC3339),
			Summary:   latest.Summary,
		}
		if latest.CompletedAt.Valid {
			lastRun.CompletedAt = latest.CompletedAt.Time.Format(time.RFC3339)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("retrieving latest renovate run: %w", err))
	}

	var nextScheduledRun string
	if s.cron != nil {
		if next, err := s.cron.NextRun(poolID); err == nil && !next.IsZero() {
			nextScheduledRun = next.UTC().Format(time.RFC3339)
		}
	}

	return connect.NewResponse(&supervisorv1.GetRenovateStatusResponse{
		LastRun:          lastRun,
		NextScheduledRun: nextScheduledRun,
	}), nil
}

// ListRenovateHistory retrieves paginated historical Renovate executions for a pool.
func (s *RenovateService) ListRenovateHistory(
	ctx context.Context,
	req *connect.Request[supervisorv1.ListRenovateHistoryRequest],
) (*connect.Response[supervisorv1.ListRenovateHistoryResponse], error) {
	if req.Msg == nil || req.Msg.PoolId <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("pool_id is required"))
	}
	poolID := req.Msg.PoolId

	if _, err := s.db.GetRunnerPoolById(ctx, poolID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("pool %d not found", poolID))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("retrieving pool %d: %w", poolID, err))
	}

	limit := req.Msg.Limit
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}
	offset := req.Msg.Offset
	if offset < 0 {
		offset = 0
	}

	runs, err := s.db.ListRenovateRunsByPoolId(ctx, db.ListRenovateRunsByPoolIdParams{
		PoolID: poolID,
		Limit:  int64(limit),
		Offset: int64(offset),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing renovate runs: %w", err))
	}

	totalCount, err := s.db.CountRenovateRunsByPoolId(ctx, poolID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("counting renovate runs: %w", err))
	}

	protoRuns := make([]*supervisorv1.RenovateRun, 0, len(runs))
	for _, r := range runs {
		pr := &supervisorv1.RenovateRun{
			Id:        r.ID,
			PoolId:    r.PoolID,
			Status:    r.Status,
			StartedAt: r.StartedAt.Format(time.RFC3339),
			Summary:   r.Summary,
		}
		if r.CompletedAt.Valid {
			pr.CompletedAt = r.CompletedAt.Time.Format(time.RFC3339)
		}
		protoRuns = append(protoRuns, pr)
	}

	return connect.NewResponse(&supervisorv1.ListRenovateHistoryResponse{
		Runs:       protoRuns,
		TotalCount: int32(totalCount),
	}), nil
}
