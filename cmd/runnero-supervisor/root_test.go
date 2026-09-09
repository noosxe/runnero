package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/testsupport/fakedocker"
)

// validKeyEnv returns t.Setenv for a strong placeholder encryption key so
// the loader's required-key validation passes and tests can focus on the
// behavior under test.
func validKeyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SUPERVISOR_DB_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
}

// TestMain installs a package-wide tripwire (RUN-143): the production
// daemon must never boot against the default local Docker socket from a
// test, because RebuildState adopts real runner containers and graceful
// shutdown terminates them. Every daemon-booting test obtains a hermetic
// endpoint via setFakeDocker; a test that forgets falls through to this
// dead endpoint, fails its health assertions loudly, and cannot harm a
// live stack.
func TestMain(m *testing.M) {
	if os.Getenv("SUPERVISOR_DOCKER_HOST") == "" {
		_ = os.Setenv("SUPERVISOR_DOCKER_HOST", "tcp://127.0.0.1:1")
	}
	os.Exit(m.Run())
}

// setFakeDocker points the daemon at a per-test fake Docker Engine
// (internal/testsupport/fakedocker) and returns it. This is the only
// sanctioned way for a test to boot runDaemonContext: the fake answers the
// boot-time Engine surface (ping, container list, events) so health
// assertions see a genuinely healthy engine — without any real Docker.
func setFakeDocker(t *testing.T) *fakedocker.Fake {
	t.Helper()
	fake := fakedocker.New()
	t.Cleanup(fake.Close)
	t.Setenv("SUPERVISOR_DOCKER_HOST", fake.Host())
	if err := fake.Ping(); err != nil {
		t.Fatalf("fake Docker endpoint not answering: %v", err)
	}
	return fake
}

// TestUsageListsAllSubcommands mirrors the acceptance criterion:
// `supervisor --help` lists every planned subcommand.
func TestUsageListsAllSubcommands(t *testing.T) {
	usage := NewRootCommand().UsageString()
	for _, name := range []string{"daemon", "import", "reset-password", "backup"} {
		if !strings.Contains(usage, name) {
			t.Errorf("usage output does not mention subcommand %q:\n%s", name, usage)
		}
	}
}

// TestBareInvocationRunsDaemon verifies that a bare `supervisor` delegates
// to the daemon handler. The daemon now blocks serving HTTP (RUN-10), so
// delegation is asserted structurally; the serving loop itself is
// exercised with a cancellable context in TestDaemonServesHealthEndpoints.
func TestBareInvocationRunsDaemon(t *testing.T) {
	root := NewRootCommand()
	if root.RunE == nil || reflect.ValueOf(root.RunE).Pointer() != reflect.ValueOf(runDaemon).Pointer() {
		t.Fatal("a bare invocation must delegate to the daemon handler")
	}
}

