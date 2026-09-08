package renovate

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/cron"
	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
)

type mockRenovateDB struct {
	mu           sync.Mutex
	pool         db.RunnerPool
	poolErr      error
	config       db.RenovateConfig
	configErr    error
	runs         map[int64]db.RenovateRun
	byContainer  map[string]int64
	nextID       int64
	createRunErr error
}

func newMockRenovateDB() *mockRenovateDB {
	return &mockRenovateDB{
		runs:        make(map[int64]db.RenovateRun),
		byContainer: make(map[string]int64),
		nextID:      1,
		pool: db.RunnerPool{
			ID:            1,
			Name:          "prod-runners",
			Provider:      "github",
			RepositoryUrl: "https://github.com/owner/repo",
			AuthProfileID: 10,
			AllowDocker:   true,
			CpuLimit:      sql.NullString{String: "2.0", Valid: true},
			MemoryLimit:   sql.NullString{String: "4g", Valid: true},
		},
		config: db.RenovateConfig{
			ID:           1,
			PoolID:       1,
			Enabled:      true,
			CronSchedule: sql.NullString{String: "0 2 * * *", Valid: true},
			Image:        "renovate/renovate:38",
		},
	}
}

func (m *mockRenovateDB) GetRunnerPoolById(ctx context.Context, id int64) (db.RunnerPool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.poolErr != nil {
		return db.RunnerPool{}, m.poolErr
	}
	return m.pool, nil
}

func (m *mockRenovateDB) GetRenovateConfigByPoolId(ctx context.Context, poolID int64) (db.RenovateConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.configErr != nil {
		return db.RenovateConfig{}, m.configErr
	}
	return m.config, nil
}

func (m *mockRenovateDB) CreateRenovateRun(ctx context.Context, arg db.CreateRenovateRunParams) (db.RenovateRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createRunErr != nil {
		return db.RenovateRun{}, m.createRunErr
	}
	id := m.nextID
	m.nextID++
	run := db.RenovateRun{
		ID:          id,
		PoolID:      arg.PoolID,
		Status:      arg.Status,
		StartedAt:   arg.StartedAt,
		Summary:     arg.Summary,
		ContainerID: arg.ContainerID,
		CreatedAt:   arg.StartedAt,
	}
	m.runs[id] = run
	if arg.ContainerID.Valid && arg.ContainerID.String != "" {
		m.byContainer[arg.ContainerID.String] = id
	}
	return run, nil
}

func (m *mockRenovateDB) UpdateRenovateRunContainerID(ctx context.Context, arg db.UpdateRenovateRunContainerIDParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, exists := m.runs[arg.ID]
	if !exists {
		return sql.ErrNoRows
	}
	run.ContainerID = arg.ContainerID
	m.runs[arg.ID] = run
	if arg.ContainerID.Valid && arg.ContainerID.String != "" {
		m.byContainer[arg.ContainerID.String] = arg.ID
	}
	return nil
}

func (m *mockRenovateDB) CompleteRenovateRun(ctx context.Context, arg db.CompleteRenovateRunParams) (db.RenovateRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, exists := m.runs[arg.ID]
	if !exists {
		return db.RenovateRun{}, sql.ErrNoRows
	}
	run.Status = arg.Status
	run.CompletedAt = arg.CompletedAt
	run.Summary = arg.Summary
	m.runs[arg.ID] = run
	return run, nil
}

func (m *mockRenovateDB) CompleteRenovateRunByContainerID(ctx context.Context, arg db.CompleteRenovateRunByContainerIDParams) (db.RenovateRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, exists := m.byContainer[arg.ContainerID.String]
	if !exists {
		return db.RenovateRun{}, sql.ErrNoRows
	}
	run := m.runs[id]
	run.Status = arg.Status
	run.CompletedAt = arg.CompletedAt
	run.Summary = arg.Summary
	m.runs[id] = run
	return run, nil
}

