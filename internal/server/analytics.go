package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
)

// AnalyticsDatabase defines the database queries required by AnalyticsService.
// *db.DB satisfies this interface.
type AnalyticsDatabase interface {
	CountJobHistory(ctx context.Context) (int64, error)
	CountJobHistoryByPoolId(ctx context.Context, poolID int64) (int64, error)
	ListJobHistory(ctx context.Context, arg db.ListJobHistoryParams) ([]db.JobHistory, error)
	ListJobHistoryByPoolId(ctx context.Context, arg db.ListJobHistoryByPoolIdParams) ([]db.JobHistory, error)
	GetJobHistoryById(ctx context.Context, id int64) (db.JobHistory, error)
	SearchJobHistory(ctx context.Context, arg db.SearchJobHistoryParams) ([]db.JobHistory, error)
	CountSearchJobHistory(ctx context.Context, arg db.CountSearchJobHistoryParams) (int64, error)
	GetJobStatsSince(ctx context.Context, createdAt time.Time) (db.GetJobStatsSinceRow, error)
	GetHourlyJobStatsSince(ctx context.Context, createdAt time.Time) ([]db.GetHourlyJobStatsSinceRow, error)
	ListRunnerPools(ctx context.Context) ([]db.RunnerPool, error)
}

// SystemStatsProvider provides live active/idle runner counts across all pools.
// *orchestrator.PoolController satisfies this interface.
type SystemStatsProvider interface {
	SystemRunnerStats() (active int32, idle int32)
}

// AnalyticsService implements supervisorv1connect.AnalyticsServiceHandler.
type AnalyticsService struct {
	supervisorv1connect.UnimplementedAnalyticsServiceHandler
	db            AnalyticsDatabase
	statsProvider SystemStatsProvider
	poolStats     PoolStatsProvider
}

// NewAnalyticsService constructs an AnalyticsService instance.
func NewAnalyticsService(database AnalyticsDatabase, statsProvider SystemStatsProvider, poolStats PoolStatsProvider) *AnalyticsService {
	return &AnalyticsService{
		db:            database,
		statsProvider: statsProvider,
		poolStats:     poolStats,
	}
}

func toFloat64(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int64:
		return float64(val)
	case int:
		return float64(val)
	case []byte:
		var f float64
		_, _ = fmt.Sscanf(string(val), "%f", &f)
		return f
	default:
		return 0
	}
}

func toInt64(v any) int64 {
	switch val := v.(type) {
	case int64:
		return val
	case int:
		return int64(val)
	case float64:
		return int64(val)
	case []byte:
		var n int64
		_, _ = fmt.Sscanf(string(val), "%d", &n)
		return n
	default:
		if v != nil {
			var n int64
			_, _ = fmt.Sscanf(fmt.Sprintf("%v", v), "%d", &n)
			return n
		}
	}
	return 0
}

func toInt32(v any) int32 {
	switch val := v.(type) {
	case int64:
		return int32(val)
	case int:
		return int32(val)
	case float64:
		return int32(val)
	case []byte:
		var n int32
		_, _ = fmt.Sscanf(string(val), "%d", &n)
		return n
	default:
		return 0
	}
}

