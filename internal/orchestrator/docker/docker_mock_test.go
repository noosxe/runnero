package docker_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/orchestrator/docker"
)

func TestDockerClient_WithMockDockerAPIClient(t *testing.T) {
	ctx := context.Background()

	t.Run("Bootstrap and Ping Success", func(t *testing.T) {
		mockAPI := docker.NewMockDockerAPIClient()
		mockAPI.On("Ping", mock.Anything, mock.AnythingOfType("client.PingOptions")).Return(client.PingResult{APIVersion: "1.47"}, nil).Once()

		client, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
		require.NoError(t, err)
		require.NotNil(t, client)
		assert.False(t, client.IsDegraded())

		err = client.Ping(ctx)
		require.NoError(t, err)

		mockAPI.AssertExpectations(t)
	})

	t.Run("SpawnRunner Success and Label Verification", func(t *testing.T) {
		mockAPI := docker.NewMockDockerAPIClient()
		mockAPI.On("NetworkList", mock.Anything, mock.AnythingOfType("client.NetworkListOptions")).Return(client.NetworkListResult{
			Items: []network.Summary{
				{Network: network.Network{ID: "net-default-id", Name: orchestrator.DefaultNetworkName}},
			},
		}, nil).Once()

		expectedContainerID := "cnt-testify-123456"
		mockAPI.On("ContainerCreate",
			mock.Anything,
			mock.MatchedBy(func(opts client.ContainerCreateOptions) bool {
				cfg := opts.Config
				return cfg != nil &&
					cfg.Labels[orchestrator.LabelManaged] == "true" &&
					cfg.Labels[orchestrator.LabelPoolName] == "ci-pool" &&
					cfg.Image == "ghcr.io/actions/runner:latest"
			}),
		).Return(client.ContainerCreateResult{ID: expectedContainerID}, nil).Once()

		mockAPI.On("ContainerStart",
			mock.Anything,
			expectedContainerID,
			mock.AnythingOfType("client.ContainerStartOptions"),
		).Return(client.ContainerStartResult{}, nil).Once()

		client, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
		require.NoError(t, err)

		cfg := orchestrator.RunnerConfig{
			Name:     "runnero-ci-pool-abc12345",
			PoolName: "ci-pool",
			Image:    "ghcr.io/actions/runner:latest",
			RepoURL:  "https://github.com/org/repo",
			Token:    "mock-runner-token",
		}

		id, err := client.SpawnRunner(ctx, cfg)
		require.NoError(t, err)
		assert.Equal(t, expectedContainerID, id)

		mockAPI.AssertExpectations(t)
	})

	t.Run("SpawnRunner Create Failure Cleans Up", func(t *testing.T) {
		mockAPI := docker.NewMockDockerAPIClient()
		mockAPI.On("NetworkList", mock.Anything, mock.AnythingOfType("client.NetworkListOptions")).Return(client.NetworkListResult{
			Items: []network.Summary{
				{Network: network.Network{ID: "net-default-id", Name: orchestrator.DefaultNetworkName}},
			},
		}, nil).Once()

		createErr := errors.New("out of disk space")
		mockAPI.On("ContainerCreate",
			mock.Anything,
			mock.AnythingOfType("client.ContainerCreateOptions"),
		).Return(client.ContainerCreateResult{}, createErr).Once()

		client, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
		require.NoError(t, err)

		cfg := orchestrator.RunnerConfig{
			Name:     "runnero-ci-pool-fail",
			PoolName: "ci-pool",
			Image:    "ghcr.io/actions/runner:latest",
			RepoURL:  "https://github.com/org/repo",
			Token:    "mock-runner-token",
		}

		id, err := client.SpawnRunner(ctx, cfg)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, createErr))
		assert.Empty(t, id)

		mockAPI.AssertExpectations(t)
	})

	t.Run("TerminateRunner Stops and Removes Container", func(t *testing.T) {
		mockAPI := docker.NewMockDockerAPIClient()
		targetID := "cnt-terminate-999"
		mockAPI.On("ContainerStop", mock.Anything, targetID, mock.AnythingOfType("client.ContainerStopOptions")).Return(client.ContainerStopResult{}, nil).Once()
		mockAPI.On("ContainerRemove", mock.Anything, targetID, mock.MatchedBy(func(opts client.ContainerRemoveOptions) bool {
			return opts.Force
		})).Return(client.ContainerRemoveResult{}, nil).Once()

		client, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
		require.NoError(t, err)

		err = client.TerminateRunner(ctx, targetID)
		assert.NoError(t, err)

		mockAPI.AssertExpectations(t)
	})

	t.Run("PruneExitedContainers Calls Prune", func(t *testing.T) {
		mockAPI := docker.NewMockDockerAPIClient()
		mockAPI.On("ContainerPrune", mock.Anything, mock.MatchedBy(func(opts client.ContainerPruneOptions) bool {
			return opts.Filters != nil && opts.Filters["label"][orchestrator.LabelManaged+"=true"]
		})).Return(client.ContainerPruneResult{
			Report: container.PruneReport{
				ContainersDeleted: []string{"dead-c1", "dead-c2"},
				SpaceReclaimed:    1024 * 1024 * 50,
			},
		}, nil).Once()

		client, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
		require.NoError(t, err)

		err = client.PruneExitedContainers(ctx)
		assert.NoError(t, err)

		mockAPI.AssertExpectations(t)
	})

	t.Run("EnsureNetwork Idempotency", func(t *testing.T) {
		mockAPI := docker.NewMockDockerAPIClient()
		// Case 1: Network already exists
		mockAPI.On("NetworkList", mock.Anything, mock.AnythingOfType("client.NetworkListOptions")).Return(client.NetworkListResult{
			Items: []network.Summary{
				{Network: network.Network{ID: "net-existing-id", Name: "runnero-net"}},
			},
		}, nil).Once()

		client, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
		require.NoError(t, err)

		netID, err := client.EnsureNetwork(ctx, "runnero-net")
		require.NoError(t, err)
		assert.Equal(t, "net-existing-id", netID)

		mockAPI.AssertExpectations(t)
	})

	t.Run("AuditRunners Parses Status Correctly", func(t *testing.T) {
		mockAPI := docker.NewMockDockerAPIClient()
		spawnedTime := time.Now().Add(-10 * time.Minute).Format(time.RFC3339)
		mockAPI.On("ContainerList", mock.Anything, mock.AnythingOfType("client.ContainerListOptions")).Return(client.ContainerListResult{
			Items: []container.Summary{
				{
					ID:    "cnt-audited-1",
					Names: []string{"/runnero-pool1-0001"},
					State: "running",
					Labels: map[string]string{
						orchestrator.LabelManaged:   "true",
						orchestrator.LabelPoolName:  "pool1",
						orchestrator.LabelSpawnedAt: spawnedTime,
					},
					NetworkSettings: &container.NetworkSettingsSummary{
						Networks: map[string]*network.EndpointSettings{
							"bridge": {IPAddress: netip.MustParseAddr("172.17.0.2")},
						},
					},
				},
			},
		}, nil).Once()

		client, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
		require.NoError(t, err)

		statuses, err := client.AuditRunners(ctx)
		require.NoError(t, err)
		require.Len(t, statuses, 1)
		assert.Equal(t, "cnt-audited-1", statuses[0].ID)
		assert.Equal(t, "runnero-pool1-0001", statuses[0].Name)
		assert.Equal(t, "pool1", statuses[0].PoolName)
		assert.Equal(t, "running", statuses[0].State)
		assert.Equal(t, "172.17.0.2", statuses[0].IPAddress)

		mockAPI.AssertExpectations(t)
	})

	t.Run("Close Closes Underlying Client", func(t *testing.T) {
		mockAPI := docker.NewMockDockerAPIClient()
		mockAPI.On("Close").Return(nil).Once()

		client, err := docker.NewClient(ctx, docker.WithAPIClient(mockAPI))
		require.NoError(t, err)

		err = client.Close()
		assert.NoError(t, err)

		mockAPI.AssertExpectations(t)
	})
}