func (m *mockRenovateDB) GetRenovateRunByContainerID(ctx context.Context, containerID sql.NullString) (db.RenovateRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !containerID.Valid {
		return db.RenovateRun{}, sql.ErrNoRows
	}
	id, exists := m.byContainer[containerID.String]
	if !exists {
		return db.RenovateRun{}, sql.ErrNoRows
	}
	return m.runs[id], nil
}

func (m *mockRenovateDB) GetLatestRenovateRunByPoolId(ctx context.Context, poolID int64) (db.RenovateRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest db.RenovateRun
	found := false
	for _, r := range m.runs {
		if r.PoolID == poolID {
			if !found || r.StartedAt.After(latest.StartedAt) {
				latest = r
				found = true
			}
		}
	}
	if !found {
		return db.RenovateRun{}, sql.ErrNoRows
	}
	return latest, nil
}

func (m *mockRenovateDB) ListRenovateRunsByPoolId(ctx context.Context, arg db.ListRenovateRunsByPoolIdParams) ([]db.RenovateRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var list []db.RenovateRun
	for _, r := range m.runs {
		if r.PoolID == arg.PoolID {
			list = append(list, r)
		}
	}
	return list, nil
}

func (m *mockRenovateDB) CountRenovateRunsByPoolId(ctx context.Context, poolID int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int64
	for _, r := range m.runs {
		if r.PoolID == poolID {
			count++
		}
	}
	return count, nil
}

type mockGitProvider struct {
	renovateToken string
	regToken      string
	renovateErr   error
	isRenovate    bool
}

func (m *mockGitProvider) GetRegistrationToken(ctx context.Context, scope provider.RegistrationScope, targetURL string) (string, error) {
	return m.regToken, nil
}

func (m *mockGitProvider) ValidateCredentials(ctx context.Context) error {
	return nil
}

func (m *mockGitProvider) ScalingMode() provider.ScalingMode {
	return provider.ScalingWebhook
}

func (m *mockGitProvider) PollQueuedJobs(ctx context.Context, targetURL string) (int, error) {
	return 0, nil
}

func (m *mockGitProvider) DiscoverOrganizations(ctx context.Context) ([]provider.DiscoveredTarget, error) {
	return nil, nil
}

func (m *mockGitProvider) DiscoverRepositories(ctx context.Context) ([]provider.DiscoveredTarget, error) {
	return nil, nil
}

func (m *mockGitProvider) GetRenovateToken(ctx context.Context, targetURL string) (string, error) {
	if m.renovateErr != nil {
		return "", m.renovateErr
	}
	return m.renovateToken, nil
}

type mockGitProviderWithoutRenovate struct {
	regToken string
}

func (m *mockGitProviderWithoutRenovate) GetRegistrationToken(ctx context.Context, scope provider.RegistrationScope, targetURL string) (string, error) {
	return m.regToken, nil
}

func (m *mockGitProviderWithoutRenovate) ValidateCredentials(ctx context.Context) error {
	return nil
}

func (m *mockGitProviderWithoutRenovate) ScalingMode() provider.ScalingMode {
	return provider.ScalingWebhook
}

func (m *mockGitProviderWithoutRenovate) PollQueuedJobs(ctx context.Context, targetURL string) (int, error) {
	return 0, nil
}

func (m *mockGitProviderWithoutRenovate) DiscoverOrganizations(ctx context.Context) ([]provider.DiscoveredTarget, error) {
	return nil, nil
}

func (m *mockGitProviderWithoutRenovate) DiscoverRepositories(ctx context.Context) ([]provider.DiscoveredTarget, error) {
	return nil, nil
}

type mockProviderResolver struct {
	prov provider.GitProvider
	err  error
}

func (m *mockProviderResolver) ResolveProvider(ctx context.Context, authProfileID int64) (provider.GitProvider, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.prov, nil
}

