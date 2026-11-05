package docker_test

import (
	"context"
	"errors"
	"io"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/orchestrator/docker"
)

const missingTestImage = "ghcr.io/noosxe/runnero:latest"

// fakePullResponse is a client.ImagePullResponse whose Wait returns waitErr,
// standing in for a daemon that reports a pull failure in-stream.
type fakePullResponse struct {
	io.Reader
	waitErr error
}

func (f *fakePullResponse) Close() error { return nil }

func (f *fakePullResponse) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(func(jsonstream.Message, error) bool) {}
}

func (f *fakePullResponse) Wait(context.Context) error { return f.waitErr }

func newPullResponse(waitErr error) client.ImagePullResponse {
	return &fakePullResponse{Reader: strings.NewReader(""), waitErr: waitErr}
}

func noSuchImage() error {
	return cerrdefs.ErrNotFound.WithMessage("No such image: " + missingTestImage)
}

func expectDefaultNetwork(m *docker.MockDockerAPIClient) {
	m.On("NetworkList", mock.Anything, mock.Anything).Return(client.NetworkListResult{
		Items: []network.Summary{
			{Network: network.Network{ID: "net-default-id", Name: orchestrator.DefaultNetworkName}},
		},
	}, nil)
}

func missingImageConfig(name string) orchestrator.RunnerConfig {
	return orchestrator.RunnerConfig{
		Name:     name,
		PoolName: "kraken-runners",
		Image:    missingTestImage,
		RepoURL:  "https://github.com/org/repo",
		Token:    "mock-runner-token",
	}
}

func TestSpawnRunner_PullsMissingImageThenCreates(t *testing.T) {
	ctx := context.Background()
	m := docker.NewMockDockerAPIClient()
	expectDefaultNetwork(m)

	m.On("ContainerCreate", mock.Anything, mock.Anything).
		Return(client.ContainerCreateResult{}, noSuchImage()).Once()
	m.On("ImageInspect", mock.Anything, missingTestImage, mock.Anything).
		Return(client.ImageInspectResult{}, noSuchImage()).Once()
	m.On("ImagePull", mock.Anything, missingTestImage, mock.Anything).
		Return(newPullResponse(nil), nil).Once()
	m.On("ContainerCreate", mock.Anything, mock.Anything).
		Return(client.ContainerCreateResult{ID: "cnt-after-pull"}, nil).Once()
	m.On("ContainerStart", mock.Anything, "cnt-after-pull", mock.Anything).
		Return(client.ContainerStartResult{}, nil).Once()

	c, err := docker.NewClient(ctx, docker.WithAPIClient(m))
	require.NoError(t, err)

	id, err := c.SpawnRunner(ctx, missingImageConfig("runnero-kraken-runners-a"))
	require.NoError(t, err)
	assert.Equal(t, "cnt-after-pull", id)
	m.AssertExpectations(t)
}

func TestSpawnRunner_PresentImageIsNotPulled(t *testing.T) {
	ctx := context.Background()
	m := docker.NewMockDockerAPIClient()
	expectDefaultNetwork(m)

	m.On("ContainerCreate", mock.Anything, mock.Anything).
		Return(client.ContainerCreateResult{ID: "cnt-present"}, nil).Once()
	m.On("ContainerStart", mock.Anything, "cnt-present", mock.Anything).
		Return(client.ContainerStartResult{}, nil).Once()

	c, err := docker.NewClient(ctx, docker.WithAPIClient(m))
	require.NoError(t, err)

	_, err = c.SpawnRunner(ctx, missingImageConfig("runnero-kraken-runners-b"))
	require.NoError(t, err)
	m.AssertNotCalled(t, "ImageInspect", mock.Anything, mock.Anything, mock.Anything)
	m.AssertNotCalled(t, "ImagePull", mock.Anything, mock.Anything, mock.Anything)
	m.AssertExpectations(t)
}

// A 404 from create whose image is present (e.g. a vanished network) must
// not trigger a pull: the original create error is returned as-is.
func TestSpawnRunner_NotFoundWithPresentImageIsNotPulled(t *testing.T) {
	ctx := context.Background()
	m := docker.NewMockDockerAPIClient()
	expectDefaultNetwork(m)

	netErr := cerrdefs.ErrNotFound.WithMessage("network runnero-net not found")
	m.On("ContainerCreate", mock.Anything, mock.Anything).
		Return(client.ContainerCreateResult{}, netErr).Once()
	m.On("ImageInspect", mock.Anything, missingTestImage, mock.Anything).
		Return(client.ImageInspectResult{}, nil).Once()

	c, err := docker.NewClient(ctx, docker.WithAPIClient(m))
	require.NoError(t, err)

	_, err = c.SpawnRunner(ctx, missingImageConfig("runnero-kraken-runners-c"))
	require.Error(t, err)
	assert.ErrorIs(t, err, netErr)
	assert.NotErrorIs(t, err, orchestrator.ErrImagePullFailed)
	m.AssertNotCalled(t, "ImagePull", mock.Anything, mock.Anything, mock.Anything)
	m.AssertExpectations(t)
}

