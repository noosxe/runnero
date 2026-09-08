package orchestrator

import (
	"context"
	"errors"
	"io"
	"time"
)

const (
	// DefaultNetworkName is the supervisor-managed bridge network for runner communication (OQ #22).
	DefaultNetworkName = "runnero-supervisor"
)

// RunnerConfig defines parameters for spawning an ephemeral runner or task container.
type RunnerConfig struct {
	Name        string   `json:"name"`
	RepoURL     string   `json:"repo_url"`
	Token       string   `json:"token"`
	Labels      []string `json:"labels"`
	WorkDir     string   `json:"work_dir"`
	Image       string   `json:"image"`
	CPULimit    string   `json:"cpu_limit"`
	MemoryLimit string   `json:"memory_limit"`
	AllowDocker bool     `json:"allow_docker"`
	Env         []string `json:"env,omitempty"`
	PoolName    string   `json:"pool_name,omitempty"`
	// PoolID is the stable database identifier of the owning runner pool; it
	// survives renames, unlike PoolName (docs/22 §5.4, RUN-126).
	PoolID       int64  `json:"pool_id,omitempty"`
	DockerHostID string `json:"docker_host_id,omitempty"` // Groundwork for multi-host Docker (OQ #22)
	Network      string `json:"network,omitempty"`        // Managed bridge network name (defaults to DefaultNetworkName)
}

// RunnerStatus represents the current state of a containerized runner.
type RunnerStatus struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	PoolName string `json:"pool_name"`
	// PoolID is the owning pool's stable database id, parsed from the container's
	// pool-id label (RUN-126). Zero for containers spawned before the label
	// existed; the reconciler resolves those by name at adoption time.
	PoolID    int64     `json:"pool_id,omitempty"`
	State     string    `json:"state"` // e.g., "running", "exited", "created"
	IPAddress string    `json:"ip_address"`
	ExitCode  int       `json:"exit_code"`
	SpawnedAt time.Time `json:"spawned_at"`
	// BusySince anchors the runner-lifetime kill switch: the moment this
	// supervisor lifetime first observed the runner busy (docs/23 §4). Set once
	// at the idle→busy transition and sticky until the tracked state is dropped;
	// seeded with SpawnedAt for containers adopted already running (docs/23
	// §4.4.1). Zero means unknown — consumers fall back to SpawnedAt.
	BusySince time.Time `json:"busy_since,omitempty"`
	IsBusy    bool      `json:"is_busy,omitempty"`
	OnDemand  bool      `json:"on_demand,omitempty"`
	// ForgeID is the forge-assigned runner id observed by the busy-state
	// listing (docs/21 §5.3); zero until observed. Conclusion enrichment
	// keys its API calls on it.
	ForgeID   int64  `json:"forge_id,omitempty"`
	TargetURL string `json:"target_url,omitempty"`
}

// ErrLogsUnavailable is returned by CaptureLogs when the container's logs can
// no longer be fetched: the container is already removed, dead, or its removal
// is in progress. Multiple reap paths (die/destroy events, audit cycle, hung-
// runner sweep) race on the same container; the losing path observes this
// error and must treat it as benign rather than warning (RUN-121).
var ErrLogsUnavailable = errors.New("container logs unavailable: already removed or removal in progress")

// ContainerProvider abstracts container lifecycle operations from the underlying container engine.
// See docs/02-architecture-design.md §3.1.
type ContainerProvider interface {
	// SpawnRunner creates and starts an ephemeral runner container.
	SpawnRunner(ctx context.Context, config RunnerConfig) (string, error)

	// SpawnTask creates and starts a one-off task container (e.g., Renovate bot).
	SpawnTask(ctx context.Context, config RunnerConfig) (string, error)

	// TerminateRunner gracefully stops and removes a runner container.
	TerminateRunner(ctx context.Context, containerID string) error

	// AuditRunners inspects all active and exited supervisor-managed runner containers.
	AuditRunners(ctx context.Context) ([]RunnerStatus, error)

	// PruneExitedContainers removes containers that have finished executing.
	PruneExitedContainers(ctx context.Context) error

	// EnsureNetwork verifies that the specified bridge network exists, creating it if needed.
	EnsureNetwork(ctx context.Context, name string) (string, error)

	// CaptureLogs reads full logs from containerID and writes them gzipped to DATA_DIR/logs/<runner-id>.log.jsonl.gz.
	CaptureLogs(ctx context.Context, containerID, dataDir string) (string, error)

	// StreamLogs opens a live follow log stream for containerID via Docker Logs API (OQ #14, #20, #30).
	StreamLogs(ctx context.Context, containerID string) (io.ReadCloser, error)

	// Ping checks connectivity with the container engine daemon.
	Ping(ctx context.Context) error

	// Close releases any allocated resources or connections.
	Close() error
}
