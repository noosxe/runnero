package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/server"
)

var (
	// ErrNilClient is returned if a nil docker client is provided.
	ErrNilClient = errors.New("docker client cannot be nil")
	// ErrDaemonUnreachable is returned when pinging the Docker daemon fails.
	ErrDaemonUnreachable = errors.New("docker daemon unreachable")
)

// APIClient abstracts the Docker Engine SDK methods utilized by the orchestrator.
type APIClient interface {
	Ping(ctx context.Context, options client.PingOptions) (client.PingResult, error)
	Close() error
	ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerStop(ctx context.Context, containerID string, options client.ContainerStopOptions) (client.ContainerStopResult, error)
	ContainerRemove(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerLogs(ctx context.Context, containerID string, options client.ContainerLogsOptions) (client.ContainerLogsResult, error)
	ContainerPrune(ctx context.Context, opts client.ContainerPruneOptions) (client.ContainerPruneResult, error)
	Events(ctx context.Context, options client.EventsListOptions) client.EventsResult
	NetworkList(ctx context.Context, options client.NetworkListOptions) (client.NetworkListResult, error)
	NetworkCreate(ctx context.Context, name string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error)
	NetworkConnect(ctx context.Context, networkID string, options client.NetworkConnectOptions) (client.NetworkConnectResult, error)
	ImageInspect(ctx context.Context, imageID string, inspectOpts ...client.ImageInspectOption) (client.ImageInspectResult, error)
	ImagePull(ctx context.Context, refStr string, options client.ImagePullOptions) (client.ImagePullResponse, error)
}

// Client implements orchestrator.ContainerProvider using the Docker Engine API.
type Client struct {
	docker        APIClient
	host          string
	dockerHostID  string // Groundwork for multi-host Docker pools (OQ #22)
	healthTracker *orchestrator.DockerHealthTracker
	mu            sync.RWMutex
}

// Option configures the Docker orchestrator Client.
type Option func(*options)

type options struct {
	host          string
	certPath      string
	verifyTLS     bool
	dockerHostID  string
	apiClient     APIClient
	healthTracker *orchestrator.DockerHealthTracker
	alertHandler  func(orchestrator.DockerAlert)
}

// WithHost configures a custom Docker host endpoint (e.g. unix:///var/run/docker.sock or tcp://remote:2376).
func WithHost(host string) Option {
	return func(o *options) {
		o.host = host
	}
}

// WithTLS configures TLS client authentication for a remote Docker daemon.
func WithTLS(certPath string, verify bool) Option {
	return func(o *options) {
		o.certPath = certPath
		o.verifyTLS = verify
	}
}

// WithDockerHostID sets the identifier for this Docker host instance (OQ #22).
func WithDockerHostID(id string) Option {
	return func(o *options) {
		o.dockerHostID = id
	}
}

// WithAPIClient injects a custom or mock Docker API client.
func WithAPIClient(apiClient APIClient) Option {
	return func(o *options) {
		o.apiClient = apiClient
	}
}

// WithHealthTracker injects a custom DockerHealthTracker.
func WithHealthTracker(tracker *orchestrator.DockerHealthTracker) Option {
	return func(o *options) {
		o.healthTracker = tracker
	}
}

// WithAlertHandler configures an alert callback for when Docker is down > 5m (OQ #5).
func WithAlertHandler(handler func(orchestrator.DockerAlert)) Option {
	return func(o *options) {
		o.alertHandler = handler
	}
}

// NewClient initializes a new Docker orchestrator client.
// Honors standard Docker environment variables (DOCKER_HOST, DOCKER_TLS_VERIFY, DOCKER_CERT_PATH)
// unless overridden by options.
func NewClient(ctx context.Context, opts ...Option) (*Client, error) {
	cfg := options{
		host:         os.Getenv("DOCKER_HOST"),
		certPath:     os.Getenv("DOCKER_CERT_PATH"),
		verifyTLS:    os.Getenv("DOCKER_TLS_VERIFY") != "",
		dockerHostID: "default",
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	tracker := cfg.healthTracker
	if tracker == nil {
		tracker = orchestrator.NewDockerHealthTracker(cfg.dockerHostID, cfg.alertHandler)
	}

	if cfg.apiClient != nil {
		return &Client{
			docker:        cfg.apiClient,
			host:          cfg.host,
			dockerHostID:  cfg.dockerHostID,
			healthTracker: tracker,
		}, nil
	}

	var clientOpts []client.Opt
	clientOpts = append(clientOpts, client.FromEnv)

	if cfg.host != "" {
		clientOpts = append(clientOpts, client.WithHost(cfg.host))
	}
	if cfg.certPath != "" {
		caCert := fmt.Sprintf("%s/ca.pem", strings.TrimRight(cfg.certPath, "/"))
		cert := fmt.Sprintf("%s/cert.pem", strings.TrimRight(cfg.certPath, "/"))
		key := fmt.Sprintf("%s/key.pem", strings.TrimRight(cfg.certPath, "/"))
		clientOpts = append(clientOpts, client.WithTLSClientConfig(caCert, cert, key))
	}

	cli, err := client.New(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("initializing Docker SDK client: %w", err)
	}

	return &Client{
		docker:        cli,
		host:          cfg.host,
		dockerHostID:  cfg.dockerHostID,
		healthTracker: tracker,
	}, nil
}

// Ping verifies Docker daemon reachability.
func (c *Client) Ping(ctx context.Context) error {
	c.mu.RLock()
	docker := c.docker
	c.mu.RUnlock()

	if docker == nil {
		return ErrNilClient
	}

	_, err := docker.Ping(ctx, client.PingOptions{})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDaemonUnreachable, err)
	}
	return nil
}

