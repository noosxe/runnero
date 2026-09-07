package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/noosxe/runnero/internal/db"
)

var (
	// ErrJobNotFound is returned when querying or manipulating a non-existent job.
	ErrJobNotFound = errors.New("job not found")
	// ErrNilTask is returned when attempting to register a job without an executable task function.
	ErrNilTask = errors.New("task function cannot be nil")
	// ErrSchedulerRunning is returned when attempting to Start an already-running scheduler.
	ErrSchedulerRunning = errors.New("scheduler is already running")
	// ErrSchedulerStopped is returned when attempting an operation on a stopped scheduler.
	ErrSchedulerStopped = errors.New("scheduler is stopped")
)

// Options configures the Scheduler.
type Options struct {
	// Clock provides time abstraction. If nil, defaults to RealClock.
	Clock Clock
	// Logger provides structured logging. If nil, defaults to package logger.
	Logger *slog.Logger
}

// Scheduler is an in-memory, thread-safe cron ticking engine that executes registered
// tasks per standard cron expressions in UTC, handles missed fires on restarts,
// and supports dynamic runtime additions, updates, and removals as pools change (docs/03 §5).
type Scheduler struct {
	mu      sync.RWMutex
	clock   Clock
	logger  *slog.Logger
	jobs    map[int64]*jobEntry
	wakeup  chan struct{}
	stopCh  chan struct{}
	doneCh  chan struct{}
	runCtx  context.Context
	cancel  context.CancelFunc
	running bool
	stopped bool
	wg      sync.WaitGroup
}

// NewScheduler creates a new Scheduler with the provided options.
func NewScheduler(opts Options) *Scheduler {
	clk := opts.Clock
	if clk == nil {
		clk = RealClock{}
	}
	log := opts.Logger
	if log == nil {
		log = logger
	}

	return &Scheduler{
		clock:  clk,
		logger: log,
		jobs:   make(map[int64]*jobEntry),
		wakeup: make(chan struct{}, 1),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// RegisterJob adds or updates a scheduled job. If the scheduler is already running and
// a missed fire is detected with MissedFirePolicyRunImmediately, the task is dispatched immediately.
func (s *Scheduler) RegisterJob(cfg JobConfig) error {
	if cfg.Task == nil {
		return ErrNilTask
	}

	sched, err := ParseSchedule(cfg.Schedule)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return ErrSchedulerStopped
	}

	now := s.clock.Now().UTC()
	var pendingImmediate bool
	var nextRun time.Time

	if !cfg.LastRun.IsZero() {
		lastUTC := cfg.LastRun.UTC()
		expected := sched.Next(lastUTC).UTC()
		if expected.Before(now) {
			// A scheduled fire was missed while inactive or offline.
			if cfg.MissedFirePolicy == MissedFirePolicyRunImmediately {
				s.logger.Info("missed cron fire detected on registration, triggering immediately",
					"pool_id", cfg.PoolID,
					"last_run", lastUTC,
					"expected_fire", expected,
					"now", now,
				)
				pendingImmediate = true
				nextRun = sched.Next(now).UTC()
			} else {
				s.logger.Info("missed cron fire detected on registration, skipping execution",
					"pool_id", cfg.PoolID,
					"last_run", lastUTC,
					"expected_fire", expected,
					"now", now,
				)
				nextRun = sched.Next(now).UTC()
			}
		} else {
			nextRun = expected
		}
	} else {
		nextRun = sched.Next(now).UTC()
	}

	entry := &jobEntry{
		poolID:           cfg.PoolID,
		expr:             cfg.Schedule,
		schedule:         sched,
		task:             cfg.Task,
		lastRun:          cfg.LastRun.UTC(),
		nextRun:          nextRun,
		missedFirePolicy: cfg.MissedFirePolicy,
		pendingImmediate: pendingImmediate,
	}

	s.jobs[cfg.PoolID] = entry

	if s.running && pendingImmediate {
		entry.pendingImmediate = false
		s.dispatchJobLocked(entry, s.runCtx)
	}

	s.signalWakeupLocked()
	return nil
}

// UnregisterJob removes a registered job by pool ID. Returns true if the job was found and removed.
func (s *Scheduler) UnregisterJob(poolID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.jobs[poolID]; !exists {
		return false
	}

	delete(s.jobs, poolID)
	s.signalWakeupLocked()
	return true
}

// HasJob reports whether a job for the given pool ID is currently registered.
func (s *Scheduler) HasJob(poolID int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, exists := s.jobs[poolID]
	return exists
}

// NextRun returns the next scheduled execution time for the given pool ID in UTC.
// Exposed for status RPCs such as GetRenovateStatus (docs/03 §5, RUN-65).
func (s *Scheduler) NextRun(poolID int64) (time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, exists := s.jobs[poolID]
	if !exists {
		return time.Time{}, fmt.Errorf("%w: pool_id=%d", ErrJobNotFound, poolID)
	}
	return entry.nextRun, nil
}

// GetJob returns runtime status and scheduling metadata for the given pool ID.
func (s *Scheduler) GetJob(poolID int64) (JobInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, exists := s.jobs[poolID]
	if !exists {
		return JobInfo{}, fmt.Errorf("%w: pool_id=%d", ErrJobNotFound, poolID)
	}

	return JobInfo{
		PoolID:           entry.poolID,
		Schedule:         entry.expr,
		NextRun:          entry.nextRun,
		LastRun:          entry.lastRun,
		IsRunning:        entry.running,
		MissedFirePolicy: entry.missedFirePolicy,
	}, nil
}

// ListJobs returns status metadata for all currently registered jobs.
func (s *Scheduler) ListJobs() []JobInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]JobInfo, 0, len(s.jobs))
	for _, entry := range s.jobs {
		list = append(list, JobInfo{
			PoolID:           entry.poolID,
			Schedule:         entry.expr,
			NextRun:          entry.nextRun,
			LastRun:          entry.lastRun,
			IsRunning:        entry.running,
			MissedFirePolicy: entry.missedFirePolicy,
		})
	}
	return list
}

