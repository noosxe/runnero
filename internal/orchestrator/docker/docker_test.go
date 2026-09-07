package docker_test

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"iter"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	dockerimage "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/orchestrator/docker"
	"github.com/noosxe/runnero/internal/server"
)

type nopPullResponse struct {
	io.ReadCloser
}

func (nopPullResponse) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(yield func(jsonstream.Message, error) bool) {}
}

func (nopPullResponse) Wait(context.Context) error {
	return nil
}

type mockDockerAPI struct {
	pingFn            func(ctx context.Context, options client.PingOptions) (client.PingResult, error)
	closeFn           func() error
	containerCreateFn func(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	containerStartFn  func(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error)
	containerStopFn   func(ctx context.Context, containerID string, options client.ContainerStopOptions) (client.ContainerStopResult, error)
	containerRemoveFn func(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	containerListFn   func(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
	containerLogsFn   func(ctx context.Context, containerID string, options client.ContainerLogsOptions) (client.ContainerLogsResult, error)
	containersPruneFn func(ctx context.Context, opts client.ContainerPruneOptions) (client.ContainerPruneResult, error)
	eventsFn          func(ctx context.Context, options client.EventsListOptions) client.EventsResult
	networkListFn     func(ctx context.Context, options client.NetworkListOptions) (client.NetworkListResult, error)
	networkCreateFn   func(ctx context.Context, name string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error)
	networkConnectFn  func(ctx context.Context, networkID string, options client.NetworkConnectOptions) (client.NetworkConnectResult, error)
	imageInspectFn    func(ctx context.Context, imageID string, inspectOpts ...client.ImageInspectOption) (client.ImageInspectResult, error)
	imagePullFn       func(ctx context.Context, refStr string, options client.ImagePullOptions) (client.ImagePullResponse, error)
}

func (m *mockDockerAPI) Ping(ctx context.Context, options client.PingOptions) (client.PingResult, error) {
	if m.pingFn != nil {
		return m.pingFn(ctx, options)
	}
	return client.PingResult{APIVersion: "1.47"}, nil
}

func (m *mockDockerAPI) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}

func (m *mockDockerAPI) ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	if m.containerCreateFn != nil {
		return m.containerCreateFn(ctx, options)
	}
	return client.ContainerCreateResult{ID: "mock-container-id"}, nil
}

func (m *mockDockerAPI) ContainerStart(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error) {
	if m.containerStartFn != nil {
		return m.containerStartFn(ctx, containerID, options)
	}
	return client.ContainerStartResult{}, nil
}

func (m *mockDockerAPI) ContainerStop(ctx context.Context, containerID string, options client.ContainerStopOptions) (client.ContainerStopResult, error) {
	if m.containerStopFn != nil {
		return m.containerStopFn(ctx, containerID, options)
	}
	return client.ContainerStopResult{}, nil
}

func (m *mockDockerAPI) ContainerRemove(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	if m.containerRemoveFn != nil {
		return m.containerRemoveFn(ctx, containerID, options)
	}
	return client.ContainerRemoveResult{}, nil
}

func (m *mockDockerAPI) ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error) {
	if m.containerListFn != nil {
		return m.containerListFn(ctx, options)
	}
	return client.ContainerListResult{}, nil
}

func (m *mockDockerAPI) ContainerLogs(ctx context.Context, containerID string, options client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	if m.containerLogsFn != nil {
		return m.containerLogsFn(ctx, containerID, options)
	}
	return io.NopCloser(strings.NewReader("")), nil
}

func (m *mockDockerAPI) ContainerPrune(ctx context.Context, opts client.ContainerPruneOptions) (client.ContainerPruneResult, error) {
	if m.containersPruneFn != nil {
		return m.containersPruneFn(ctx, opts)
	}
	return client.ContainerPruneResult{}, nil
}

func (m *mockDockerAPI) Events(ctx context.Context, options client.EventsListOptions) client.EventsResult {
	if m.eventsFn != nil {
		return m.eventsFn(ctx, options)
	}
	return client.EventsResult{}
}