// Close releases the Docker client connections.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.docker != nil {
		return c.docker.Close()
	}
	return nil
}

// SpawnRunner creates and starts an ephemeral runner container.
func (c *Client) SpawnRunner(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
	return c.spawn(ctx, config, orchestrator.TaskTypeRunner)
}

// SpawnTask creates and starts a one-off task container (e.g. Renovate bot).
func (c *Client) SpawnTask(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
	return c.spawn(ctx, config, orchestrator.TaskTypeJob)
}

func (c *Client) spawn(ctx context.Context, config orchestrator.RunnerConfig, taskType string) (string, error) {
	c.mu.RLock()
	docker := c.docker
	tracker := c.healthTracker
	c.mu.RUnlock()

	if docker == nil {
		return "", ErrNilClient
	}

	if tracker != nil {
		if err := tracker.CanSpawn(); err != nil {
			return "", err
		}
	}

	containerName := config.Name
	if containerName == "" {
		containerName = orchestrator.GenerateContainerName(config.PoolName)
	}

	image := config.Image
	if image == "" {
		image = orchestrator.DefaultRunnerImage
	}

	labels := map[string]string{
		orchestrator.LabelManaged:   "true",
		orchestrator.LabelPoolName:  config.PoolName,
		orchestrator.LabelID:        containerName,
		orchestrator.LabelSpawnedAt: time.Now().UTC().Format(time.RFC3339),
		orchestrator.LabelTaskType:  taskType,
	}
	if config.RepoURL != "" {
		labels[orchestrator.LabelTargetURL] = config.RepoURL
	}

	env := config.Env
	if len(env) == 0 {
		if taskType == orchestrator.TaskTypeRunner {
			workDir := config.WorkDir
			if workDir == "" {
				workDir = "_work"
			}
			env = []string{
				"RUNNER_NAME=" + containerName,
				"RUNNER_TOKEN=" + config.Token,
				"RUNNER_WORKDIR=" + workDir,
				"RUNNER_LABELS=" + strings.Join(config.Labels, ","),
				"RUNNER_EPHEMERAL=true",
			}
			if config.RepoURL != "" {
				if strings.Contains(config.RepoURL, "gitea") {
					env = append(env, "GITEA_INSTANCE_URL="+config.RepoURL)
				} else if strings.Contains(config.RepoURL, "forgejo") {
					env = append(env, "FORGEJO_INSTANCE_URL="+config.RepoURL)
				} else {
					env = append(env, "GITHUB_REPOSITORY_URL="+config.RepoURL)
				}
			}
		} else {
			env = []string{
				"RENOVATE_TOKEN=" + config.Token,
				"RENOVATE_ENDPOINT=" + config.RepoURL,
			}
		}
	}

	hostConfig := &container.HostConfig{}

	if config.AllowDocker {
		hostConfig.Binds = append(hostConfig.Binds, "/var/run/docker.sock:/var/run/docker.sock")
	}

	if config.CPULimit != "" {
		nanoCPUs, err := ParseCPULimit(config.CPULimit)
		if err != nil {
			return "", err
		}
		hostConfig.NanoCPUs = nanoCPUs
	}

	if config.MemoryLimit != "" {
		memBytes, err := ParseMemoryLimit(config.MemoryLimit)
		if err != nil {
			return "", err
		}
		hostConfig.Memory = memBytes
	}

	containerConfig := &container.Config{
		Image:  image,
		Env:    env,
		Labels: labels,
	}

	networkName := config.Network
	if networkName == "" {
		networkName = orchestrator.DefaultNetworkName
	}

	var networkingConfig *network.NetworkingConfig
	if networkName != "none" {
		if _, err := c.EnsureNetwork(ctx, networkName); err != nil {
			return "", fmt.Errorf("ensuring network %q: %w", networkName, err)
		}
		networkingConfig = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkName: {},
			},
		}
	}

	createResp, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           containerConfig,
		HostConfig:       hostConfig,
		NetworkingConfig: networkingConfig,
		Name:             containerName,
	})
	if err != nil {
		return "", fmt.Errorf("creating container %q: %w", containerName, err)
	}

	if _, err := docker.ContainerStart(ctx, createResp.ID, client.ContainerStartOptions{}); err != nil {
		_, _ = docker.ContainerRemove(ctx, createResp.ID, client.ContainerRemoveOptions{Force: true})
		return "", fmt.Errorf("starting container %q (%s): %w", containerName, createResp.ID, err)
	}

	return createResp.ID, nil
}