// SyncJobs synchronizes the registered jobs with the provided desired configurations:
// registering new jobs, updating modified jobs, and removing absent jobs.
func (s *Scheduler) SyncJobs(configs []JobConfig) error {
	desired := make(map[int64]struct{}, len(configs))
	for _, cfg := range configs {
		desired[cfg.PoolID] = struct{}{}
		if err := s.RegisterJob(cfg); err != nil {
			return err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for id := range s.jobs {
		if _, keep := desired[id]; !keep {
			delete(s.jobs, id)
		}
	}
	s.signalWakeupLocked()
	return nil
}

// RenovateConfigReader abstracts reading enabled renovate configurations from the database.
type RenovateConfigReader interface {
	ListEnabledRenovateConfigs(ctx context.Context) ([]db.RenovateConfig, error)
}

// SyncFromDB loads all enabled renovate configurations from the database and synchronizes
// the scheduler's registered jobs: registering newly enabled pools, updating modified schedules,
// and unregistering pools that were disabled or deleted (docs/03 §5).
func (s *Scheduler) SyncFromDB(
	ctx context.Context,
	reader RenovateConfigReader,
	taskFactory func(poolID int64, image string) TaskFunc,
	lastRunResolver func(ctx context.Context, poolID int64) time.Time,
) error {
	if reader == nil {
		return nil
	}

	configs, err := reader.ListEnabledRenovateConfigs(ctx)
	if err != nil {
		return fmt.Errorf("listing enabled renovate configs: %w", err)
	}

	desired := make(map[int64]struct{}, len(configs))
	for _, cfg := range configs {
		if !cfg.Enabled || !cfg.CronSchedule.Valid || cfg.CronSchedule.String == "" {
			continue
		}

		desired[cfg.PoolID] = struct{}{}

		var lastRun time.Time
		if lastRunResolver != nil {
			lastRun = lastRunResolver(ctx, cfg.PoolID)
		}

		var task TaskFunc
		if taskFactory != nil {
			task = taskFactory(cfg.PoolID, cfg.Image)
		}
		if task == nil {
			task = func(ctx context.Context) error {
				s.logger.Info("renovate cron fired (stub)", "pool_id", cfg.PoolID)
				return nil
			}
		}

		if err := s.RegisterJob(JobConfig{
			PoolID:           cfg.PoolID,
			Schedule:         cfg.CronSchedule.String,
			LastRun:          lastRun,
			MissedFirePolicy: MissedFirePolicyRunImmediately,
			Task:             task,
		}); err != nil {
			s.logger.Error("failed to register renovate cron schedule", "pool_id", cfg.PoolID, "schedule", cfg.CronSchedule.String, "err", err)
		}
	}

	s.mu.Lock()
	for poolID := range s.jobs {
		if _, keep := desired[poolID]; !keep {
			delete(s.jobs, poolID)
			s.logger.Info("unregistered disabled/deleted renovate cron job", "pool_id", poolID)
		}
	}
	s.signalWakeupLocked()
	s.mu.Unlock()

	return nil
}

// Start initiates the background ticking loop.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrSchedulerRunning
	}
	if s.stopped {
		s.mu.Unlock()
		return ErrSchedulerStopped
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.runCtx = runCtx
	s.cancel = cancel
	s.running = true

	// Dispatch any pending immediate jobs that accumulated prior to Start.
	for _, entry := range s.jobs {
		if entry.pendingImmediate {
			entry.pendingImmediate = false
			s.dispatchJobLocked(entry, runCtx)
		}
	}
	s.mu.Unlock()

	go s.loop(runCtx)
	s.logger.Info("cron scheduler started")
	return nil
}

// Stop signals the ticking loop to exit and waits for in-flight tasks to complete.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running || s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.running = false
	if s.cancel != nil {
		s.cancel()
	}
	close(s.stopCh)
	s.mu.Unlock()

	<-s.doneCh
	s.wg.Wait()
	s.logger.Info("cron scheduler stopped")
}