func (m *mockDockerAPI) NetworkList(ctx context.Context, options client.NetworkListOptions) (client.NetworkListResult, error) {
	if m.networkListFn != nil {
		return m.networkListFn(ctx, options)
	}
	return client.NetworkListResult{}, nil
}

func (m *mockDockerAPI) NetworkCreate(ctx context.Context, name string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
	if m.networkCreateFn != nil {
		return m.networkCreateFn(ctx, name, options)
	}
	return client.NetworkCreateResult{ID: "mock-net-id"}, nil
}

func (m *mockDockerAPI) NetworkConnect(ctx context.Context, networkID string, options client.NetworkConnectOptions) (client.NetworkConnectResult, error) {
	if m.networkConnectFn != nil {
		return m.networkConnectFn(ctx, networkID, options)
	}
	return client.NetworkConnectResult{}, nil
}

func (m *mockDockerAPI) ImageInspect(ctx context.Context, imageID string, inspectOpts ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	if m.imageInspectFn != nil {
		return m.imageInspectFn(ctx, imageID, inspectOpts...)
	}
	return client.ImageInspectResult{}, nil
}

func (m *mockDockerAPI) ImagePull(ctx context.Context, refStr string, options client.ImagePullOptions) (client.ImagePullResponse, error) {
	if m.imagePullFn != nil {
		return m.imagePullFn(ctx, refStr, options)
	}
	return nopPullResponse{io.NopCloser(strings.NewReader(""))}, nil
}

var _ docker.APIClient = (*mockDockerAPI)(nil)