// ParseCPULimit converts CPU limits like "2.0", "0.5", "1" to NanoCPUs (int64).
func ParseCPULimit(cpu string) (int64, error) {
	trimmed := strings.TrimSpace(cpu)
	if trimmed == "" {
		return 0, nil
	}
	val, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid cpu limit %q: %w", cpu, err)
	}
	if val <= 0 {
		return 0, fmt.Errorf("cpu limit must be positive: %q", cpu)
	}
	return int64(val * 1e9), nil
}

// ParseMemoryLimit parses strings like "4g", "512m", "1024k" to byte count (int64).
func ParseMemoryLimit(mem string) (int64, error) {
	s := strings.TrimSpace(strings.ToLower(mem))
	if s == "" {
		return 0, nil
	}
	multiplier := int64(1)
	switch {
	case strings.HasSuffix(s, "g") || strings.HasSuffix(s, "gb"):
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "b"), "g")
	case strings.HasSuffix(s, "m") || strings.HasSuffix(s, "mb"):
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "b"), "m")
	case strings.HasSuffix(s, "k") || strings.HasSuffix(s, "kb"):
		multiplier = 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "b"), "k")
	case strings.HasSuffix(s, "b"):
		s = strings.TrimSuffix(s, "b")
	}

	val, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory limit %q: %w", mem, err)
	}
	if val <= 0 {
		return 0, fmt.Errorf("memory limit must be positive: %q", mem)
	}
	return int64(val * float64(multiplier)), nil
}

// TerminateRunner gracefully stops and removes a runner container.
func (c *Client) TerminateRunner(ctx context.Context, containerID string) error {
	c.mu.RLock()
	docker := c.docker
	c.mu.RUnlock()

	if docker == nil {
		return ErrNilClient
	}

	timeoutSec := 10
	stopOpts := client.ContainerStopOptions{
		Timeout: &timeoutSec,
	}

	// Graceful stop with fallback
	if _, err := docker.ContainerStop(ctx, containerID, stopOpts); err != nil {
		if !cerrdefs.IsNotFound(err) {
			_, _ = docker.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
			return fmt.Errorf("stopping container %s: %w", containerID, err)
		}
	}

	// Remove container
	if _, err := docker.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true}); err != nil {
		if !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("removing container %s: %w", containerID, err)
		}
	}

	return nil
}

