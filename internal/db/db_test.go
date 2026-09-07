package db

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/noosxe/runnero/internal/logging"
)

// TestFreshDBOpenAndPragmas verifies that a fresh database file is created
// with WAL mode, foreign keys, and busy timeout pragmas applied.
func TestFreshDBOpenAndPragmas(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sub", "test.db")

	database, err := Open(Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = database.Close() }()

	if database.Path() != dbPath {
		t.Errorf("Path() = %q, want %q", database.Path(), dbPath)
	}

	ctx := context.Background()
	if err := database.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	var journalMode string
	if err := database.SQL().QueryRow("PRAGMA journal_mode;").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var foreignKeys int
	if err := database.SQL().QueryRow("PRAGMA foreign_keys;").Scan(&foreignKeys); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}

	var busyTimeout int
	if err := database.SQL().QueryRow("PRAGMA busy_timeout;").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout < 5000 {
		t.Errorf("busy_timeout = %d, want >= 5000", busyTimeout)
	}
}

// TestFreshDBMigratesOnBoot verifies acceptance criteria: a fresh DB executes
// goose migrations automatically at startup.
func TestFreshDBMigratesOnBoot(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "migrate.db")

	testMigrations := fstest.MapFS{
		"00001_initial.sql": &fstest.MapFile{
			Data: []byte(`-- +goose Up
CREATE TABLE test_nodes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL
);
INSERT INTO test_nodes (name) VALUES ('node-1');

-- +goose Down
DROP TABLE test_nodes;
`),
		},
	}

	database, err := Open(Options{
		Path:       dbPath,
		Migrations: testMigrations,
	})
	if err != nil {
		t.Fatalf("Open with migrations failed: %v", err)
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()
	ver, err := database.Version(ctx, testMigrations)
	if err != nil {
		t.Fatalf("Version failed: %v", err)
	}
	if ver != 1 {
		t.Errorf("DB version = %d, want 1", ver)
	}

	var name string
	err = database.SQL().QueryRow("SELECT name FROM test_nodes WHERE id = 1;").Scan(&name)
	if err != nil {
		t.Fatalf("query migrated table: %v", err)
	}
	if name != "node-1" {
		t.Errorf("test_nodes row = %q, want 'node-1'", name)
	}

	// Reopening existing database should not fail and preserves version.
	db2, err := Open(Options{
		Path:       dbPath,
		Migrations: testMigrations,
	})
	if err != nil {
		t.Fatalf("Reopening database failed: %v", err)
	}
	defer func() { _ = db2.Close() }()

	ver2, err := db2.Version(ctx, testMigrations)
	if err != nil {
		t.Fatalf("Version failed on reopened DB: %v", err)
	}
	if ver2 != 1 {
		t.Errorf("DB version after reopen = %d, want 1", ver2)
	}
}

// TestCorruptedDBRefusesToStart verifies acceptance criteria & OQ #21:
// when the database file is corrupted, the supervisor refuses to boot with a
// clear actionable error directing the administrator to restore from backups.
func TestCorruptedDBRefusesToStart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "corrupt.db")

	if err := os.WriteFile(dbPath, []byte("NOT_A_VALID_SQLITE_DATABASE_HEADER_GARBAGE"), 0644); err != nil {
		t.Fatalf("creating corrupt file: %v", err)
	}

	database, err := Open(Options{Path: dbPath})
	if err == nil {
		_ = database.Close()
		t.Fatal("Open succeeded on corrupted database file, expected error")
	}

	if !errors.Is(err, ErrCorrupted) {
		t.Errorf("expected error wrapping ErrCorrupted, got %v", err)
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "corrupted") {
		t.Errorf("error %q should mention 'corrupted'", errStr)
	}
	if !strings.Contains(errStr, "backup") {
		t.Errorf("error %q should point admin to restore from backup", errStr)
	}
}

// TestMigrationErrorStrictlyLogged verifies that migration syntax/execution errors
// are rejected and strictly logged per docs/06.
func TestMigrationErrorStrictlyLogged(t *testing.T) {
	var logBuf bytes.Buffer
	if err := logging.Setup(logging.Options{Level: "info", Writer: &logBuf}); err != nil {
		t.Fatalf("logging.Setup failed: %v", err)
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bad_mig.db")

	badMigrations := fstest.MapFS{
		"00001_invalid.sql": &fstest.MapFile{
			Data: []byte(`-- +goose Up
THIS IS NOT VALID SQL STATEMENT;
`),
		},
	}

	database, err := Open(Options{
		Path:       dbPath,
		Migrations: badMigrations,
	})
	if err == nil {
		_ = database.Close()
		t.Fatal("Open expected to fail on invalid migration, got nil")
	}

	if !errors.Is(err, ErrMigration) {
		t.Errorf("expected error wrapping ErrMigration, got %v", err)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "database migration failed") {
		t.Errorf("logs should contain 'database migration failed', got:\n%s", logs)
	}
	if !strings.Contains(logs, "\"module\":\"db\"") {
		t.Errorf("logs should be tagged with module=db, got:\n%s", logs)
	}
}

// TestEmptyPathRejected verifies that an empty path returns an error.
func TestEmptyPathRejected(t *testing.T) {
	_, err := Open(Options{Path: "  "})
	if err == nil {
		t.Fatal("Open with empty path should return error")
	}
}

// TestInMemoryDatabase verifies :memory: databases operate correctly.
func TestInMemoryDatabase(t *testing.T) {
	database, err := OpenPath(":memory:")
	if err != nil {
		t.Fatalf("OpenPath(:memory:) failed: %v", err)
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()
	if err := database.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
}
