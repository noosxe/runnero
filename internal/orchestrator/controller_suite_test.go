package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/webhook"
)

type ControllerTestSuite struct {
	suite.Suite
	ctx         context.Context
	cancel      context.CancelFunc
	mockEngine  *orchestrator.MockContainerProvider
	mockProv    *mockGitProvider
	resolver    *mockGitProviderResolver
	reconciler  *orchestrator.Reconciler
	liveMu      sync.Mutex
	liveRunners map[string]orchestrator.RunnerStatus
}

func (s *ControllerTestSuite) SetupTest() {
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.mockEngine = orchestrator.NewMockContainerProvider()
	s.mockProv = &mockGitProvider{}
	s.resolver = &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{
			10: s.mockProv,
		},
	}
	s.liveRunners = make(map[string]orchestrator.RunnerStatus)
	s.mockEngine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		s.liveMu.Lock()
		defer s.liveMu.Unlock()
		res := make([]orchestrator.RunnerStatus, 0, len(s.liveRunners))
		for _, r := range s.liveRunners {
			res = append(res, r)
		}
		return res, nil
	}
	s.mockEngine.TerminateRunnerFn = func(ctx context.Context, containerID string) error {
		s.liveMu.Lock()
		delete(s.liveRunners, containerID)
		s.liveMu.Unlock()
		return nil
	}
	s.reconciler = orchestrator.NewReconciler(s.mockEngine)
}

func (s *ControllerTestSuite) TearDownTest() {
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *ControllerTestSuite) TestReplenisher_ProviderFailureHandledGracefully() {
	pool := db.RunnerPool{
		ID:             1,
		Name:           "failing-prov-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/owner/repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 2,
		MaxConcurrency: 4,
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}

	// Make provider return error for registration token initially
	s.mockProv.tokenErr = errors.New("github api rate limit exceeded")

	spawnAttempts := int32(0)
	s.mockEngine.SpawnRunnerFn = func(ctx context.Context, cfg orchestrator.RunnerConfig) (string, error) {
		idx := atomic.AddInt32(&spawnAttempts, 1)
		id := fmt.Sprintf("c-spawned-%d", idx)
		s.liveMu.Lock()
		s.liveRunners[id] = orchestrator.RunnerStatus{
			ID:        id,
			Name:      cfg.Name,
			PoolName:  cfg.PoolName,
			State:     "running",
			SpawnedAt: time.Now().UTC(),
		}
		s.liveMu.Unlock()
		return id, nil
	}

	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  s.mockEngine,
		ProviderResolver: s.resolver,
		Reconciler:       s.reconciler,
	})

	err := ctrl.Boot(s.ctx)
	s.Require().NoError(err)

	// Since token generation fails during boot, no runners should have been spawned
	s.Assert().Equal(int32(0), atomic.LoadInt32(&spawnAttempts))
	active, idle := ctrl.PoolStats("failing-prov-pool")
	s.Assert().Equal(int32(0), active)
	s.Assert().Equal(int32(0), idle)

	// Now provider recovers
	s.mockProv.tokenErr = nil
	err = ctrl.Reconcile(s.ctx)
	s.Require().NoError(err)

	// Reconcile should now spawn the required MinIdleRunners (2)
	s.Assert().Equal(int32(2), atomic.LoadInt32(&spawnAttempts))
	active, idle = ctrl.PoolStats("failing-prov-pool")
	s.Assert().Equal(int32(0), active)
	s.Assert().Equal(int32(2), idle)
}

func (s *ControllerTestSuite) TestReplenisher_EngineFailurePreservesState() {
	pool := db.RunnerPool{
		ID:             2,
		Name:           "engine-fail-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/owner/repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 2,
		MaxConcurrency: 4,
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}

	spawnCalls := int32(0)
	s.mockEngine.SpawnRunnerFn = func(ctx context.Context, cfg orchestrator.RunnerConfig) (string, error) {
		atomic.AddInt32(&spawnCalls, 1)
		return "", errors.New("docker daemon out of memory")
	}

	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  s.mockEngine,
		ProviderResolver: s.resolver,
		Reconciler:       s.reconciler,
	})

	err := ctrl.Boot(s.ctx)
	s.Require().NoError(err)

	// Boot attempted 1 spawn which failed; state is healthy with 0 idle
	s.Assert().Equal(int32(1), atomic.LoadInt32(&spawnCalls))
	active, idle := ctrl.PoolStats("engine-fail-pool")
	s.Assert().Equal(int32(0), active)
	s.Assert().Equal(int32(0), idle)

	// Reconcile tick re-attempts spawn
	_ = ctrl.Reconcile(s.ctx)
	s.Assert().Equal(int32(2), atomic.LoadInt32(&spawnCalls))
}

