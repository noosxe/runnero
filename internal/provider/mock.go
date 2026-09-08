package provider

import (
	"context"

	"github.com/stretchr/testify/mock"
)

// MockProvider provides a mock implementation of GitProvider for testing.
type MockProvider struct {
	RegistrationTokenFn     func(ctx context.Context, scope RegistrationScope, targetURL string) (string, error)
	ValidateCredentialsFn   func(ctx context.Context) error
	ScalingModeFn           func() ScalingMode
	PollQueuedJobsFn        func(ctx context.Context, target PollTarget) (int, error)
	DeregisterRunnerFn      func(ctx context.Context, scope RegistrationScope, targetURL, runnerName string) error
	GetRenovateTokenFn      func(ctx context.Context, targetURL string) (string, error)
	DiscoverOrganizationsFn func(ctx context.Context) ([]DiscoveredTarget, error)
	DiscoverRepositoriesFn  func(ctx context.Context) ([]DiscoveredTarget, error)
	GetAppMetadataFn        func(ctx context.Context) (string, []AppInstallation, error)
}

// GetAppMetadata delegates to GetAppMetadataFn if set, otherwise returns empty.
func (m *MockProvider) GetAppMetadata(ctx context.Context) (string, []AppInstallation, error) {
	if m.GetAppMetadataFn != nil {
		return m.GetAppMetadataFn(ctx)
	}
	return "", nil, nil
}

// DiscoverOrganizations delegates to DiscoverOrganizationsFn if set, otherwise returns nil.
func (m *MockProvider) DiscoverOrganizations(ctx context.Context) ([]DiscoveredTarget, error) {
	if m.DiscoverOrganizationsFn != nil {
		return m.DiscoverOrganizationsFn(ctx)
	}
	return nil, nil
}

// DiscoverRepositories delegates to DiscoverRepositoriesFn if set, otherwise returns nil.
func (m *MockProvider) DiscoverRepositories(ctx context.Context) ([]DiscoveredTarget, error) {
	if m.DiscoverRepositoriesFn != nil {
		return m.DiscoverRepositoriesFn(ctx)
	}
	return nil, nil
}

// GetRegistrationToken delegates to RegistrationTokenFn if set, otherwise returns a default token.
func (m *MockProvider) GetRegistrationToken(ctx context.Context, scope RegistrationScope, targetURL string) (string, error) {
	if m.RegistrationTokenFn != nil {
		return m.RegistrationTokenFn(ctx, scope, targetURL)
	}
	return "mock-registration-token", nil
}

// ValidateCredentials delegates to ValidateCredentialsFn if set, otherwise returns nil.
func (m *MockProvider) ValidateCredentials(ctx context.Context) error {
	if m.ValidateCredentialsFn != nil {
		return m.ValidateCredentialsFn(ctx)
	}
	return nil
}

// ScalingMode delegates to ScalingModeFn if set, otherwise returns ScalingWebhook.
func (m *MockProvider) ScalingMode() ScalingMode {
	if m.ScalingModeFn != nil {
		return m.ScalingModeFn()
	}
	return ScalingWebhook
}

// PollQueuedJobs delegates to PollQueuedJobsFn if set, otherwise returns 0.
func (m *MockProvider) PollQueuedJobs(ctx context.Context, target PollTarget) (int, error) {
	if m.PollQueuedJobsFn != nil {
		return m.PollQueuedJobsFn(ctx, target)
	}
	return 0, nil
}

// DeregisterRunner delegates to DeregisterRunnerFn if set, otherwise returns nil.
func (m *MockProvider) DeregisterRunner(ctx context.Context, scope RegistrationScope, targetURL, runnerName string) error {
	if m.DeregisterRunnerFn != nil {
		return m.DeregisterRunnerFn(ctx, scope, targetURL, runnerName)
	}
	return nil
}

// GetRenovateToken delegates to GetRenovateTokenFn if set, otherwise returns a mock token.
func (m *MockProvider) GetRenovateToken(ctx context.Context, targetURL string) (string, error) {
	if m.GetRenovateTokenFn != nil {
		return m.GetRenovateTokenFn(ctx, targetURL)
	}
	return "mock-renovate-token", nil
}

var _ GitProvider = (*MockProvider)(nil)
var _ RunnerDeregistrar = (*MockProvider)(nil)
var _ RenovateTokenProvider = (*MockProvider)(nil)
var _ AppMetadataProvider = (*MockProvider)(nil)

// MockGitProvider is a testify/mock implementation of GitProvider, RunnerDeregistrar, RenovateTokenProvider, and AppMetadataProvider.
type MockGitProvider struct {
	mock.Mock
}

// NewMockGitProvider returns a new MockGitProvider.
func NewMockGitProvider() *MockGitProvider {
	return &MockGitProvider{}
}

func (m *MockGitProvider) GetRegistrationToken(ctx context.Context, scope RegistrationScope, targetURL string) (string, error) {
	args := m.Called(ctx, scope, targetURL)
	return args.String(0), args.Error(1)
}

func (m *MockGitProvider) ValidateCredentials(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockGitProvider) ScalingMode() ScalingMode {
	args := m.Called()
	return args.Get(0).(ScalingMode)
}

func (m *MockGitProvider) PollQueuedJobs(ctx context.Context, target PollTarget) (int, error) {
	args := m.Called(ctx, target)
	return args.Int(0), args.Error(1)
}

func (m *MockGitProvider) DeregisterRunner(ctx context.Context, scope RegistrationScope, targetURL, runnerName string) error {
	args := m.Called(ctx, scope, targetURL, runnerName)
	return args.Error(0)
}

func (m *MockGitProvider) GetRenovateToken(ctx context.Context, targetURL string) (string, error) {
	args := m.Called(ctx, targetURL)
	return args.String(0), args.Error(1)
}

func (m *MockGitProvider) DiscoverOrganizations(ctx context.Context) ([]DiscoveredTarget, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]DiscoveredTarget), args.Error(1)
}

func (m *MockGitProvider) DiscoverRepositories(ctx context.Context) ([]DiscoveredTarget, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]DiscoveredTarget), args.Error(1)
}

func (m *MockGitProvider) GetAppMetadata(ctx context.Context) (string, []AppInstallation, error) {
	args := m.Called(ctx)
	installURL := args.String(0)
	if args.Get(1) == nil {
		return installURL, nil, args.Error(2)
	}
	return installURL, args.Get(1).([]AppInstallation), args.Error(2)
}

var _ GitProvider = (*MockGitProvider)(nil)
var _ RunnerDeregistrar = (*MockGitProvider)(nil)
var _ RenovateTokenProvider = (*MockGitProvider)(nil)
var _ AppMetadataProvider = (*MockGitProvider)(nil)
