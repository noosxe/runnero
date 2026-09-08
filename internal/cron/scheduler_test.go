package cron

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
)

// waitForJobIdle blocks until the scheduler no longer considers the job running.
// A completed task's running flag is cleared by a deferred statement in the
// task goroutine, which may execute after the task has already signaled the
// test — advancing the clock in that window would hit the overlap-skip path
// and lose the next fire. The condition is guaranteed to become true, so the
// poll has no deadline: it cannot flake, a broken scheduler surfaces as a
// test hang instead.
func waitForJobIdle(t *testing.T, s *Scheduler, poolID int64) {
	t.Helper()
	for {
		info, err := s.GetJob(poolID)
		if err != nil {
			t.Fatalf("GetJob failed: %v", err)
		}
		if !info.IsRunning {
			return
		}
		time.Sleep(50 * time.Microsecond)
	}
}

func TestScheduler_VirtualClock_FiresOnSchedule(t *testing.T) {
	// Virtual clock starting at 2026-09-04 00:00:00 UTC
	initial := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	vc := NewVirtualClock(initial)

	s := NewScheduler(Options{Clock: vc})

	var fireCount atomic.Int32
	firedTimes := make(chan time.Time, 10)

	// Register a job to run daily at 02:00:00 UTC
	err := s.RegisterJob(JobConfig{
		PoolID:   1,
		Schedule: "0 2 * * *",
		Task: func(ctx context.Context) error {
			fireCount.Add(1)
			firedTimes <- vc.Now()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RegisterJob failed: %v", err)
	}

	// Verify next run computation
	next, err := s.NextRun(1)
	if err != nil {
		t.Fatalf("NextRun failed: %v", err)
	}
	expectedNext1 := time.Date(2026, 9, 4, 2, 0, 0, 0, time.UTC)
	if !next.Equal(expectedNext1) {
		t.Fatalf("expected next run %v, got %v", expectedNext1, next)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Stop()

	// Deterministically wait until the scheduler loop has armed its timer on the
	// virtual clock: no wall-clock deadline is involved.
	vc.WaitForWaiter()

	// Advance 1 hour: now 01:00 UTC. The job cannot fire yet — the scheduler
	// dispatches only when a virtual timer fires, and no timer deadline is due
	// before 02:00 UTC — so the zero-fire assertion is deterministic without
	// any settling sleep.
	vc.Advance(1 * time.Hour)
	if fireCount.Load() != 0 {
		t.Fatalf("expected 0 fires, got %d", fireCount.Load())
	}

	// Advance another 1 hour: now 02:00 UTC -> must fire. The fire is guaranteed
	// by virtual-clock semantics — the armed absolute-deadline timer fires on
	// Advance, and Until's at-or-before-now fast path covers a loop that has
	// not re-armed yet — so a blocking receive needs no wall-clock timeout.
	vc.Advance(1 * time.Hour)

	fireTime := <-firedTimes
	if !fireTime.Equal(expectedNext1) {
		t.Fatalf("expected fire time %v, got %v", expectedNext1, fireTime)
	}

	// fireDueJobs records the next run before dispatching the task, and the
	// task signals via firedTimes only after running, so the updated next run
	// is already visible here — no polling loop needed.
	expectedNext2 := time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC)

	// The task has signaled, but the scheduler clears its running flag in a
	// deferred statement that may not have executed yet; advancing before it
	// does would make the scheduler treat the next fire as overlapping and
	// skip it. Wait deterministically for the job to go idle.
	waitForJobIdle(t, s, 1)
	next, err = s.NextRun(1)
	if err != nil {
		t.Fatalf("NextRun failed: %v", err)
	}
	if !next.Equal(expectedNext2) {
		t.Fatalf("expected next run %v, got %v", expectedNext2, next)
	}

	// Advance another 24 hours -> second fire at 2026-09-05 02:00 UTC.
	vc.Advance(24 * time.Hour)

	fireTime = <-firedTimes
	if !fireTime.Equal(expectedNext2) {
		t.Fatalf("expected second fire time %v, got %v", expectedNext2, fireTime)
	}

	if fireCount.Load() != 2 {
		t.Fatalf("expected 2 fires total, got %d", fireCount.Load())
	}
}

func TestScheduler_MissedFireHandling(t *testing.T) {
	initial := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC) // 10:00 UTC
	vc := NewVirtualClock(initial)

	t.Run("MissedFirePolicyRunImmediately triggers upon restart", func(t *testing.T) {
		s := NewScheduler(Options{Clock: vc})

		var fired atomic.Bool
		done := make(chan struct{})

		// Last run was yesterday 02:00 UTC, schedule is daily 02:00 UTC.
		// Today 02:00 UTC was missed while daemon was stopped!
		lastRun := time.Date(2026, 9, 3, 2, 0, 0, 0, time.UTC)

		err := s.RegisterJob(JobConfig{
			PoolID:           10,
			Schedule:         "0 2 * * *",
			LastRun:          lastRun,
			MissedFirePolicy: MissedFirePolicyRunImmediately,
			Task: func(ctx context.Context) error {
				fired.Store(true)
				close(done)
				return nil
			},
		})
		if err != nil {
			t.Fatalf("RegisterJob failed: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if err := s.Start(ctx); err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		defer s.Stop()

		select {
		case <-done:
		case <-time.After(1 * time.Second):
			t.Fatal("expected immediate execution of missed run, timed out")
		}

		if !fired.Load() {
			t.Fatal("expected missed run task to execute")
		}

		// Next run should be tomorrow at 02:00 UTC
		next, err := s.NextRun(10)
		if err != nil {
			t.Fatalf("NextRun failed: %v", err)
		}
		expectedNext := time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC)
		if !next.Equal(expectedNext) {
			t.Fatalf("expected next run %v, got %v", expectedNext, next)
		}
	})

	t.Run("MissedFirePolicySkip skips immediate execution", func(t *testing.T) {
		s := NewScheduler(Options{Clock: vc})

		var fired atomic.Bool

		lastRun := time.Date(2026, 9, 3, 2, 0, 0, 0, time.UTC)

		err := s.RegisterJob(JobConfig{
			PoolID:           11,
			Schedule:         "0 2 * * *",
			LastRun:          lastRun,
			MissedFirePolicy: MissedFirePolicySkip,
			Task: func(ctx context.Context) error {
				fired.Store(true)
				return nil
			},
		})
		if err != nil {
			t.Fatalf("RegisterJob failed: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if err := s.Start(ctx); err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		defer s.Stop()

		time.Sleep(50 * time.Millisecond)
		if fired.Load() {
			t.Fatal("expected missed run task to be skipped, but it executed")
		}

		// Next run is tomorrow 02:00 UTC
		next, err := s.NextRun(11)
		if err != nil {
			t.Fatalf("NextRun failed: %v", err)
		}
		expectedNext := time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC)
		if !next.Equal(expectedNext) {
			t.Fatalf("expected next run %v, got %v", expectedNext, next)
		}
	})

	t.Run("Zero LastRun is not treated as missed fire", func(t *testing.T) {
		s := NewScheduler(Options{Clock: vc})

		var fired atomic.Bool

		err := s.RegisterJob(JobConfig{
			PoolID:           12,
			Schedule:         "0 2 * * *",
			LastRun:          time.Time{}, // never run before
			MissedFirePolicy: MissedFirePolicyRunImmediately,
			Task: func(ctx context.Context) error {
				fired.Store(true)
				return nil
			},
		})
		if err != nil {
			t.Fatalf("RegisterJob failed: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if err := s.Start(ctx); err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		defer s.Stop()

		time.Sleep(50 * time.Millisecond)
		if fired.Load() {
			t.Fatal("zero LastRun should not trigger immediate missed execution")
		}
	})

	t.Run("Missed fire detected on registration while scheduler is already running", func(t *testing.T) {
		s := NewScheduler(Options{Clock: vc})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if err := s.Start(ctx); err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		defer s.Stop()

		var fired atomic.Bool
		done := make(chan struct{})

		lastRun := time.Date(2026, 9, 3, 2, 0, 0, 0, time.UTC)
		err := s.RegisterJob(JobConfig{
			PoolID:           13,
			Schedule:         "0 2 * * *",
			LastRun:          lastRun,
			MissedFirePolicy: MissedFirePolicyRunImmediately,
			Task: func(ctx context.Context) error {
				fired.Store(true)
				close(done)
				return nil
			},
		})
		if err != nil {
			t.Fatalf("RegisterJob failed: %v", err)
		}

		select {
		case <-done:
		case <-time.After(1 * time.Second):
			t.Fatal("expected immediate execution while running, timed out")
		}

		if !fired.Load() {
			t.Fatal("expected task to fire immediately")
		}
	})
}

func TestScheduler_RuntimeManagement(t *testing.T) {
	initial := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	vc := NewVirtualClock(initial)

	s := NewScheduler(Options{Clock: vc})

	// Error on nil task
	err := s.RegisterJob(JobConfig{
		PoolID:   1,
		Schedule: "0 2 * * *",
		Task:     nil,
	})
	if !errors.Is(err, ErrNilTask) {
		t.Fatalf("expected ErrNilTask, got %v", err)
	}

	// Error on invalid schedule
	err = s.RegisterJob(JobConfig{
		PoolID:   1,
		Schedule: "invalid",
		Task:     func(ctx context.Context) error { return nil },
	})
	if !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("expected ErrInvalidSchedule, got %v", err)
	}

	// Register valid job
	err = s.RegisterJob(JobConfig{
		PoolID:   1,
		Schedule: "0 2 * * *",
		Task:     func(ctx context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("RegisterJob failed: %v", err)
	}

	if !s.HasJob(1) {
		t.Fatal("expected HasJob(1) to be true")
	}

	jobInfo, err := s.GetJob(1)
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if jobInfo.PoolID != 1 || jobInfo.Schedule != "0 2 * * *" {
		t.Fatalf("unexpected job info: %+v", jobInfo)
	}

	list := s.ListJobs()
	if len(list) != 1 || list[0].PoolID != 1 {
		t.Fatalf("unexpected list jobs: %+v", list)
	}

	// Unregister job
	if !s.UnregisterJob(1) {
		t.Fatal("expected UnregisterJob(1) to return true")
	}
	if s.HasJob(1) {
		t.Fatal("expected HasJob(1) to be false")
	}
	if s.UnregisterJob(1) {
		t.Fatal("expected UnregisterJob(1) on non-existent to return false")
	}

	_, err = s.NextRun(1)
	if !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("expected ErrJobNotFound, got %v", err)
	}
	_, err = s.GetJob(1)
	if !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("expected ErrJobNotFound, got %v", err)
	}
}

func TestScheduler_SyncJobs(t *testing.T) {
	initial := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	vc := NewVirtualClock(initial)
	s := NewScheduler(Options{Clock: vc})

	// Initial set of jobs: pool 1 and pool 2
	err := s.SyncJobs([]JobConfig{
		{PoolID: 1, Schedule: "0 2 * * *", Task: func(ctx context.Context) error { return nil }},
		{PoolID: 2, Schedule: "0 3 * * *", Task: func(ctx context.Context) error { return nil }},
	})
	if err != nil {
		t.Fatalf("SyncJobs failed: %v", err)
	}

	if !s.HasJob(1) || !s.HasJob(2) {
		t.Fatal("expected pool 1 and pool 2 to be registered")
	}

	// Sync with pool 2 updated schedule and pool 3 added, pool 1 removed
	err = s.SyncJobs([]JobConfig{
		{PoolID: 2, Schedule: "0 4 * * *", Task: func(ctx context.Context) error { return nil }},
		{PoolID: 3, Schedule: "0 5 * * *", Task: func(ctx context.Context) error { return nil }},
	})
	if err != nil {
		t.Fatalf("SyncJobs failed: %v", err)
	}

	if s.HasJob(1) {
		t.Fatal("expected pool 1 to be removed")
	}
	if !s.HasJob(2) || !s.HasJob(3) {
		t.Fatal("expected pool 2 and pool 3 to be registered")
	}

	job2, _ := s.GetJob(2)
	if job2.Schedule != "0 4 * * *" {
		t.Fatalf("expected pool 2 schedule to be updated, got %s", job2.Schedule)
	}
}

func TestScheduler_NonOverlappingExecution(t *testing.T) {
	initial := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	vc := NewVirtualClock(initial)
	s := NewScheduler(Options{Clock: vc})

	var concurrentRuns atomic.Int32
	var maxConcurrent atomic.Int32
	taskBlocker := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)

	// Run every hour
	err := s.RegisterJob(JobConfig{
		PoolID:   1,
		Schedule: "0 * * * *",
		Task: func(ctx context.Context) error {
			cur := concurrentRuns.Add(1)
			if cur > maxConcurrent.Load() {
				maxConcurrent.Store(cur)
			}
			wg.Done()
			<-taskBlocker // hold task running
			concurrentRuns.Add(-1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RegisterJob failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for scheduler to wait on clock
	for i := 0; i < 50; i++ {
		if vc.WaitersCount() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Advance 1 hour: fires first run
	vc.Advance(1 * time.Hour)
	wg.Wait() // wait until first run entered

	jobInfo, _ := s.GetJob(1)
	if !jobInfo.IsRunning {
		t.Fatal("expected job to be marked running")
	}

	// Advance another hour while first run is still blocked
	vc.Advance(1 * time.Hour)
	time.Sleep(20 * time.Millisecond)

	// Verify concurrent runs did not exceed 1
	if maxConcurrent.Load() > 1 {
		t.Fatalf("expected max concurrent runs <= 1, got %d", maxConcurrent.Load())
	}

	close(taskBlocker)
	s.Stop()
}

func TestScheduler_LifecycleErrors(t *testing.T) {
	s := NewScheduler(Options{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Starting again returns error
	if err := s.Start(ctx); !errors.Is(err, ErrSchedulerRunning) {
		t.Fatalf("expected ErrSchedulerRunning, got %v", err)
	}

	s.Stop()

	// Operations on stopped scheduler return ErrSchedulerStopped
	if err := s.Start(ctx); !errors.Is(err, ErrSchedulerStopped) {
		t.Fatalf("expected ErrSchedulerStopped on Start, got %v", err)
	}
	err := s.RegisterJob(JobConfig{
		PoolID:   1,
		Schedule: "0 2 * * *",
		Task:     func(ctx context.Context) error { return nil },
	})
	if !errors.Is(err, ErrSchedulerStopped) {
		t.Fatalf("expected ErrSchedulerStopped on RegisterJob, got %v", err)
	}
}

type mockRenovateConfigReader struct {
	configs []db.RenovateConfig
	err     error
}

func (m *mockRenovateConfigReader) ListEnabledRenovateConfigs(ctx context.Context) ([]db.RenovateConfig, error) {
	return m.configs, m.err
}

func TestScheduler_SyncFromDB(t *testing.T) {
	initial := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	vc := NewVirtualClock(initial)
	s := NewScheduler(Options{Clock: vc})

	mockReader := &mockRenovateConfigReader{
		configs: []db.RenovateConfig{
			{
				PoolID:       1,
				Enabled:      true,
				CronSchedule: sqlNullString("0 2 * * *"),
				Image:        "renovate/renovate:37",
			},
			{
				PoolID:       2,
				Enabled:      false, // disabled, should not register
				CronSchedule: sqlNullString("0 3 * * *"),
				Image:        "renovate/renovate:latest",
			},
			{
				PoolID:       3,
				Enabled:      true,
				CronSchedule: sqlNullString("*/15 * * * *"),
				Image:        "renovate/renovate:latest",
			},
		},
	}

	var triggered atomic.Int64
	taskFactory := func(poolID int64, image string) TaskFunc {
		return func(ctx context.Context) error {
			triggered.Store(poolID)
			return nil
		}
	}

	ctx := context.Background()
	if err := s.SyncFromDB(ctx, mockReader, taskFactory, nil); err != nil {
		t.Fatalf("SyncFromDB failed: %v", err)
	}

	if !s.HasJob(1) {
		t.Fatal("expected pool 1 to be registered")
	}
	if s.HasJob(2) {
		t.Fatal("expected pool 2 to NOT be registered because enabled=false")
	}
	if !s.HasJob(3) {
		t.Fatal("expected pool 3 to be registered")
	}

	// Update configs: remove pool 1, enable pool 2 with new schedule
	mockReader.configs = []db.RenovateConfig{
		{
			PoolID:       2,
			Enabled:      true,
			CronSchedule: sqlNullString("0 4 * * *"),
			Image:        "renovate/renovate:latest",
		},
		{
			PoolID:       3,
			Enabled:      true,
			CronSchedule: sqlNullString("*/30 * * * *"), // updated schedule
			Image:        "renovate/renovate:latest",
		},
	}

	if err := s.SyncFromDB(ctx, mockReader, taskFactory, nil); err != nil {
		t.Fatalf("SyncFromDB update failed: %v", err)
	}

	if s.HasJob(1) {
		t.Fatal("expected pool 1 to be removed")
	}
	if !s.HasJob(2) {
		t.Fatal("expected pool 2 to now be registered")
	}
	if !s.HasJob(3) {
		t.Fatal("expected pool 3 to be registered")
	}
	job3, _ := s.GetJob(3)
	if job3.Schedule != "*/30 * * * *" {
		t.Fatalf("expected pool 3 schedule to update, got %s", job3.Schedule)
	}
}

func sqlNullString(s string) sql.NullString {
	return sql.NullString{
		String: s,
		Valid:  s != "",
	}
}