type mockTaskSpawner struct {
	mu         sync.Mutex
	spawned    []orchestrator.RunnerConfig
	nextID     int
	spawnErr   error
	spawnedIDs []string
}

func (m *mockTaskSpawner) SpawnTask(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.spawnErr != nil {
		return "", m.spawnErr
	}
	m.nextID++
	id := "renovate-task-cid-" + string(rune('0'+m.nextID))
	m.spawned = append(m.spawned, config)
	m.spawnedIDs = append(m.spawnedIDs, id)
	return id, nil
}

func TestExecutor_Execute_Success(t *testing.T) {
	mockDB := newMockRenovateDB()
	mockProv := &mockGitProvider{
		renovateToken: "ghs_installation_token_123",
		isRenovate:    true,
	}
	mockResolver := &mockProviderResolver{prov: mockProv}
	mockSpawner := &mockTaskSpawner{}

	exec, err := NewExecutor(ExecutorOptions{
		DB:        mockDB,
		Providers: mockResolver,
		Spawner:   mockSpawner,
	})
	if err != nil {
		t.Fatalf("NewExecutor failed: %v", err)
	}

	ctx := context.Background()
	run, err := exec.Execute(ctx, 1)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if run.Status != "running" {
		t.Fatalf("expected status 'running', got '%s'", run.Status)
	}
	if !run.ContainerID.Valid || run.ContainerID.String == "" {
		t.Fatal("expected valid container ID on run")
	}

	// Verify spawner config
	if len(mockSpawner.spawned) != 1 {
		t.Fatalf("expected 1 task spawned, got %d", len(mockSpawner.spawned))
	}
	cfg := mockSpawner.spawned[0]
	if cfg.Image != "renovate/renovate:38" {
		t.Errorf("expected image renovate/renovate:38, got %s", cfg.Image)
	}
	if cfg.Token != "ghs_installation_token_123" {
		t.Errorf("expected token ghs_installation_token_123, got %s", cfg.Token)
	}
	if cfg.RepoURL != "https://github.com/owner/repo" {
		t.Errorf("expected repo URL https://github.com/owner/repo, got %s", cfg.RepoURL)
	}
	if cfg.AllowDocker != true {
		t.Errorf("expected AllowDocker true, got %v", cfg.AllowDocker)
	}
	if cfg.CPULimit != "2.0" || cfg.MemoryLimit != "4g" {
		t.Errorf("expected CPU 2.0 and mem 4g, got %s, %s", cfg.CPULimit, cfg.MemoryLimit)
	}
}

func TestExecutor_Execute_SpawnFailure(t *testing.T) {
	mockDB := newMockRenovateDB()
	mockProv := &mockGitProvider{renovateToken: "token123"}
	mockResolver := &mockProviderResolver{prov: mockProv}
	mockSpawner := &mockTaskSpawner{
		spawnErr: errors.New("docker daemon out of memory"),
	}

	exec, err := NewExecutor(ExecutorOptions{
		DB:        mockDB,
		Providers: mockResolver,
		Spawner:   mockSpawner,
	})
	if err != nil {
		t.Fatalf("NewExecutor failed: %v", err)
	}

	ctx := context.Background()
	_, err = exec.Execute(ctx, 1)
	if err == nil {
		t.Fatal("expected error from Execute, got nil")
	}

	// Verify run recorded as failure
	if len(mockDB.runs) != 1 {
		t.Fatalf("expected 1 run in db, got %d", len(mockDB.runs))
	}
	run := mockDB.runs[1]
	if run.Status != "failure" {
		t.Errorf("expected status failure, got %s", run.Status)
	}
	if !strings.Contains(run.Summary, "Failed to spawn") {
		t.Errorf("expected summary to mention failure, got %s", run.Summary)
	}
}