func (s *Scheduler) loop(ctx context.Context) {
	defer close(s.doneCh)

	for {
		s.mu.RLock()
		nextFire, hasJobs := s.earliestNextRunLocked()
		s.mu.RUnlock()

		var waitCh <-chan time.Time
		if hasJobs && !nextFire.IsZero() {
			now := s.clock.Now().UTC()
			if nextFire.After(now) {
				waitCh = s.clock.After(nextFire.Sub(now))
			} else {
				waitCh = s.clock.After(0)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-s.wakeup:
			continue
		case <-waitCh:
			s.fireDueJobs(ctx)
		}
	}
}

func (s *Scheduler) earliestNextRunLocked() (time.Time, bool) {
	if len(s.jobs) == 0 {
		return time.Time{}, false
	}

	var earliest time.Time
	for _, entry := range s.jobs {
		if entry.nextRun.IsZero() {
			continue
		}
		if earliest.IsZero() || entry.nextRun.Before(earliest) {
			earliest = entry.nextRun
		}
	}
	return earliest, !earliest.IsZero()
}

func (s *Scheduler) fireDueJobs(ctx context.Context) {
	now := s.clock.Now().UTC()

	s.mu.Lock()
	var toDispatch []*jobEntry
	for _, entry := range s.jobs {
		if !entry.nextRun.IsZero() && !entry.nextRun.After(now) {
			if !entry.running {
				entry.lastRun = now
				entry.nextRun = entry.schedule.Next(now).UTC()
				entry.running = true
				toDispatch = append(toDispatch, entry)
			} else {
				s.logger.Warn("cron job is already running, skipping overlapping run", "pool_id", entry.poolID)
				entry.nextRun = entry.schedule.Next(now).UTC()
			}
		}
	}
	s.mu.Unlock()

	for _, entry := range toDispatch {
		s.wg.Add(1)
		go func(j *jobEntry) {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				j.running = false
				s.mu.Unlock()
			}()

			if err := j.task(ctx); err != nil {
				s.logger.Error("cron task execution failed", "pool_id", j.poolID, "err", err)
			}
		}(entry)
	}
}

func (s *Scheduler) dispatchJobLocked(entry *jobEntry, ctx context.Context) {
	if entry.running {
		s.logger.Warn("cron job is already running, skipping overlapping run", "pool_id", entry.poolID)
		return
	}
	entry.running = true
	now := s.clock.Now().UTC()
	entry.lastRun = now

	s.wg.Add(1)
	go func(j *jobEntry) {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			j.running = false
			s.mu.Unlock()
		}()

		if err := j.task(ctx); err != nil {
			s.logger.Error("cron task execution failed", "pool_id", j.poolID, "err", err)
		}
	}(entry)
}

func (s *Scheduler) signalWakeupLocked() {
	select {
	case s.wakeup <- struct{}{}:
	default:
	}
}
