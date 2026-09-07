package gitea

import (
	"bytes"
	"strings"
	"testing"

	"github.com/noosxe/runnero/internal/logging"
)

// TestModuleLoggerTagged proves this package instantiates its module
// logger (RUN-8 acceptance): records carry module="provider.gitea", and level
// filtering applies — the debug record below stays suppressed at info.
func TestModuleLoggerTagged(t *testing.T) {
	var buf bytes.Buffer
	if err := logging.Setup(logging.Options{Level: "info", Writer: &buf}); err != nil {
		t.Fatalf("Setup(info) failed: %v", err)
	}

	logger.Info("beacon")
	logger.Debug("suppressed beacon")

	out := buf.String()
	if !strings.Contains(out, "\"module\":\"provider.gitea\"") || !strings.Contains(out, "\"msg\":\"beacon\"") {
		t.Errorf("record missing module tag or message:\n%s", out)
	}
	if strings.Contains(out, "suppressed beacon") {
		t.Error("debug record leaked through the info-level filter")
	}
}
