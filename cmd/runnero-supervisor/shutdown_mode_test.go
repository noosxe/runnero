package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/testsupport/fakedocker"
)

const shutdownTestContainer = "c0ffee0000000000000000000000000000000000000000000000000000000001"

// runDaemonUntilReady boots the daemon against a fake engine holding one
// running managed runner container, waits for readiness, then cancels the
// signal context and waits for the daemon to return.
func runDaemonUntilReady(t *testing.T, fake *fakedocker.Fake) {
	t.Helper()
	fake.SetContainers([]fakedocker.Container{{
		Id:     shutdownTestContainer,
		Names:  []string{"/runnero-pool-a-abc123"},
		Image:  "ghcr.io/noosxe/runnero-runner:latest",
		State:  "running",
		Status: "Up 5 minutes",
		Labels: map[string]string{
			orchestrator.LabelManaged:  "true",
			orchestrator.LabelPoolName: "pool-a",
		},
		Created: time.Now().Add(-5 * time.Minute).Unix(),
	}})

	port := freePort(t)
	t.Setenv("SUPERVISOR_DATA_DIR", t.TempDir())
	t.Setenv("SUPERVISOR_PORT", strconv.Itoa(port))
	root := NewRootCommand()
	if err := bindFlagsToConfig(root, nil); err != nil {
		t.Fatalf("loading configuration: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runDaemonContext(ctx) }()

	if body := getHealth(t, fmt.Sprintf("http://127.0.0.1:%d/readyz", port)); body.Status != "ready" {
		t.Fatalf("daemon not ready: %+v", body)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon returned %v after shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not shut down after context cancellation")
	}
}

// containerMutations returns the engine requests that stop, kill, or remove
// the test container.
func containerMutations(fake *fakedocker.Fake) []string {
	var out []string
	for _, r := range fake.Requests() {
		if r.Method != http.MethodGet && strings.Contains(r.Path, shutdownTestContainer) {
			out = append(out, r.Method+" "+r.Path)
		}
	}
	return out
}

// TestDaemonDetachShutdownLeavesRunners is the docs/03 §7 default: a signal
// shutdown never stops, kills, or removes a runner container.
func TestDaemonDetachShutdownLeavesRunners(t *testing.T) {
	validKeyEnv(t)
	fake := setFakeDocker(t)

	runDaemonUntilReady(t, fake)

	if got := containerMutations(fake); len(got) != 0 {
		t.Fatalf("detach shutdown touched the runner container: %v", got)
	}
}

// TestDaemonDrainShutdownTerminatesRunners covers the opt-in drain mode:
// the idle runner is terminated on the way out.
func TestDaemonDrainShutdownTerminatesRunners(t *testing.T) {
	validKeyEnv(t)
	t.Setenv("SUPERVISOR_SHUTDOWN_MODE", "drain")
	fake := setFakeDocker(t)

	runDaemonUntilReady(t, fake)

	if got := containerMutations(fake); len(got) == 0 {
		t.Fatal("drain shutdown left the idle runner container untouched")
	}
}
