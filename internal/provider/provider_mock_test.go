package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/noosxe/runnero/internal/provider"
)

func TestMockGitProvider_ExpectationsAndAssertions(t *testing.T) {
	ctx := context.Background()

	t.Run("GetRegistrationToken success and error", func(t *testing.T) {
		mockP := provider.NewMockGitProvider()
		mockP.On("GetRegistrationToken", mock.Anything, provider.ScopeRepo, "https://github.com/org/repo").
			Return("token-xyz", nil).Once()

		tok, err := mockP.GetRegistrationToken(ctx, provider.ScopeRepo, "https://github.com/org/repo")
		require.NoError(t, err)
		assert.Equal(t, "token-xyz", tok)

		mockP.On("GetRegistrationToken", mock.Anything, provider.ScopeOrg, "https://github.com/org").
			Return("", errors.New("forbidden")).Once()

		_, err = mockP.GetRegistrationToken(ctx, provider.ScopeOrg, "https://github.com/org")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "forbidden")

		mockP.AssertExpectations(t)
	})

	t.Run("ValidateCredentials", func(t *testing.T) {
		mockP := provider.NewMockGitProvider()
		mockP.On("ValidateCredentials", mock.Anything).Return(nil).Once()

		err := mockP.ValidateCredentials(ctx)
		assert.NoError(t, err)

		mockP.AssertExpectations(t)
	})

	t.Run("ScalingMode and PollQueuedJobs", func(t *testing.T) {
		mockP := provider.NewMockGitProvider()
		mockP.On("ScalingMode").Return(provider.ScalingPolling).Once()
		mockP.On("PollQueuedJobs", mock.Anything, provider.PollTarget{URL: "https://forgejo.org/repo"}).Return(3, nil).Once()

		mode := mockP.ScalingMode()
		assert.Equal(t, provider.ScalingPolling, mode)

		jobs, err := mockP.PollQueuedJobs(ctx, provider.PollTarget{URL: "https://forgejo.org/repo"})
		require.NoError(t, err)
		assert.Equal(t, 3, jobs)

		mockP.AssertExpectations(t)
	})

	t.Run("RunnerDeregistrar and RenovateTokenProvider", func(t *testing.T) {
		mockP := provider.NewMockGitProvider()
		mockP.On("DeregisterRunner", mock.Anything, provider.ScopeRepo, "https://github.com/org/repo", "runner-1").
			Return(nil).Once()
		mockP.On("GetRenovateToken", mock.Anything, "https://github.com/org/repo").
			Return("renovate-gh-tok", nil).Once()

		var deregistrar provider.RunnerDeregistrar = mockP
		var renoProvider provider.RenovateTokenProvider = mockP

		err := deregistrar.DeregisterRunner(ctx, provider.ScopeRepo, "https://github.com/org/repo", "runner-1")
		assert.NoError(t, err)

		renoTok, err := renoProvider.GetRenovateToken(ctx, "https://github.com/org/repo")
		require.NoError(t, err)
		assert.Equal(t, "renovate-gh-tok", renoTok)

		mockP.AssertExpectations(t)
	})
}