// AuditRunners inspects all active and exited supervisor-managed runner containers
// by querying the Docker daemon for the com.runnero.managed=true label.
func (c *Client) AuditRunners(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
	c.mu.RLock()
	docker := c.docker
	c.mu.RUnlock()

	if docker == nil {
		return nil, ErrNilClient
	}

	filterArgs := make(client.Filters).Add("label", orchestrator.LabelManaged+"=true")

	res, err := docker.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return nil, fmt.Errorf("listing supervisor containers: %w", err)
	}

	statuses := make([]orchestrator.RunnerStatus, 0, len(res.Items))
	for _, cnt := range res.Items {
		name := ""
		if len(cnt.Names) > 0 {
			name = strings.TrimPrefix(cnt.Names[0], "/")
		}

		poolName := cnt.Labels[orchestrator.LabelPoolName]

		var spawnedAt time.Time
		if rawSpawned := cnt.Labels[orchestrator.LabelSpawnedAt]; rawSpawned != "" {
			if t, err := time.Parse(time.RFC3339, rawSpawned); err == nil {
				spawnedAt = t
			}
		}
		if spawnedAt.IsZero() {
			spawnedAt = time.Unix(cnt.Created, 0)
		}

		ipAddress := ""
		if cnt.NetworkSettings != nil && len(cnt.NetworkSettings.Networks) > 0 {
			for _, net := range cnt.NetworkSettings.Networks {
				if net.IPAddress.IsValid() {
					ipAddress = net.IPAddress.String()
					break
				}
			}
		}

		statuses = append(statuses, orchestrator.RunnerStatus{
			ID:        cnt.ID,
			Name:      name,
			PoolName:  poolName,
			State:     string(cnt.State),
			IPAddress: ipAddress,
			SpawnedAt: spawnedAt,
			TargetURL: cnt.Labels[orchestrator.LabelTargetURL],
		})
	}

	return statuses, nil
}

// PruneExitedContainers removes containers that have finished executing.
func (c *Client) PruneExitedContainers(ctx context.Context) error {
	c.mu.RLock()
	docker := c.docker
	c.mu.RUnlock()

	if docker == nil {
		return ErrNilClient
	}

	filterArgs := make(client.Filters).Add("label", orchestrator.LabelManaged+"=true")

	_, err := docker.ContainerPrune(ctx, client.ContainerPruneOptions{Filters: filterArgs})
	if err != nil {
		return fmt.Errorf("pruning exited containers: %w", err)
	}
	return nil
}

// Events streams events from the Docker daemon matching the supplied options.
func (c *Client) Events(ctx context.Context, options client.EventsListOptions) client.EventsResult {
	c.mu.RLock()
	docker := c.docker
	c.mu.RUnlock()

	if docker == nil {
		errCh := make(chan error, 1)
		errCh <- ErrNilClient
		close(errCh)
		msgCh := make(chan events.Message)
		close(msgCh)
		return client.EventsResult{Messages: msgCh, Err: errCh}
	}

	return docker.Events(ctx, options)
}

// EnsureNetwork verifies that the specified bridge network exists, creating it if needed.
// Idempotent across supervisor restarts and concurrent invocations (OQ #22).
func (c *Client) EnsureNetwork(ctx context.Context, name string) (string, error) {
	c.mu.RLock()
	docker := c.docker
	c.mu.RUnlock()

	if docker == nil {
		return "", ErrNilClient
	}

	if name == "" {
		name = orchestrator.DefaultNetworkName
	}

	filterArgs := make(client.Filters).Add("name", "^"+name+"$")

	res, err := docker.NetworkList(ctx, client.NetworkListOptions{
		Filters: filterArgs,
	})
	if err == nil {
		for _, net := range res.Items {
			if net.Name == name {
				return net.ID, nil
			}
		}
	}

	createOpts := client.NetworkCreateOptions{
		Driver: "bridge",
		Labels: map[string]string{
			orchestrator.LabelManaged: "true",
		},
	}

	resp, err := docker.NetworkCreate(ctx, name, createOpts)
	if err != nil {
		if listRes, listErr := docker.NetworkList(ctx, client.NetworkListOptions{Filters: filterArgs}); listErr == nil {
			for _, net := range listRes.Items {
				if net.Name == name {
					return net.ID, nil
				}
			}
		}
		return "", fmt.Errorf("creating managed network %q: %w", name, err)
	}

	return resp.ID, nil
}

// DockerHostID returns the configured Docker host identifier.
func (c *Client) DockerHostID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dockerHostID
}

// HealthTracker returns the Docker health tracker instance.
func (c *Client) HealthTracker() *orchestrator.DockerHealthTracker {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.healthTracker
}

// IsDegraded reports whether the client has entered degraded mode due to an unreachable daemon.
func (c *Client) IsDegraded() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.healthTracker == nil {
		return false
	}
	return c.healthTracker.IsDegraded()
}