func TestDockerClient_BootstrapAndPing(t *testing.T) {
	ctx := context.Background()

	// 1. Successful Ping
	mockAPI := &mockDockerAPI{}
	cli, err := docker.NewClient(ctx,
		docker.WithAPIClient(mockAPI),
		docker.WithHost("tcp://remote-docker:2376"),
		docker.WithDockerHostID("remote-host-1"),
	)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	var _ orchestrator.ContainerProvider = cli

	if err := cli.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
	if cli.DockerHostID() != "remote-host-1" {
		t.Errorf("unexpected DockerHostID: %q", cli.DockerHostID())
	}

	// 2. Failed Ping (daemon unreachable)
	failingAPI := &mockDockerAPI{
		pingFn: func(ctx context.Context, options client.PingOptions) (client.PingResult, error) {
			return client.PingResult{}, errors.New("connection refused")
		},
	}
	failingCli, err := docker.NewClient(ctx, docker.WithAPIClient(failingAPI))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	err = failingCli.Ping(ctx)
	if err == nil || !errors.Is(err, docker.ErrDaemonUnreachable) {
		t.Fatalf("expected ErrDaemonUnreachable, got: %v", err)
	}

	// 3. Close
	if err := cli.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestDockerClient_SpawnRunner(t *testing.T) {
	ctx := context.Background()

	var createdConfig *container.Config
	var createdHostConfig *container.HostConfig
	var createdNetConfig *network.NetworkingConfig
	var createdName string
	var startedID string

	mockAPI := &mockDockerAPI{
		containerCreateFn: func(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
			createdConfig = options.Config
			createdHostConfig = options.HostConfig
			createdNetConfig = options.NetworkingConfig
			createdName = options.Name
			return client.ContainerCreateResult{ID: "c-123456"}, nil
		},
		containerStartFn: func(ctx context.Context, id string, options client.ContainerStartOptions) (client.ContainerStartResult, error) {
			startedID = id
			return client.ContainerStartResult{}, nil
		},
	}

	cli, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	config := orchestrator.RunnerConfig{
		PoolName:    "arm64-pool",
		RepoURL:     "https://github.com/my-org/my-repo",
		Token:       "runner-ephemeral-tok",
		Labels:      []string{"self-hosted", "arm64"},
		WorkDir:     "_custom_work",
		CPULimit:    "1.5",
		MemoryLimit: "2g",
		AllowDocker: true,
	}

	id, err := cli.SpawnRunner(ctx, config)
	if err != nil {
		t.Fatalf("SpawnRunner failed: %v", err)
	}
	if id != "c-123456" || startedID != "c-123456" {
		t.Fatalf("expected container c-123456 created and started, got %q / %q", id, startedID)
	}

	// Verify Naming
	if !strings.HasPrefix(createdName, "runnero-arm64-pool-") {
		t.Errorf("unexpected container name: %q", createdName)
	}

	// Verify Labels
	if createdConfig.Labels[orchestrator.LabelManaged] != "true" {
		t.Errorf("missing managed label")
	}
	if createdConfig.Labels[orchestrator.LabelPoolName] != "arm64-pool" {
		t.Errorf("unexpected pool label: %v", createdConfig.Labels[orchestrator.LabelPoolName])
	}
	if createdConfig.Labels[orchestrator.LabelTaskType] != orchestrator.TaskTypeRunner {
		t.Errorf("unexpected task-type label: %v", createdConfig.Labels[orchestrator.LabelTaskType])
	}
	if createdConfig.Labels[orchestrator.LabelSpawnedAt] == "" {
		t.Errorf("missing spawned-at label")
	}

	// Verify Limits
	if createdHostConfig.NanoCPUs != 1500000000 {
		t.Errorf("expected 1.5e9 NanoCPUs, got %d", createdHostConfig.NanoCPUs)
	}
	if createdHostConfig.Memory != 2*1024*1024*1024 {
		t.Errorf("expected 2GiB memory, got %d", createdHostConfig.Memory)
	}

	// Verify Docker socket mount
	foundDockerSock := false
	for _, bind := range createdHostConfig.Binds {
		if bind == "/var/run/docker.sock:/var/run/docker.sock" {
			foundDockerSock = true
			break
		}
	}
	if !foundDockerSock {
		t.Errorf("expected /var/run/docker.sock mount in binds: %+v", createdHostConfig.Binds)
	}

	// Verify Environment Variables
	envMap := make(map[string]string)
	for _, e := range createdConfig.Env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	if envMap["RUNNER_TOKEN"] != "runner-ephemeral-tok" {
		t.Errorf("unexpected token in env: %v", envMap["RUNNER_TOKEN"])
	}
	if envMap["GITHUB_REPOSITORY_URL"] != "https://github.com/my-org/my-repo" {
		t.Errorf("unexpected repo URL in env: %v", envMap["GITHUB_REPOSITORY_URL"])
	}
	if envMap["RUNNER_WORKDIR"] != "_custom_work" {
		t.Errorf("unexpected workdir: %v", envMap["RUNNER_WORKDIR"])
	}
	if envMap["RUNNER_EPHEMERAL"] != "true" {
		t.Errorf("missing RUNNER_EPHEMERAL=true")
	}

	// Verify Network Attachment
	if createdNetConfig == nil || createdNetConfig.EndpointsConfig[orchestrator.DefaultNetworkName] == nil {
		t.Errorf("expected container attached to %q network: %+v", orchestrator.DefaultNetworkName, createdNetConfig)
	}
}

func TestDockerClient_SpawnTask(t *testing.T) {
	ctx := context.Background()

	var createdConfig *container.Config
	mockAPI := &mockDockerAPI{
		containerCreateFn: func(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
			createdConfig = options.Config
			return client.ContainerCreateResult{ID: "task-999"}, nil
		},
	}

	cli, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	config := orchestrator.RunnerConfig{
		PoolName: "renovate-pool",
		RepoURL:  "https://github.com/org/repo",
		Token:    "renovate-short-lived-tok",
		Image:    "renovate/renovate:latest",
	}

	id, err := cli.SpawnTask(ctx, config)
	if err != nil {
		t.Fatalf("SpawnTask failed: %v", err)
	}
	if id != "task-999" {
		t.Fatalf("unexpected id: %q", id)
	}

	if createdConfig.Labels[orchestrator.LabelTaskType] != orchestrator.TaskTypeJob {
		t.Errorf("expected task-type job, got %v", createdConfig.Labels[orchestrator.LabelTaskType])
	}
	if createdConfig.Image != "renovate/renovate:latest" {
		t.Errorf("unexpected image: %q", createdConfig.Image)
	}

	envMap := make(map[string]string)
	for _, e := range createdConfig.Env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	if envMap["RENOVATE_TOKEN"] != "renovate-short-lived-tok" {
		t.Errorf("unexpected renovate token: %v", envMap["RENOVATE_TOKEN"])
	}
}

func TestDockerClient_SpawnFailureCleanup(t *testing.T) {
	ctx := context.Background()

	var removedID string
	var forceRemoved bool

	mockAPI := &mockDockerAPI{
		containerCreateFn: func(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
			return client.ContainerCreateResult{ID: "doomed-container"}, nil
		},
		containerStartFn: func(ctx context.Context, id string, options client.ContainerStartOptions) (client.ContainerStartResult, error) {
			return client.ContainerStartResult{}, errors.New("cannot start: port conflict")
		},
		containerRemoveFn: func(ctx context.Context, id string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
			removedID = id
			forceRemoved = options.Force
			return client.ContainerRemoveResult{}, nil
		},
	}

	cli, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	_, err = cli.SpawnRunner(ctx, orchestrator.RunnerConfig{PoolName: "test-pool"})
	if err == nil || !strings.Contains(err.Error(), "starting container") {
		t.Fatalf("expected start container error, got: %v", err)
	}
	if removedID != "doomed-container" || !forceRemoved {
		t.Errorf("expected doomed-container to be force removed, got %q (force=%v)", removedID, forceRemoved)
	}
}

func TestParseLimits(t *testing.T) {
	// 1. CPU Limits
	nano, err := docker.ParseCPULimit("2.5")
	if err != nil || nano != 2500000000 {
		t.Errorf("expected 2.5e9 nano cpus, got %d (err=%v)", nano, err)
	}
	nano, err = docker.ParseCPULimit("")
	if err != nil || nano != 0 {
		t.Errorf("expected 0 for empty cpu, got %d", nano)
	}
	_, err = docker.ParseCPULimit("-1")
	if err == nil {
		t.Errorf("expected error for negative cpu limit")
	}
	_, err = docker.ParseCPULimit("invalid")
	if err == nil {
		t.Errorf("expected error for invalid cpu limit")
	}

	// 2. Memory Limits
	memTests := []struct {
		input    string
		expected int64
	}{
		{"4g", 4 * 1024 * 1024 * 1024},
		{"512m", 512 * 1024 * 1024},
		{"1024k", 1024 * 1024},
		{"1000b", 1000},
		{"", 0},
	}
	for _, tc := range memTests {
		bytes, err := docker.ParseMemoryLimit(tc.input)
		if err != nil {
			t.Errorf("ParseMemoryLimit(%q) failed: %v", tc.input, err)
		}
		if bytes != tc.expected {
			t.Errorf("ParseMemoryLimit(%q) = %d, expected %d", tc.input, bytes, tc.expected)
		}
	}

	_, err = docker.ParseMemoryLimit("bad-mem")
	if err == nil {
		t.Errorf("expected error for bad memory limit")
	}
	_, err = docker.ParseMemoryLimit("-100m")
	if err == nil {
		t.Errorf("expected error for negative memory limit")
	}
}

func TestDockerClient_AuditRunners(t *testing.T) {
	ctx := context.Background()

	var filterUsed client.Filters
	mockAPI := &mockDockerAPI{
		containerListFn: func(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error) {
			filterUsed = options.Filters
			return client.ContainerListResult{
				Items: []container.Summary{
					{
						ID:    "cnt-1",
						Names: []string{"/runnero-pool-a-abcdef"},
						State: "running",
						Labels: map[string]string{
							orchestrator.LabelManaged:   "true",
							orchestrator.LabelPoolName:  "pool-a",
							orchestrator.LabelID:        "runnero-pool-a-abcdef",
							orchestrator.LabelSpawnedAt: "2026-09-03T12:00:00Z",
						},
						NetworkSettings: &container.NetworkSettingsSummary{
							Networks: map[string]*network.EndpointSettings{
								"bridge": {IPAddress: netip.MustParseAddr("172.17.0.2")},
							},
						},
					},
				},
			}, nil
		},
	}

	cli, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	statuses, err := cli.AuditRunners(ctx)
	if err != nil {
		t.Fatalf("AuditRunners failed: %v", err)
	}

	if len(statuses) != 1 {
		t.Fatalf("expected 1 runner status, got %d", len(statuses))
	}
	s := statuses[0]
	if s.ID != "cnt-1" || s.Name != "runnero-pool-a-abcdef" || s.PoolName != "pool-a" || s.State != "running" || s.IPAddress != "172.17.0.2" {
		t.Errorf("unexpected mapped runner status: %+v", s)
	}

	// Verify label filter
	if !filterUsed["label"][orchestrator.LabelManaged+"=true"] {
		t.Errorf("expected managed label filter, got %+v", filterUsed)
	}
}

func TestDockerClient_TerminateRunner(t *testing.T) {
	ctx := context.Background()

	var stoppedID string
	var removedID string
	var forceRemoved bool

	mockAPI := &mockDockerAPI{
		containerStopFn: func(ctx context.Context, containerID string, options client.ContainerStopOptions) (client.ContainerStopResult, error) {
			stoppedID = containerID
			return client.ContainerStopResult{}, nil
		},
		containerRemoveFn: func(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
			removedID = containerID
			forceRemoved = options.Force
			return client.ContainerRemoveResult{}, nil
		},
	}

	cli, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	if err := cli.TerminateRunner(ctx, "cnt-to-terminate"); err != nil {
		t.Fatalf("TerminateRunner failed: %v", err)
	}
	if stoppedID != "cnt-to-terminate" || removedID != "cnt-to-terminate" || !forceRemoved {
		t.Errorf("terminate failed: stopped=%q removed=%q force=%v", stoppedID, removedID, forceRemoved)
	}
}

func TestDockerClient_PruneExitedContainers(t *testing.T) {
	ctx := context.Background()

	var pruneFiltersUsed client.Filters
	mockAPI := &mockDockerAPI{
		containersPruneFn: func(ctx context.Context, opts client.ContainerPruneOptions) (client.ContainerPruneResult, error) {
			pruneFiltersUsed = opts.Filters
			return client.ContainerPruneResult{
				Report: container.PruneReport{
					ContainersDeleted: []string{"c-dead-1", "c-dead-2"},
					SpaceReclaimed:    1024,
				},
			}, nil
		},
	}

	cli, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	if err := cli.PruneExitedContainers(ctx); err != nil {
		t.Fatalf("PruneExitedContainers failed: %v", err)
	}

	if !pruneFiltersUsed["label"][orchestrator.LabelManaged+"=true"] {
		t.Errorf("expected managed label in prune filters: %+v", pruneFiltersUsed)
	}
}

func TestDockerClient_Events(t *testing.T) {
	ctx := context.Background()

	msgChan := make(chan events.Message, 1)
	errChan := make(chan error, 1)

	mockAPI := &mockDockerAPI{
		eventsFn: func(ctx context.Context, options client.EventsListOptions) client.EventsResult {
			return client.EventsResult{
				Messages: msgChan,
				Err:      errChan,
			}
		},
	}

	cli, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	res := cli.Events(ctx, client.EventsListOptions{})
	if res.Messages == nil || res.Err == nil {
		t.Fatalf("expected non-nil event channels")
	}
}

func TestDockerClient_EnsureNetwork(t *testing.T) {
	ctx := context.Background()

	// 1. Network already exists
	listCalled := false
	createCalled := false
	mockAPIExisting := &mockDockerAPI{
		networkListFn: func(ctx context.Context, options client.NetworkListOptions) (client.NetworkListResult, error) {
			listCalled = true
			return client.NetworkListResult{
				Items: []network.Summary{
					{
						Network: network.Network{
							ID:   "net-existing-123",
							Name: orchestrator.DefaultNetworkName,
						},
					},
				},
			}, nil
		},
		networkCreateFn: func(ctx context.Context, name string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
			createCalled = true
			return client.NetworkCreateResult{ID: "should-not-be-called"}, nil
		},
	}

	cli, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPIExisting))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	id, err := cli.EnsureNetwork(ctx, "")
	if err != nil {
		t.Fatalf("EnsureNetwork failed: %v", err)
	}
	if id != "net-existing-123" {
		t.Errorf("expected existing network id net-existing-123, got %q", id)
	}
	if !listCalled || createCalled {
		t.Errorf("expected listCalled=true and createCalled=false, got list=%v create=%v", listCalled, createCalled)
	}

	// 2. Network does not exist -> created with driver bridge and managed label
	var createdNetName string
	var createdOptions client.NetworkCreateOptions
	mockAPINew := &mockDockerAPI{
		networkListFn: func(ctx context.Context, options client.NetworkListOptions) (client.NetworkListResult, error) {
			return client.NetworkListResult{}, nil
		},
		networkCreateFn: func(ctx context.Context, name string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
			createdNetName = name
			createdOptions = options
			return client.NetworkCreateResult{ID: "net-created-456"}, nil
		},
	}

	cliNew, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPINew))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	id, err = cliNew.EnsureNetwork(ctx, "custom-net")
	if err != nil {
		t.Fatalf("EnsureNetwork failed: %v", err)
	}
	if id != "net-created-456" {
		t.Errorf("expected created network id net-created-456, got %q", id)
	}
	if createdNetName != "custom-net" {
		t.Errorf("expected network name custom-net, got %q", createdNetName)
	}
	if createdOptions.Driver != "bridge" {
		t.Errorf("expected bridge driver, got %q", createdOptions.Driver)
	}
	if createdOptions.Labels[orchestrator.LabelManaged] != "true" {
		t.Errorf("expected managed label on created network: %+v", createdOptions.Labels)
	}
}