func (s *ControllerTestSuite) TestQuota_GlobalQuotaSaturationAndFairQueueDrain() {
	pool := db.RunnerPool{
		ID:             3,
		Name:           "quota-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/owner/repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 0,
		MaxConcurrency: 10,
		Labels:         `["self-hosted"]`,
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}

	var spawnedMu sync.Mutex
	spawned := make([]string, 0)
	s.mockEngine.SpawnRunnerFn = func(ctx context.Context, cfg orchestrator.RunnerConfig) (string, error) {
		spawnedMu.Lock()
		defer spawnedMu.Unlock()
		id := "cnt-" + cfg.Name
		spawned = append(spawned, id)
		s.liveMu.Lock()
		s.liveRunners[id] = orchestrator.RunnerStatus{
			ID:        id,
			Name:      cfg.Name,
			PoolName:  cfg.PoolName,
			State:     "running",
			SpawnedAt: time.Now().UTC(),
		}
		s.liveMu.Unlock()
		return id, nil
	}

	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  s.mockEngine,
		ProviderResolver: s.resolver,
		Reconciler:       s.reconciler,
		GlobalMaxRunners: 2, // Saturation limit = 2
	})

	err := ctrl.Boot(s.ctx)
	s.Require().NoError(err)

	// Event 1: Queued -> spawns
	ev1 := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "owner/repo",
			HTMLURL:  "https://github.com/owner/repo",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     1001,
			Labels: []string{"self-hosted"},
		},
	}
	s.Require().NoError(ctrl.HandleWorkflowJob(s.ctx, "github", ev1))

	// Event 2: Queued -> spawns (reaches global limit 2)
	ev2 := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "owner/repo",
			HTMLURL:  "https://github.com/owner/repo",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     1002,
			Labels: []string{"self-hosted"},
		},
	}
	s.Require().NoError(ctrl.HandleWorkflowJob(s.ctx, "github", ev2))

	spawnedMu.Lock()
	count := len(spawned)
	spawnedMu.Unlock()
	s.Assert().Equal(2, count)

	// Event 3: Queued -> Global quota saturated (2/2), queued internally
	ev3 := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "owner/repo",
			HTMLURL:  "https://github.com/owner/repo",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     1003,
			Labels: []string{"self-hosted"},
		},
	}
	s.Require().NoError(ctrl.HandleWorkflowJob(s.ctx, "github", ev3))
	s.Assert().Equal(1, ctrl.QueueLength())

	spawnedMu.Lock()
	countAfter3 := len(spawned)
	spawnedMu.Unlock()
	s.Assert().Equal(2, countAfter3)

	// Event 1 completes -> runner container exits -> reap drains internal queue
	spawnedMu.Lock()
	firstID := spawned[0]
	spawnedMu.Unlock()

	exitEvent := orchestrator.ContainerEvent{
		ContainerID: firstID,
		PoolName:    "quota-pool",
		Action:      "die",
		ExitCode:    0,
	}
	err = ctrl.HandleContainerEvent(s.ctx, exitEvent)
	s.Require().NoError(err)

	s.Assert().Equal(0, ctrl.QueueLength())

	spawnedMu.Lock()
	countFinal := len(spawned)
	spawnedMu.Unlock()
	s.Assert().Equal(3, countFinal)
}

func (s *ControllerTestSuite) TestLifecycle_ImmediateShutdownTerminatesAll() {
	pool := db.RunnerPool{
		ID:             4,
		Name:           "shutdown-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/owner/repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 2,
		MaxConcurrency: 4,
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}

	spawnedContainers := make([]string, 0)
	var spawnMu sync.Mutex
	s.mockEngine.SpawnRunnerFn = func(ctx context.Context, cfg orchestrator.RunnerConfig) (string, error) {
		spawnMu.Lock()
		defer spawnMu.Unlock()
		id := "cnt-" + cfg.Name
		spawnedContainers = append(spawnedContainers, id)
		return id, nil
	}

	terminated := make([]string, 0)
	var termMu sync.Mutex
	s.mockEngine.TerminateRunnerFn = func(ctx context.Context, containerID string) error {
		termMu.Lock()
		defer termMu.Unlock()
		terminated = append(terminated, containerID)
		return nil
	}

	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  s.mockEngine,
		ProviderResolver: s.resolver,
		Reconciler:       s.reconciler,
	})

	err := ctrl.Boot(s.ctx)
	s.Require().NoError(err)

	spawnMu.Lock()
	spawnCount := len(spawnedContainers)
	spawnMu.Unlock()
	s.Require().Equal(2, spawnCount)

	// Immediate shutdown terminates all containers without waiting
	err = ctrl.ImmediateShutdown(s.ctx)
	s.Require().NoError(err)

	termMu.Lock()
	termCount := len(terminated)
	termMu.Unlock()
	s.Assert().Equal(2, termCount)
}

func TestControllerTestSuite(t *testing.T) {
	suite.Run(t, new(ControllerTestSuite))
}