// ReadinessCheck adapts the Docker client's reachability into a server.Check probe.
// When the daemon is unreachable, the probe reports server.StatusDegraded, allowing
// the supervisor to remain ready to serve while pausing container spawns (OQ #19).
func (c *Client) ReadinessCheck() server.Check {
	return server.NewCheck("docker", func(ctx context.Context) server.Status {
		if c.IsDegraded() {
			return server.StatusDegraded
		}
		if err := c.Ping(ctx); err != nil {
			return server.StatusDegraded
		}
		return server.StatusOK
	})
}

// StartHealthMonitor starts periodic daemon reachability checks.
// Retries periodically without crashing, transitions in and out of degraded mode,
// and raises alerts if down > 5m (OQ #5, OQ #19).
func (c *Client) StartHealthMonitor(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = orchestrator.DefaultMonitorInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial check
	c.checkHealth(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.checkHealth(ctx)
		}
	}
}

func (c *Client) checkHealth(ctx context.Context) {
	err := c.Ping(ctx)
	if err != nil {
		c.healthTracker.RecordFailure(err)
	} else {
		c.healthTracker.RecordSuccess()
	}
}

// CaptureLogs reads full logs from containerID via Docker Logs API, compresses them into
// DATA_DIR/logs/<runner-id>.log.jsonl.gz, and returns the path to the compressed file (OQ #14, #20).
func (c *Client) CaptureLogs(ctx context.Context, containerID, dataDir string) (string, error) {
	c.mu.RLock()
	docker := c.docker
	c.mu.RUnlock()

	if docker == nil {
		return "", ErrNilClient
	}

	opts := client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
	}

	logStream, err := docker.ContainerLogs(ctx, containerID, opts)
	if err != nil {
		return "", fmt.Errorf("fetching container logs for %s: %w", containerID, err)
	}
	defer func() { _ = logStream.Close() }()

	destPath := orchestrator.LogPath(dataDir, containerID)
	if _, err := orchestrator.CaptureAndCompressLogs(logStream, destPath); err != nil {
		return "", fmt.Errorf("compressing logs to %s: %w", destPath, err)
	}

	return destPath, nil
}

// StreamLogs opens a live follow log stream for containerID via Docker Logs API (OQ #14, #20, #30).
func (c *Client) StreamLogs(ctx context.Context, containerID string) (io.ReadCloser, error) {
	c.mu.RLock()
	docker := c.docker
	c.mu.RUnlock()

	if docker == nil {
		return nil, ErrNilClient
	}

	opts := client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: true,
	}

	stream, err := docker.ContainerLogs(ctx, containerID, opts)
	if err != nil {
		return nil, fmt.Errorf("opening follow log stream for %s: %w", containerID, err)
	}
	return stream, nil
}

// PullImage downloads an image using the Docker daemon (M10, RUN-66, RUN-67).
func (c *Client) PullImage(ctx context.Context, img string) error {
	c.mu.RLock()
	docker := c.docker
	c.mu.RUnlock()

	if docker == nil {
		return ErrNilClient
	}

	rc, err := docker.ImagePull(ctx, img, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pulling image %s: %w", img, err)
	}
	defer func() { _ = rc.Close() }()

	_, _ = io.Copy(io.Discard, rc)
	return nil
}

// GetLocalImageDigest retrieves the image digest or ID for an image present on the Docker host (M10, RUN-66).
func (c *Client) GetLocalImageDigest(ctx context.Context, img string) (string, error) {
	c.mu.RLock()
	docker := c.docker
	c.mu.RUnlock()

	if docker == nil {
		return "", ErrNilClient
	}

	resp, err := docker.ImageInspect(ctx, img)
	if err != nil {
		return "", fmt.Errorf("inspecting local image %s: %w", img, err)
	}

	if len(resp.RepoDigests) > 0 {
		for _, rd := range resp.RepoDigests {
			if idx := strings.LastIndex(rd, "@"); idx != -1 {
				return rd[idx+1:], nil
			}
		}
		return resp.RepoDigests[0], nil
	}
	if resp.Descriptor != nil && resp.Descriptor.Digest != "" {
		return string(resp.Descriptor.Digest), nil
	}
	return resp.ID, nil
}

var _ orchestrator.ContainerProvider = (*Client)(nil)
var _ server.ImagePuller = (*Client)(nil)
var _ server.LocalImageInspector = (*Client)(nil)