func TestExecutor_Execute_FallbackTokenProvider(t *testing.T) {
	mockDB := newMockRenovateDB()
	mockProv := &mockGitProviderWithoutRenovate{regToken: "fallback_token_xyz"}
	mockResolver := &mockProviderResolver{prov: mockProv}
	mockSpawner := &mockTaskSpawner{}

	exec, _ := NewExecutor(ExecutorOptions{
		DB:        mockDB,
		Providers: mockResolver,
		Spawner:   mockSpawner,
	})

	ctx := context.Background()
	run, err := exec.Execute(ctx, 1)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if run.Status != "running" {
		t.Fatalf("expected status running, got %s", run.Status)
	}
	if mockSpawner.spawned[0].Token != "fallback_token_xyz" {
		t.Fatalf("expected fallback token, got %s", mockSpawner.spawned[0].Token)
	}
}

func TestExecutor_HandleContainerExit_SuccessAndFailure(t *testing.T) {
	mockDB := newMockRenovateDB()
	mockProv := &mockGitProvider{renovateToken: "token"}
	mockResolver := &mockProviderResolver{prov: mockProv}
	mockSpawner := &mockTaskSpawner{}

	exec, _ := NewExecutor(ExecutorOptions{
		DB:        mockDB,
		Providers: mockResolver,
		Spawner:   mockSpawner,
	})

	ctx := context.Background()
	run, err := exec.Execute(ctx, 1)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	cid := run.ContainerID.String

	// Handle exit 0
	handled, err := exec.HandleContainerExit(ctx, cid, 0, "")
	if err != nil {
		t.Fatalf("HandleContainerExit failed: %v", err)
	}
	if !handled {
		t.Fatal("expected handled to be true")
	}

	updated := mockDB.runs[run.ID]
	if updated.Status != "success" {
		t.Fatalf("expected status 'success', got '%s'", updated.Status)
	}
	if !updated.CompletedAt.Valid {
		t.Fatal("expected completed_at to be valid")
	}
	if !strings.Contains(updated.Summary, "completed successfully") {
		t.Fatalf("unexpected summary: %s", updated.Summary)
	}

	// Calling again on already completed run
	handledAgain, err := exec.HandleContainerExit(ctx, cid, 0, "")
	if err != nil || !handledAgain {
		t.Fatalf("expected handledAgain true, got %v, err: %v", handledAgain, err)
	}

	// Non-existent container
	notHandled, err := exec.HandleContainerExit(ctx, "non-existent-cid", 0, "")
	if err != nil || notHandled {
		t.Fatalf("expected notHandled false, got %v, err: %v", notHandled, err)
	}
}

func TestExecutor_HandleContainerExit_WithLogSummary(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "logs", "task1.log.jsonl.gz")

	// Create compressed log file
	logContent := "INFO: Authenticated\nINFO: Cloned repository\nINFO: Created PR #101: bump react to 19.0.0\nINFO: Created PR #102: bump typescript to 5.6.0\nINFO: Done\n"
	stream := strings.NewReader(logContent)
	_, err := orchestrator.CaptureAndCompressLogs(stream, logFile)
	if err != nil {
		t.Fatalf("failed to create test log file: %v", err)
	}

	mockDB := newMockRenovateDB()
	mockProv := &mockGitProvider{renovateToken: "token"}
	mockResolver := &mockProviderResolver{prov: mockProv}
	mockSpawner := &mockTaskSpawner{}

	exec, _ := NewExecutor(ExecutorOptions{
		DB:        mockDB,
		Providers: mockResolver,
		Spawner:   mockSpawner,
	})

	ctx := context.Background()
	run, _ := exec.Execute(ctx, 1)
	cid := run.ContainerID.String

	handled, err := exec.HandleContainerExit(ctx, cid, 0, logFile)
	if err != nil || !handled {
		t.Fatalf("HandleContainerExit failed: %v", err)
	}

	updated := mockDB.runs[run.ID]
	if updated.Status != "success" {
		t.Fatalf("expected success, got %s", updated.Status)
	}
	if !strings.Contains(updated.Summary, "2 pull request(s)") {
		t.Fatalf("expected summary to report 2 PRs, got: %s", updated.Summary)
	}
}