// GetJobHistory retrieves paginated job history with optional search, status, and pool_id filters.
func (s *AnalyticsService) GetJobHistory(ctx context.Context, req *connect.Request[supervisorv1.GetJobHistoryRequest]) (*connect.Response[supervisorv1.GetJobHistoryResponse], error) {
	limit := req.Msg.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	offset := req.Msg.Offset
	if offset < 0 {
		offset = 0
	}

	search := strings.TrimSpace(req.Msg.Search)
	status := strings.TrimSpace(strings.ToLower(req.Msg.Status))
	if status == "all" {
		status = ""
	}

	totalCount, err := s.db.CountSearchJobHistory(ctx, db.CountSearchJobHistoryParams{
		PoolID: req.Msg.PoolId,
		Search: search,
		Status: status,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("counting job history: %w", err))
	}

	jobs, err := s.db.SearchJobHistory(ctx, db.SearchJobHistoryParams{
		PoolID: req.Msg.PoolId,
		Search: search,
		Status: status,
		Limit:  int64(limit),
		Offset: int64(offset),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("searching job history: %w", err))
	}

	// Lookup pool names
	poolMap := make(map[int64]string)
	if pools, err := s.db.ListRunnerPools(ctx); err == nil {
		for _, p := range pools {
			poolMap[p.ID] = p.Name
		}
	}

	resp := &supervisorv1.GetJobHistoryResponse{
		Jobs:       make([]*supervisorv1.JobRecord, 0, len(jobs)),
		TotalCount: int32(totalCount),
	}

	for _, j := range jobs {
		rec := &supervisorv1.JobRecord{
			Id:         j.ID,
			PoolId:     j.PoolID,
			RunnerName: j.RunnerName,
			Status:     j.Status,
			PoolName:   poolMap[j.PoolID],
		}
		if j.QueuedAt.Valid {
			rec.QueuedAt = j.QueuedAt.Time.Format(time.RFC3339)
		}
		if j.StartedAt.Valid {
			rec.StartedAt = j.StartedAt.Time.Format(time.RFC3339)
		}
		if j.CompletedAt.Valid {
			rec.CompletedAt = j.CompletedAt.Time.Format(time.RFC3339)
		}

		if j.QueuedAt.Valid && j.StartedAt.Valid {
			rec.QueueTimeSeconds = j.StartedAt.Time.Sub(j.QueuedAt.Time).Seconds()
			if rec.QueueTimeSeconds < 0 {
				rec.QueueTimeSeconds = 0
			}
		}

		if j.StartedAt.Valid && j.CompletedAt.Valid {
			rec.DurationSeconds = j.CompletedAt.Time.Sub(j.StartedAt.Time).Seconds()
			if rec.DurationSeconds < 0 {
				rec.DurationSeconds = 0
			}
		}

		resp.Jobs = append(resp.Jobs, rec)
	}

	return connect.NewResponse(resp), nil
}

// GetJobRecord retrieves a single job execution record by job ID.
func (s *AnalyticsService) GetJobRecord(ctx context.Context, req *connect.Request[supervisorv1.GetJobRecordRequest]) (*connect.Response[supervisorv1.GetJobRecordResponse], error) {
	if req.Msg.JobId <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("job_id must be greater than 0"))
	}
	j, err := s.db.GetJobHistoryById(ctx, req.Msg.JobId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("job id %d not found: %w", req.Msg.JobId, err))
	}

	poolName := ""
	if pools, err := s.db.ListRunnerPools(ctx); err == nil {
		for _, p := range pools {
			if p.ID == j.PoolID {
				poolName = p.Name
				break
			}
		}
	}

	rec := &supervisorv1.JobRecord{
		Id:         j.ID,
		PoolId:     j.PoolID,
		RunnerName: j.RunnerName,
		Status:     j.Status,
		PoolName:   poolName,
	}
	if j.QueuedAt.Valid {
		rec.QueuedAt = j.QueuedAt.Time.Format(time.RFC3339)
	}
	if j.StartedAt.Valid {
		rec.StartedAt = j.StartedAt.Time.Format(time.RFC3339)
	}
	if j.CompletedAt.Valid {
		rec.CompletedAt = j.CompletedAt.Time.Format(time.RFC3339)
	}
	if j.QueuedAt.Valid && j.StartedAt.Valid {
		rec.QueueTimeSeconds = j.StartedAt.Time.Sub(j.QueuedAt.Time).Seconds()
		if rec.QueueTimeSeconds < 0 {
			rec.QueueTimeSeconds = 0
		}
	}
	if j.StartedAt.Valid && j.CompletedAt.Valid {
		rec.DurationSeconds = j.CompletedAt.Time.Sub(j.StartedAt.Time).Seconds()
		if rec.DurationSeconds < 0 {
			rec.DurationSeconds = 0
		}
	}

	return connect.NewResponse(&supervisorv1.GetJobRecordResponse{
		Job: rec,
	}), nil
}