// A create that misses the image while a concurrent spawn's pull lands
// before the inspect sees the image present: the create is retried once
// instead of failing the spawn with the stale "No such image".
func TestSpawnRunner_ImageLandsBetweenCreateAndInspectRetriesCreate(t *testing.T) {
	ctx := context.Background()
	m := docker.NewMockDockerAPIClient()
	expectDefaultNetwork(m)

	m.On("ContainerCreate", mock.Anything, mock.Anything).
		Return(client.ContainerCreateResult{}, noSuchImage()).Once()
	m.On("ImageInspect", mock.Anything, missingTestImage, mock.Anything).
		Return(client.ImageInspectResult{}, nil).Once()
	m.On("ContainerCreate", mock.Anything, mock.Anything).
		Return(client.ContainerCreateResult{ID: "cnt-after-race"}, nil).Once()
	m.On("ContainerStart", mock.Anything, "cnt-after-race", mock.Anything).
		Return(client.ContainerStartResult{}, nil).Once()

	c, err := docker.NewClient(ctx, docker.WithAPIClient(m))
	require.NoError(t, err)

	id, err := c.SpawnRunner(ctx, missingImageConfig("runnero-kraken-runners-g"))
	require.NoError(t, err)
	assert.Equal(t, "cnt-after-race", id)
	m.AssertNotCalled(t, "ImagePull", mock.Anything, mock.Anything, mock.Anything)
	m.AssertExpectations(t)
}

func TestSpawnRunner_MissingImagePullFailure(t *testing.T) {
	tests := []struct {
		name     string
		pullResp client.ImagePullResponse
		pullErr  error
	}{
		{
			name:    "request rejected",
			pullErr: cerrdefs.ErrUnauthenticated.WithMessage("unauthorized: authentication required"),
		},
		{
			name:     "in-stream error",
			pullResp: newPullResponse(errors.New("manifest unknown")),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			m := docker.NewMockDockerAPIClient()
			expectDefaultNetwork(m)

			m.On("ContainerCreate", mock.Anything, mock.Anything).
				Return(client.ContainerCreateResult{}, noSuchImage()).Once()
			m.On("ImageInspect", mock.Anything, missingTestImage, mock.Anything).
				Return(client.ImageInspectResult{}, noSuchImage()).Once()
			var resp any
			if tt.pullResp != nil {
				resp = tt.pullResp
			}
			m.On("ImagePull", mock.Anything, missingTestImage, mock.Anything).
				Return(resp, tt.pullErr).Once()

			c, err := docker.NewClient(ctx, docker.WithAPIClient(m))
			require.NoError(t, err)

			_, err = c.SpawnRunner(ctx, missingImageConfig("runnero-kraken-runners-d"))
			require.Error(t, err)
			assert.ErrorIs(t, err, orchestrator.ErrImagePullFailed)

			code, _ := orchestrator.ClassifyReconcileError(err)
			assert.Equal(t, orchestrator.ErrCodeImagePullFailed, code)
			m.AssertNumberOfCalls(t, "ContainerCreate", 1)
			m.AssertExpectations(t)
		})
	}
}

// fakeImageHost models a daemon where the image appears once a pull
// completes; pulls block until release is closed.
type fakeImageHost struct {
	*docker.MockDockerAPIClient

	mu      sync.Mutex
	present bool
	pulls   int
	created int
	release chan struct{}
}

func (f *fakeImageHost) ContainerCreate(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.present {
		return client.ContainerCreateResult{}, noSuchImage()
	}
	f.created++
	return client.ContainerCreateResult{ID: "cnt"}, nil
}

func (f *fakeImageHost) ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error) {
	return client.ContainerStartResult{}, nil
}

func (f *fakeImageHost) ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.present {
		return client.ImageInspectResult{}, noSuchImage()
	}
	return client.ImageInspectResult{}, nil
}

func (f *fakeImageHost) ImagePull(context.Context, string, client.ImagePullOptions) (client.ImagePullResponse, error) {
	f.mu.Lock()
	f.pulls++
	f.mu.Unlock()
	<-f.release
	f.mu.Lock()
	f.present = true
	f.mu.Unlock()
	return newPullResponse(nil), nil
}

func TestSpawnRunner_ConcurrentMissingImageSpawnsPullOnce(t *testing.T) {
	ctx := context.Background()
	host := &fakeImageHost{MockDockerAPIClient: docker.NewMockDockerAPIClient(), release: make(chan struct{})}
	expectDefaultNetwork(host.MockDockerAPIClient)

	c, err := docker.NewClient(ctx, docker.WithAPIClient(host))
	require.NoError(t, err)

	const spawns = 5
	var wg sync.WaitGroup
	errs := make(chan error, spawns)
	for i := range spawns {
		wg.Go(func() {
			_, err := c.SpawnRunner(ctx, missingImageConfig("runnero-kraken-runners-"+string(rune('a'+i))))
			errs <- err
		})
	}

	require.Eventually(t, func() bool { return c.PullWaiters(missingTestImage) == spawns },
		5*time.Second, 5*time.Millisecond, "all spawns should join the one in-flight pull")
	close(host.release)
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, 1, host.pulls)
	assert.Equal(t, spawns, host.created)
}

// A spawn whose own context ends while the shared pull is still running
// gives up with ErrImagePullFailed, without aborting the pull.
func TestSpawnRunner_MissingImageWaitHonorsCallerContext(t *testing.T) {
	host := &fakeImageHost{MockDockerAPIClient: docker.NewMockDockerAPIClient(), release: make(chan struct{})}
	expectDefaultNetwork(host.MockDockerAPIClient)

	c, err := docker.NewClient(context.Background(), docker.WithAPIClient(host))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = c.SpawnRunner(ctx, missingImageConfig("runnero-kraken-runners-e"))
	require.ErrorIs(t, err, orchestrator.ErrImagePullFailed)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	// The detached pull is still in flight; a later spawn joins it.
	close(host.release)
	_, err = c.SpawnRunner(context.Background(), missingImageConfig("runnero-kraken-runners-f"))
	require.NoError(t, err)
	host.mu.Lock()
	defer host.mu.Unlock()
	assert.Equal(t, 1, host.pulls)
}
