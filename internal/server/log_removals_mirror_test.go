package server

import (
	"testing"
	"time"
)

// TestRemovalRecordSummaryProjection checks the wire projection rules:
// RFC3339Nano timestamps, -1 exit-code sentinel, capture passthrough.
// (Lockstep with the writer's schema is asserted end-to-end in
// log_removals_test.go / TestListRemovalRecords_WriterSchemaRoundTrip —
// this file cannot import internal/orchestrator without an import cycle.)
func TestRemovalRecordSummaryProjection(t *testing.T) {
	exitCode := 1
	ts := time.Date(2026, 9, 15, 8, 0, 0, 123000000, time.FixedZone("CET", 3600))
	rec := removalRecord{
		TS:           ts,
		BootID:       "deadbeef",
		RunnerID:     "r-1",
		RunnerName:   "runner r-1",
		PoolID:       3,
		PoolName:     "builders",
		Reason:       "idle-drain",
		ProviderBusy: true,
		DeregError:   "boom",
		ExitCode:     &exitCode,
		Capture:      removalCapture{OK: true, Bytes: 99},
	}

	sum := removalRecordSummary(&rec)
	if sum.Ts != "2026-09-15T07:00:00.123Z" {
		t.Errorf("ts not normalized to UTC RFC3339Nano: %q", sum.Ts)
	}
	if sum.ExitCode != 1 {
		t.Errorf("exit_code: got %d, want 1", sum.ExitCode)
	}
	if sum.BootId != "deadbeef" || sum.RunnerId != "r-1" || sum.PoolId != 3 ||
		sum.PoolName != "builders" || sum.Reason != "idle-drain" ||
		!sum.ProviderBusy || sum.DeregError != "boom" || !sum.CaptureOk || sum.CaptureBytes != 99 {
		t.Errorf("unexpected summary: %+v", sum)
	}

	rec.ExitCode = nil
	if got := removalRecordSummary(&rec).ExitCode; got != -1 {
		t.Errorf("nil exit_code should project to -1, got %d", got)
	}
}