func TestDockerClient_DegradedModeAndReadiness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var pingMu sync.RWMutex
	var pingError error
	setPingError := func(err error) {
		pingMu.Lock()
		defer pingMu.Unlock()
		pingError = err
	}
	getPingError := func() error {
		pingMu.RLock()
		defer pingMu.RUnlock()
		return pingError
	}

	mockAPI := &mockDockerAPI{
		pingFn: func(ctx context.Context, options client.PingOptions) (client.PingResult, error) {
			if err := getPingError(); err != nil {
				return client.PingResult{}, err
			}
			return client.PingResult{APIVersion: "1.47"}, nil
		},
		containerCreateFn: func(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
			return client.ContainerCreateResult{ID: "cnt-spawned"}, nil
		},
		containerStartFn: func(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error) {
			return client.ContainerStartResult{}, nil
		},
	}

	cli, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	probe := cli.ReadinessCheck()

	// 1. Initially healthy
	if status := probe.Check(ctx); status != server.StatusOK {
		t.Errorf("expected StatusOK, got %v", status)
	}
	if cli.IsDegraded() {
		t.Errorf("expected not degraded initially")
	}

	// 2. Daemon becomes unreachable (e.g. stopped docker in test env)
	setPingError(errors.New("daemon stopped"))
	// Start monitor with small interval
	monitorCtx, cancelMonitor := context.WithCancel(ctx)
	go func() {
		_ = cli.StartHealthMonitor(monitorCtx, 10*time.Millisecond)
	}()

	// Wait for monitor to detect degraded state
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cli.IsDegraded() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cli.IsDegraded() {
		t.Fatalf("expected client to enter degraded state")
	}

	// 3. Readiness check reflects "degraded" (OQ #19: still ready to serve, but flagged)
	if status := probe.Check(ctx); status != server.StatusDegraded {
		t.Errorf("expected StatusDegraded when daemon unreachable, got %v", status)
	}

	// 4. Spawning pauses in degraded mode (RUN-35)
	_, spawnErr := cli.SpawnRunner(ctx, orchestrator.RunnerConfig{PoolName: "test-pool"})
	if spawnErr == nil || !errors.Is(spawnErr, orchestrator.ErrDaemonDegraded) {
		t.Fatalf("expected ErrDaemonDegraded when spawning during degraded mode, got %v", spawnErr)
	}

	// 5. Daemon recovers (acceptance: process stays alive, recovers when daemon returns)
	setPingError(nil)
	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !cli.IsDegraded() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if cli.IsDegraded() {
		t.Fatalf("expected client to recover from degraded state")
	}

	// 6. Readiness check reflects "ok" after recovery
	if status := probe.Check(ctx); status != server.StatusOK {
		t.Errorf("expected StatusOK after daemon recovery, got %v", status)
	}

	// 7. Spawning succeeds after recovery
	id, err := cli.SpawnRunner(ctx, orchestrator.RunnerConfig{PoolName: "test-pool"})
	if err != nil || id != "cnt-spawned" {
		t.Fatalf("expected spawn to succeed after recovery, got id=%q err=%v", id, err)
	}

	cancelMonitor()
}

