package orchestrator

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/noosxe/runnero/internal/config"
	"github.com/noosxe/runnero/internal/logging"
)

// TestEngineOwnershipModeNormalization locks the RUN-241 / docs/33 §3.5
// controller-side contract: empty and unknown values resolve to strict
// (fail-safe — unknown modes must never silently weaken the gate), only
// the exact adopt-all value activates the escape hatch.
func TestEngineOwnershipModeNormalization(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", config.EngineOwnershipStrict},
		{config.EngineOwnershipStrict, config.EngineOwnershipStrict},
		{config.EngineOwnershipAdoptAll, config.EngineOwnershipAdoptAll},
		{"yolo", config.EngineOwnershipStrict},
		{"ADOPT-ALL", config.EngineOwnershipStrict}, // case variants must not arm the hatch
	}
	for _, tc := range cases {
		if got := engineOwnershipMode(tc.in); got != tc.want {
			t.Errorf("engineOwnershipMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAdoptAllBootNotice verifies the prominent boot warning (docs/33 §3.5:
// "its use is logged at boot"): the adopt-all controller logs a WARN naming
// the escape hatch at Boot; strict stays silent.
func TestAdoptAllBootNotice(t *testing.T) {
	var buf bytes.Buffer
	if err := logging.Setup(logging.Options{Level: "info", Writer: &buf}); err != nil {
		t.Fatalf("Setup(info) failed: %v", err)
	}

	ctrl := NewPoolController(ControllerOptions{EngineOwnership: config.EngineOwnershipAdoptAll})
	if err := ctrl.Boot(context.Background()); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "escape hatch ACTIVE") || !strings.Contains(out, "adopt-all") {
		t.Errorf("adopt-all boot must log a prominent notice, got:\n%s", out)
	}

	buf.Reset()
	strict := NewPoolController(ControllerOptions{})
	if err := strict.Boot(context.Background()); err != nil {
		t.Fatalf("strict boot failed: %v", err)
	}
	if strings.Contains(buf.String(), "escape hatch ACTIVE") {
		t.Errorf("strict boot must not log the escape-hatch notice, got:\n%s", buf.String())
	}
}