// GetSystemStats aggregates system metrics across live runner state and historical DB executions.
func (s *AnalyticsService) GetSystemStats(ctx context.Context, req *connect.Request[supervisorv1.GetSystemStatsRequest]) (*connect.Response[supervisorv1.GetSystemStatsResponse], error) {
	var active, idle int32
	if s.statsProvider != nil {
		active, idle = s.statsProvider.SystemRunnerStats()
	}

	timeframeHours := int(req.Msg.TimeframeHours)
	if timeframeHours <= 0 {
		timeframeHours = 24
	}
	cutoff := time.Now().UTC().Add(-time.Duration(timeframeHours) * time.Hour)

	row, err := s.db.GetJobStatsSince(ctx, cutoff)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("calculating job metrics: %w", err))
	}

	totalJobs := int32(row.TotalJobs)
	successfulJobs := toInt32(row.SuccessfulJobs)
	failedJobs := toInt32(row.FailedJobs)
	avgQueue := toFloat64(row.AvgQueueSeconds)
	avgRuntime := toFloat64(row.AvgRuntimeSeconds)

	successRate := 0.0
	if totalJobs > 0 {
		successRate = (float64(successfulJobs) / float64(totalJobs)) * 100.0
	}

	hourlyRows, err := s.db.GetHourlyJobStatsSince(ctx, cutoff)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("calculating hourly latency trend: %w", err))
	}

	trend := make([]*supervisorv1.LatencyBucket, 0, len(hourlyRows))
	for _, hr := range hourlyRows {
		ts := ""
		switch val := hr.BucketHour.(type) {
		case string:
			ts = val
		case []byte:
			ts = string(val)
		default:
			if val != nil {
				ts = fmt.Sprintf("%v", val)
			}
		}
		trend = append(trend, &supervisorv1.LatencyBucket{
			Timestamp:         ts,
			AvgQueueSeconds:   toFloat64(hr.AvgQueueSeconds),
			AvgRuntimeSeconds: toFloat64(hr.AvgRuntimeSeconds),
			TotalJobs:         int32(hr.TotalJobs),
			SuccessfulJobs:    toInt32(hr.SuccessfulJobs),
			FailedJobs:        toInt32(hr.FailedJobs),
		})
	}

	knownOutcomeJobs := toInt64(row.KnownOutcomeJobs)
	queueTimedJobs := toInt64(row.QueueTimedJobs)

	return connect.NewResponse(&supervisorv1.GetSystemStatsResponse{
		TotalActiveRunners:      active,
		TotalIdleRunners:        idle,
		AverageQueueTimeSeconds: avgQueue,
		TotalJobs_24H:           totalJobs,
		SuccessfulJobs_24H:      successfulJobs,
		FailedJobs_24H:          failedJobs,
		AverageRuntimeSeconds:   avgRuntime,
		SuccessRatePercent:      successRate,
		QueueLatencyTrend:       trend,
		KnownOutcomeJobs:        knownOutcomeJobs,
		QueueTimedJobs:          queueTimedJobs,
		HostArch:                HostArch(),
		HostOs:                  HostOS(),
	}), nil
}

// WatchDashboard provides near-realtime server-streaming push of system stats, pool states, and recent jobs.
func (s *AnalyticsService) WatchDashboard(ctx context.Context, req *connect.Request[supervisorv1.WatchDashboardRequest], stream *connect.ServerStream[supervisorv1.WatchDashboardResponse]) error {
	intervalMs := req.Msg.IntervalMs
	if intervalMs < 250 {
		intervalMs = 1000
	}
	if intervalMs > 10000 {
		intervalMs = 10000
	}
	ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	defer ticker.Stop()

	sendSnapshot := func() error {
		statsResp, err := s.GetSystemStats(ctx, connect.NewRequest(&supervisorv1.GetSystemStatsRequest{}))
		if err != nil {
			return err
		}

		dbPools, err := s.db.ListRunnerPools(ctx)
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("listing runner pools: %w", err))
		}
		protoPools := make([]*supervisorv1.Pool, 0, len(dbPools))
		for _, p := range dbPools {
			protoPools = append(protoPools, ConvertDBPoolToProto(p, s.poolStats))
		}

		jobs, err := s.db.ListJobHistory(ctx, db.ListJobHistoryParams{
			Limit:  10,
			Offset: 0,
		})
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("listing recent jobs: %w", err))
		}
		recentJobs := make([]*supervisorv1.JobRecord, 0, len(jobs))
		for _, j := range jobs {
			rec := &supervisorv1.JobRecord{
				Id:         j.ID,
				PoolId:     j.PoolID,
				RunnerName: j.RunnerName,
				Status:     j.Status,
			}
			if j.QueuedAt.Valid {
				rec.QueuedAt = j.QueuedAt.Time.Format(time.RFC3339)
			}
			if j.StartedAt.Valid {
				rec.StartedAt = j.StartedAt.Time.Format(time.RFC3339)
			}
			if j.CompletedAt.Valid {
				rec.CompletedAt = j.CompletedAt.Time.Format(time.RFC3339)
			}
			recentJobs = append(recentJobs, rec)
		}

		return stream.Send(&supervisorv1.WatchDashboardResponse{
			Stats:      statsResp.Msg,
			Pools:      protoPools,
			RecentJobs: recentJobs,
		})
	}

	// Send initial snapshot immediately
	if err := sendSnapshot(); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := sendSnapshot(); err != nil {
				return err
			}
		}
	}
}