func TestDockerClient_CaptureLogs(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	rawLog := "2026-09-03T10:00:00.000000000Z Runner connected to GitHub\n2026-09-03T10:00:01.000000000Z Job completed with exit 0\n"
	mockAPI := &mockDockerAPI{
		containerLogsFn: func(ctx context.Context, containerID string, options client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
			if containerID != "runner-capture-test" {
				return nil, errors.New("container not found")
			}
			if !options.ShowStdout || !options.ShowStderr || !options.Timestamps {
				t.Errorf("expected ShowStdout, ShowStderr, Timestamps to be true, got %+v", options)
			}
			return io.NopCloser(strings.NewReader(rawLog)), nil
		},
	}

	cli, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	logPath, err := cli.CaptureLogs(ctx, "runner-capture-test", tempDir)
	if err != nil {
		t.Fatalf("CaptureLogs failed: %v", err)
	}

	expectedPath := orchestrator.LogPath(tempDir, "runner-capture-test")
	if logPath != expectedPath {
		t.Errorf("logPath = %q, want %q", logPath, expectedPath)
	}

	entries, err := orchestrator.ReadGzippedJSONLLogs(logPath)
	if err != nil {
		t.Fatalf("ReadGzippedJSONLLogs failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Content != "Runner connected to GitHub" || entries[0].Stream != "stdout" {
		t.Errorf("unexpected entry 0: %+v", entries[0])
	}
	if entries[1].Content != "Job completed with exit 0" || entries[1].Stream != "stdout" {
		t.Errorf("unexpected entry 1: %+v", entries[1])
	}
}

func TestDockerClient_ImageOperations(t *testing.T) {
	ctx := context.Background()
	var pulled string
	mockAPI := &mockDockerAPI{
		imagePullFn: func(ctx context.Context, refStr string, options client.ImagePullOptions) (client.ImagePullResponse, error) {
			pulled = refStr
			return nopPullResponse{io.NopCloser(strings.NewReader(""))}, nil
		},
		imageInspectFn: func(ctx context.Context, imageID string, inspectOpts ...client.ImageInspectOption) (client.ImageInspectResult, error) {
			return client.ImageInspectResult{
				InspectResponse: dockerimage.InspectResponse{
					RepoDigests: []string{"ghcr.io/noosxe/runnero@sha256:digest-abc"},
				},
			}, nil
		},
	}

	cli, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	if err := cli.PullImage(ctx, "ghcr.io/noosxe/runnero:latest"); err != nil {
		t.Fatalf("PullImage failed: %v", err)
	}
	if pulled != "ghcr.io/noosxe/runnero:latest" {
		t.Errorf("expected pulled image %s, got %s", "ghcr.io/noosxe/runnero:latest", pulled)
	}

	digest, err := cli.GetLocalImageDigest(ctx, "ghcr.io/noosxe/runnero:latest")
	if err != nil {
		t.Fatalf("GetLocalImageDigest failed: %v", err)
	}
	if digest != "sha256:digest-abc" {
		t.Errorf("expected sha256:digest-abc, got %s", digest)
	}
}

func TestDockerClient_ImageHandoff_InFlightJobUnchanged(t *testing.T) {
	ctx := context.Background()
	tag := "ghcr.io/noosxe/runnero:latest"
	digestV1 := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	digestV2 := "sha256:2222222222222222222222222222222222222222222222222222222222222222"

	currentDigest := digestV1

	type containerState struct {
		name    string
		imageID string
		status  string
	}
	containers := make(map[string]*containerState)

	mockAPI := &mockDockerAPI{
		containerCreateFn: func(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
			cid := "cnt-" + options.Name
			containers[cid] = &containerState{
				name:    options.Name,
				imageID: currentDigest,
				status:  "created",
			}
			return client.ContainerCreateResult{ID: cid}, nil
		},
		containerStartFn: func(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error) {
			if c, ok := containers[containerID]; ok {
				c.status = "running"
			}
			return client.ContainerStartResult{}, nil
		},
		containerStopFn: func(ctx context.Context, containerID string, options client.ContainerStopOptions) (client.ContainerStopResult, error) {
			if c, ok := containers[containerID]; ok {
				c.status = "stopped"
			}
			return client.ContainerStopResult{}, nil
		},
		containerRemoveFn: func(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
			delete(containers, containerID)
			return client.ContainerRemoveResult{}, nil
		},
		imageInspectFn: func(ctx context.Context, imageID string, inspectOpts ...client.ImageInspectOption) (client.ImageInspectResult, error) {
			return client.ImageInspectResult{
				InspectResponse: dockerimage.InspectResponse{
					RepoDigests: []string{tag + "@" + currentDigest},
				},
			}, nil
		},
		imagePullFn: func(ctx context.Context, refStr string, options client.ImagePullOptions) (client.ImagePullResponse, error) {
			if refStr == tag {
				currentDigest = digestV2
			}
			return nopPullResponse{io.NopCloser(strings.NewReader(""))}, nil
		},
	}

	cli, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	// 1. Initial spawn: Runner A on digest-v1
	cid1, err := cli.SpawnRunner(ctx, orchestrator.RunnerConfig{
		Name:     "runner-in-flight",
		PoolName: "prod-pool",
		Image:    tag,
	})
	if err != nil {
		t.Fatalf("SpawnRunner 1 failed: %v", err)
	}

	if containers[cid1].imageID != digestV1 {
		t.Fatalf("expected runner 1 to use %s, got %s", digestV1, containers[cid1].imageID)
	}
	if containers[cid1].status != "running" {
		t.Fatalf("expected runner 1 to be running, got %s", containers[cid1].status)
	}

	// 2. Image pull occurs in background (RUN-67)
	if err := cli.PullImage(ctx, tag); err != nil {
		t.Fatalf("PullImage failed: %v", err)
	}

	// Verify local digest inspection now reports digestV2
	inspectedDigest, err := cli.GetLocalImageDigest(ctx, tag)
	if err != nil {
		t.Fatalf("GetLocalImageDigest failed: %v", err)
	}
	if inspectedDigest != digestV2 {
		t.Errorf("expected local digest %s, got %s", digestV2, inspectedDigest)
	}

	// 3. Acceptance check: Active runner 1 on old image finishes its job untouched!
	if containers[cid1].imageID != digestV1 {
		t.Errorf("expected runner 1 imageID to remain %s untouched, got %s", digestV1, containers[cid1].imageID)
	}
	if containers[cid1].status != "running" {
		t.Errorf("expected runner 1 to still be running its in-flight job, got %s", containers[cid1].status)
	}

	// 4. Acceptance check: Next spawn uses new digest (digestV2)
	cid2, err := cli.SpawnRunner(ctx, orchestrator.RunnerConfig{
		Name:     "runner-next-spawn",
		PoolName: "prod-pool",
		Image:    tag,
	})
	if err != nil {
		t.Fatalf("SpawnRunner 2 failed: %v", err)
	}

	if containers[cid2].imageID != digestV2 {
		t.Errorf("expected runner 2 to use new digest %s, got %s", digestV2, containers[cid2].imageID)
	}

	// 5. In-flight job on runner 1 completes and terminates cleanly
	if err := cli.TerminateRunner(ctx, cid1); err != nil {
		t.Fatalf("TerminateRunner failed: %v", err)
	}
	if _, exists := containers[cid1]; exists {
		t.Errorf("expected runner 1 to be terminated and removed")
	}

	// Runner 2 remains active on digestV2
	if containers[cid2].status != "running" {
		t.Errorf("expected runner 2 to remain running")
	}
}
