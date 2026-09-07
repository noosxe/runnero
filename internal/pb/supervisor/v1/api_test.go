package supervisorv1_test

import (
	"testing"

	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"google.golang.org/protobuf/proto"
)

func TestProtoSchemasAndStubs(t *testing.T) {
	// Verify message instantiations and proto marshaling
	pool := &supervisorv1.Pool{
		Id:                       1,
		Name:                     "test-pool",
		Provider:                 "github",
		RepositoryUrl:            "https://github.com/owner/repo",
		MinIdleRunners:           2,
		MaxConcurrency:           10,
		Labels:                   []string{"self-hosted", "arm64"},
		RunnerImage:              "ghcr.io/noosxe/runnero:latest",
		AllowDocker:              true,
		AuthProfileId:            42,
		Scope:                    "repo",
		CpuLimit:                 "2.0",
		MemoryLimit:              "4Gi",
		MaxRunnerLifetimeSeconds: 3600,
		Renovate: &supervisorv1.RenovateConfig{
			Enabled:      true,
			CronSchedule: "0 0 * * *",
			Image:        "renovate/renovate:latest",
		},
	}

	data, err := proto.Marshal(pool)
	if err != nil {
		t.Fatalf("failed to marshal Pool proto: %v", err)
	}

	var decoded supervisorv1.Pool
	if err := proto.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal Pool proto: %v", err)
	}

	if decoded.Name != pool.Name || decoded.MaxRunnerLifetimeSeconds != 3600 {
		t.Errorf("decoded proto field mismatch: got %+v", &decoded)
	}

	// Verify LogService streaming and history messages
	chunk := &supervisorv1.LogChunk{
		Timestamp: "2026-09-03T12:00:00Z",
		Stream:    "stdout",
		Content:   "Runner registered successfully\n",
	}
	if chunk.Stream != "stdout" || chunk.Content == "" {
		t.Errorf("unexpected log chunk: %+v", chunk)
	}

	// Verify Connect service constants exist and are correctly named
	if supervisorv1connect.AuthServiceName != "supervisor.v1.AuthService" {
		t.Errorf("unexpected AuthService name: %s", supervisorv1connect.AuthServiceName)
	}
	if supervisorv1connect.PoolServiceName != "supervisor.v1.PoolService" {
		t.Errorf("unexpected PoolService name: %s", supervisorv1connect.PoolServiceName)
	}
	if supervisorv1connect.AuthProfileServiceName != "supervisor.v1.AuthProfileService" {
		t.Errorf("unexpected AuthProfileService name: %s", supervisorv1connect.AuthProfileServiceName)
	}
	if supervisorv1connect.OnboardingServiceName != "supervisor.v1.OnboardingService" {
		t.Errorf("unexpected OnboardingService name: %s", supervisorv1connect.OnboardingServiceName)
	}
	if supervisorv1connect.AnalyticsServiceName != "supervisor.v1.AnalyticsService" {
		t.Errorf("unexpected AnalyticsService name: %s", supervisorv1connect.AnalyticsServiceName)
	}
	if supervisorv1connect.LogServiceName != "supervisor.v1.LogService" {
		t.Errorf("unexpected LogService name: %s", supervisorv1connect.LogServiceName)
	}
	if supervisorv1connect.RenovateServiceName != "supervisor.v1.RenovateService" {
		t.Errorf("unexpected RenovateService name: %s", supervisorv1connect.RenovateServiceName)
	}
	if supervisorv1connect.ImageUpdateServiceName != "supervisor.v1.ImageUpdateService" {
		t.Errorf("unexpected ImageUpdateService name: %s", supervisorv1connect.ImageUpdateServiceName)
	}
}