func TestExecutor_CronSchedulerIntegration(t *testing.T) {
	initial := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	vc := cron.NewVirtualClock(initial)
	s := cron.NewScheduler(cron.Options{Clock: vc})

	mockDB := newMockRenovateDB()
	mockProv := &mockGitProvider{renovateToken: "tok123"}
	mockResolver := &mockProviderResolver{prov: mockProv}
	mockSpawner := &mockTaskSpawner{}

	exec, _ := NewExecutor(ExecutorOptions{
		DB:        mockDB,
		Providers: mockResolver,
		Spawner:   mockSpawner,
	})

	var taskExecuted atomic.Bool
	taskFactory := func(poolID int64, image string) cron.TaskFunc {
		return func(ctx context.Context) error {
			taskExecuted.Store(true)
			_, err := exec.Execute(ctx, poolID)
			return err
		}
	}

	err := s.RegisterJob(cron.JobConfig{
		PoolID:   1,
		Schedule: "0 2 * * *", // 2 AM UTC daily
		Task:     taskFactory(1, "renovate/renovate:38"),
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

	// Wait for scheduler to wait on virtual clock
	for i := 0; i < 50; i++ {
		if vc.WaitersCount() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Advance virtual time to 2 AM UTC
	vc.Advance(2 * time.Hour)

	// Wait for task execution
	for i := 0; i < 50; i++ {
		if taskExecuted.Load() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !taskExecuted.Load() {
		t.Fatal("expected cron task to execute upon virtual clock advance")
	}

	// Verify run recorded in DB
	run, err := mockDB.GetLatestRenovateRunByPoolId(ctx, 1)
	if err != nil {
		t.Fatalf("GetLatestRenovateRunByPoolId failed: %v", err)
	}
	if run.Status != "running" {
		t.Fatalf("expected run status running, got %s", run.Status)
	}
	if len(mockSpawner.spawned) != 1 {
		t.Fatalf("expected 1 container spawned, got %d", len(mockSpawner.spawned))
	}

	// Now simulate container exit: die event reaped
	cid := mockSpawner.spawnedIDs[0]
	handled, err := exec.HandleContainerExit(ctx, cid, 0, "")
	if err != nil || !handled {
		t.Fatalf("HandleContainerExit failed: %v", err)
	}

	completedRun, _ := mockDB.GetLatestRenovateRunByPoolId(ctx, 1)
	if completedRun.Status != "success" {
		t.Fatalf("expected status success after exit, got %s", completedRun.Status)
	}
}

func TestExtractSummary(t *testing.T) {
	tests := []struct {
		name     string
		entries  []orchestrator.LogEntry
		exitCode int
		expected string
	}{
		{
			name:     "success no PRs",
			entries:  []orchestrator.LogEntry{{Content: "checking branches"}},
			exitCode: 0,
			expected: "Renovate completed successfully (no pull requests needed)",
		},
		{
			name: "success with PRs",
			entries: []orchestrator.LogEntry{
				{Content: "Created PR #1: update golang"},
				{Content: "Created PR #2: update docker"},
			},
			exitCode: 0,
			expected: "Renovate created 2 pull request(s)",
		},
		{
			name: "failure with error line",
			entries: []orchestrator.LogEntry{
				{Content: "cloning repo"},
				{Content: "fatal error: could not authenticate to git provider"},
			},
			exitCode: 1,
			expected: "Renovate failed (exit code 1): fatal error: could not authenticate to git provider",
		},
		{
			name:     "failure without error lines",
			entries:  nil,
			exitCode: 137,
			expected: "Renovate failed with exit code 137",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractSummary(tt.entries, tt.exitCode)
			if got != tt.expected {
				t.Errorf("ExtractSummary() = %q, expected %q", got, tt.expected)
			}
		})
	}
}