// TestDaemonRefusesToStartWithoutEncryptionKey mirrors the RUN-7
// acceptance criterion: the supervisor refuses to boot with an
// actionable error when the encryption key is not provided.
func TestDaemonRefusesToStartWithoutEncryptionKey(t *testing.T) {
	// Explicitly empty beats any ambient value; an empty key is "missing".
	t.Setenv("SUPERVISOR_DB_ENCRYPTION_KEY", "")
	root := NewRootCommand()
	root.SetArgs([]string{"daemon"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "SUPERVISOR_DB_ENCRYPTION_KEY") {
		t.Fatalf("daemon without an encryption key should refuse to start, got %v", err)
	}
}

// TestImportRequiresConfigFlag verifies that import refuses to run without
// an explicit --config path.
func TestImportRequiresConfigFlag(t *testing.T) {
	validKeyEnv(t)
	root := NewRootCommand()
	root.SetArgs([]string{"import"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--config") {
		t.Fatalf("import without --config should fail, got %v", err)
	}
}

// TestInvalidLogLevelRejected verifies that bindFlagsToConfig rejects a
// bogus --log-level before any command handler runs.
func TestInvalidLogLevelRejected(t *testing.T) {
	root := NewRootCommand()
	root.SetArgs([]string{"--log-level", "bogus", "backup"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "log-level") {
		t.Fatalf("invalid log level should be rejected, got %v", err)
	}
}

// freePort asks the kernel for an unused TCP port by binding :0, reading
// the port back, and closing. The daemon rebinds it moments later; the
// tiny window is the standard trade-off of this pattern.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// waitFor polls url until an HTTP response arrives or the deadline passes.
func waitFor(t *testing.T, url string) *http.Response {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			return resp
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s never answered", url)
	return nil
}

// healthBody is the wire shape of /healthz and /readyz (OQ #19).
type healthBody struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// getHealth fetches and decodes url.
func getHealth(t *testing.T, url string) healthBody {
	t.Helper()
	resp := waitFor(t, url)
	defer func() { _ = resp.Body.Close() }()
	var body healthBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding %s body: %v", url, err)
	}
	return body
}

// TestDaemonServesHealthEndpoints is the RUN-10 acceptance test: the
// daemon boots the Echo server on SUPERVISOR_PORT (proving environment
// configuration reaches the daemon), answers curl-able health endpoints
// with the stub probe set, and shuts down cleanly on context cancellation.
func TestDaemonServesHealthEndpoints(t *testing.T) {
	validKeyEnv(t)
	setFakeDocker(t)
	port := freePort(t)
	dataDir := t.TempDir()
	t.Setenv("SUPERVISOR_DATA_DIR", dataDir)
	t.Setenv("SUPERVISOR_PORT", strconv.Itoa(port))

	root := NewRootCommand()
	if err := bindFlagsToConfig(root, nil); err != nil {
		t.Fatalf("loading configuration: %v", err)
	}
	if cfg.DataDir != dataDir || cfg.Port != port {
		t.Fatalf("environment did not reach the daemon configuration: %+v", cfg)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runDaemonContext(ctx) }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	if body := getHealth(t, base+"/healthz"); body.Status != "healthy" || body.Checks["db"] != "ok" {
		t.Errorf("GET /healthz = %+v, want status healthy with db ok", body)
	}
	want := map[string]string{"db": "ok", "docker": "ok", "auditor": "ok"}
	if body := getHealth(t, base+"/readyz"); body.Status != "ready" || fmt.Sprint(body.Checks) != fmt.Sprint(want) {
		t.Errorf("GET /readyz = %+v, want status ready with checks %v", body, want)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon returned %v after shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not shut down after context cancellation")
	}
}

// TestDaemonRefusesToStartOnCorruptedDatabase verifies the RUN-12 and OQ #21
// acceptance criterion: when the database file is corrupted, the daemon
// refuses to start with an actionable error directing the admin to backups.
func TestDaemonRefusesToStartOnCorruptedDatabase(t *testing.T) {
	validKeyEnv(t)
	port := freePort(t)
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "corrupted.db")
	if err := os.WriteFile(dbPath, []byte("NOT_A_VALID_SQLITE_DATABASE_GARBAGE"), 0644); err != nil {
		t.Fatalf("writing corrupted file: %v", err)
	}

	t.Setenv("SUPERVISOR_DATA_DIR", dataDir)
	t.Setenv("SUPERVISOR_DB_PATH", dbPath)
	t.Setenv("SUPERVISOR_PORT", strconv.Itoa(port))

	root := NewRootCommand()
	if err := bindFlagsToConfig(root, nil); err != nil {
		t.Fatalf("loading configuration: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := runDaemonContext(ctx)
	if err == nil {
		t.Fatal("expected daemon to fail on corrupted database, got nil")
	}
	if !strings.Contains(err.Error(), "corrupted") || !strings.Contains(err.Error(), "backup") {
		t.Fatalf("expected error mentioning corrupted and backup, got %v", err)
	}
}

// TestImportAndExportCommands tests the CLI commands `supervisor import` and `supervisor export`.
func TestImportAndExportCommands(t *testing.T) {
	validKeyEnv(t)
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "supervisor.db")
	t.Setenv("SUPERVISOR_DATA_DIR", dataDir)
	t.Setenv("SUPERVISOR_DB_PATH", dbPath)
	t.Setenv("TEST_CLI_TOKEN", "secret_cli_token_123")

	yamlContent := `version: "1.0"
auth_profiles:
  cli_pat:
    auth_method: pat
    token_env_var: "TEST_CLI_TOKEN"
pools:
  - name: "cli-pool"
    provider: github
    repository_url: "https://github.com/org/cli-repo"
    auth_profile: "cli_pat"
    labels: ["self-hosted"]
    runner_image: "ghcr.io/noosxe/runnero:latest"
`
	importFile := filepath.Join(dataDir, "import.yml")
	if err := os.WriteFile(importFile, []byte(yamlContent), 0o600); err != nil {
		t.Fatalf("writing import.yml: %v", err)
	}

	// 1. Run import command
	root := NewRootCommand()
	root.SetArgs([]string{"import", "--config", importFile, "--yes"})
	if err := root.Execute(); err != nil {
		t.Fatalf("supervisor import failed: %v", err)
	}

	// Verify database was populated
	database, err := db.Open(db.Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	pools, err := database.ListRunnerPools(context.Background())
	if err != nil || len(pools) != 1 || pools[0].Name != "cli-pool" {
		_ = database.Close()
		t.Fatalf("expected 1 pool named cli-pool, got: %+v (err: %v)", pools, err)
	}
	auditLogs, err := database.ListAuditLogs(context.Background(), db.ListAuditLogsParams{Limit: 10, Offset: 0})
	_ = database.Close()
	if err != nil || len(auditLogs) == 0 || auditLogs[0].Action != "config.import" {
		t.Fatalf("expected config.import audit log, got: %+v (err: %v)", auditLogs, err)
	}

	// 2. Run export command
	exportFile := filepath.Join(dataDir, "export.yml")
	rootExport := NewRootCommand()
	rootExport.SetArgs([]string{"export", "--output", exportFile})
	if err := rootExport.Execute(); err != nil {
		t.Fatalf("supervisor export failed: %v", err)
	}

	exportedBytes, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("reading exported file: %v", err)
	}
	exportedStr := string(exportedBytes)
	if strings.Contains(exportedStr, "secret_cli_token_123") {
		t.Fatal("SECURITY VIOLATION: Exported YAML contains plaintext token!")
	}
	if !strings.Contains(exportedStr, "cli-pool") {
		t.Fatal("Exported YAML missing cli-pool")
	}
}

// TestDaemonFirstBootSeedImport verifies that the daemon imports config.yml on first boot
// and ignores YAML on subsequent boots (docs/02 §4, OQ #2).
func TestDaemonFirstBootSeedImport(t *testing.T) {
	validKeyEnv(t)
	setFakeDocker(t)
	port := freePort(t)
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "firstboot.db")
	t.Setenv("SUPERVISOR_DATA_DIR", dataDir)
	t.Setenv("SUPERVISOR_DB_PATH", dbPath)
	t.Setenv("SUPERVISOR_PORT", strconv.Itoa(port))

	configFile := filepath.Join(dataDir, "config.yml")
	yamlContent := `version: "1.0"
auth_profiles:
  fb_prof:
    auth_method: pat
    token: "token123"
pools:
  - name: "first-boot-pool"
    provider: github
    repository_url: "https://github.com/org/firstboot"
    auth_profile: "fb_prof"
    labels: ["linux"]
    runner_image: "ghcr.io/noosxe/runnero:latest"
`
	if err := os.WriteFile(configFile, []byte(yamlContent), 0o600); err != nil {
		t.Fatalf("writing config.yml: %v", err)
	}

	root := NewRootCommand()
	if err := bindFlagsToConfig(root, nil); err != nil {
		t.Fatalf("bindFlagsToConfig: %v", err)
	}

	// First boot
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan error, 1)
	go func() { done1 <- runDaemonContext(ctx1) }()

	// First boot: wait for daemon to become ready
	deadline1 := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline1) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel1()
	<-done1

	// Verify that DB has first-boot-pool
	database, err := db.Open(db.Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	pools, err := database.ListRunnerPools(context.Background())
	_ = database.Close()
	if err != nil || len(pools) != 1 || pools[0].Name != "first-boot-pool" {
		t.Fatalf("expected first-boot-pool in DB, got: %+v (err: %v)", pools, err)
	}

	// Change config.yml to declare a DIFFERENT pool
	yamlContent2 := `version: "1.0"
auth_profiles:
  fb_prof2:
    auth_method: pat
    token: "token456"
pools:
  - name: "second-boot-pool-should-be-ignored"
    provider: github
    repository_url: "https://github.com/org/ignored"
    auth_profile: "fb_prof2"
    labels: ["linux"]
    runner_image: "img"
`
	if err := os.WriteFile(configFile, []byte(yamlContent2), 0o600); err != nil {
		t.Fatalf("writing updated config.yml: %v", err)
	}

	// Second boot with fresh port
	port2 := freePort(t)
	t.Setenv("SUPERVISOR_PORT", strconv.Itoa(port2))
	root2 := NewRootCommand()
	if err := bindFlagsToConfig(root2, nil); err != nil {
		t.Fatalf("bindFlagsToConfig 2: %v", err)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- runDaemonContext(ctx2) }()

	deadline2 := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline2) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port2))
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel2()
	<-done2

	// Verify database was NOT changed (second-boot YAML ignored)
	database2, err := db.Open(db.Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	pools2, err := database2.ListRunnerPools(context.Background())
	_ = database2.Close()
	if err != nil || len(pools2) != 1 || pools2[0].Name != "first-boot-pool" {
		t.Fatalf("second boot modified the database! Expected first-boot-pool, got: %+v", pools2)
	}
}

// TestBackupCommand verifies that the `supervisor backup` CLI command
// creates a snapshot in DATA_DIR/backups.
func TestBackupCommand(t *testing.T) {
	validKeyEnv(t)
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "supervisor.db")
	t.Setenv("SUPERVISOR_DATA_DIR", dataDir)
	t.Setenv("SUPERVISOR_DB_PATH", dbPath)

	// Create DB with initial schema
	database, err := db.Open(db.Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = database.Close()

	root := NewRootCommand()
	root.SetArgs([]string{"backup"})
	if err := root.Execute(); err != nil {
		t.Fatalf("supervisor backup failed: %v", err)
	}

	backups, err := filepath.Glob(filepath.Join(dataDir, "backups", "supervisor-*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected 1 backup file, got %d (err: %v)", len(backups), err)
	}

	backupDB, err := db.Open(db.Options{Path: backups[0]})
	if err != nil {
		t.Fatalf("opening backup snapshot failed: %v", err)
	}
	_ = backupDB.Close()
}
