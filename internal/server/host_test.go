package server_test

import (
	"runtime"
	"testing"

	"github.com/noosxe/runnero/internal/server"
)

func TestHostArchAndOS(t *testing.T) {
	// Defaults to runtime
	if arch := server.HostArch(); arch != runtime.GOARCH {
		t.Errorf("HostArch want %s, got %s", runtime.GOARCH, arch)
	}
	if osName := server.HostOS(); osName != runtime.GOOS {
		t.Errorf("HostOS want %s, got %s", runtime.GOOS, osName)
	}

	// Environment variable overrides
	t.Setenv("SUPERVISOR_HOST_ARCH", "amd64")
	if arch := server.HostArch(); arch != "amd64" {
		t.Errorf("HostArch with env override want amd64, got %s", arch)
	}

	t.Setenv("SUPERVISOR_HOST_OS", "darwin")
	if osName := server.HostOS(); osName != "darwin" {
		t.Errorf("HostOS with env override want darwin, got %s", osName)
	}
}
