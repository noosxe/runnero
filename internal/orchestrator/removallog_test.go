package orchestrator_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noosxe/runnero/internal/orchestrator"
)

func readRemovalRecords(t *testing.T, dataDir string) []orchestrator.RemovalRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, "logs", "removals.jsonl"))
	if err != nil {
		t.Fatalf("reading removals.jsonl: %v", err)
	}
	var records []orchestrator.RemovalRecord
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		var rec orchestrator.RemovalRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("removals.jsonl line is not valid JSON: %v (%s)", err, line)
		}
		records = append(records, rec)
	}
	return records
}

func TestRemovalLogger_AppendsSingleLineRecords(t *testing.T) {
	dataDir := t.TempDir()
	rl := orchestrator.NewRemovalLogger(dataDir, "deadbee1")
	if !rl.Enabled() {
		t.Fatalf("logger should be enabled with a data dir")
	}

	exit := 137
	rl.Record(orchestrator.RemovalRecord{
		RunnerID:   "abc-1",
		RunnerName: "runnero-pool-a-1234",
		PoolID:     7,
		PoolName:   "pool-a",
		Container:  "abc-1",
		Reason:     orchestrator.RemovalReasonReap,
		ExitCode:   &exit,
		Capture: orchestrator.RemovalCapture{
			OK:    true,
			Bytes: 512,
			File:  filepath.Join(dataDir, "logs", "abc-1.log.jsonl.gz"),
		},
	})
	rl.Record(orchestrator.RemovalRecord{
		RunnerID:     "abc-2",
		Container:    "abc-2",
		Reason:       orchestrator.RemovalReasonPoolDrain,
		ProviderBusy: true,
	})

	records := readRemovalRecords(t, dataDir)
	if len(records) != 2 {
		t.Fatalf("want 2 records, got %d", len(records))
	}
	first := records[0]
	if first.TS.IsZero() || first.BootID != "deadbee1" {
		t.Fatalf("timestamp/boot id not filled in: %+v", first)
	}
	if first.ExitCode == nil || *first.ExitCode != 137 {
		t.Fatalf("exit code lost: %+v", first)
	}
	if !first.Capture.OK || first.Capture.Bytes != 512 {
		t.Fatalf("capture outcome lost: %+v", first.Capture)
	}
	if records[1].ProviderBusy != true || records[1].BootID != "deadbee1" {
		t.Fatalf("second record fields wrong: %+v", records[1])
	}

	// Empty data dir disables cleanly.
	if rl := orchestrator.NewRemovalLogger("", "boot"); rl != nil {
		t.Fatalf("empty data dir must disable the logger")
	}
	// Nil logger is a no-op.
	var nilLogger *orchestrator.RemovalLogger
	nilLogger.Record(orchestrator.RemovalRecord{RunnerID: "x", Reason: orchestrator.RemovalReasonReap})
}
