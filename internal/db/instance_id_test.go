package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

// TestEnsureInstanceID verifies the RUN-240 / docs/33 §3.4 instance
// identity: generated on first boot, valid UUID v4, stable across calls
// and across a full close/reopen (restart adoption must keep recognizing
// this instance's own runners).
func TestEnsureInstanceID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.db")

	database, err := Open(Options{Path: path})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	ctx := context.Background()

	first, err := database.EnsureInstanceID(ctx)
	if err != nil {
		t.Fatalf("EnsureInstanceID failed: %v", err)
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("instance id %q is not a valid UUID: %v", first, err)
	}

	// Same boot: stable.
	second, err := database.EnsureInstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("instance id changed within one boot: %q != %q", first, second)
	}
	_ = database.Close()

	// Reopen: persistent - this is the restart-adoption requirement.
	reopened, err := Open(Options{Path: path})
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	third, err := reopened.EnsureInstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if third != first {
		t.Fatalf("instance id must survive restart: %q != %q", first, third)
	}

	// It lives in app_settings where operators can inspect it.
	var stored string
	if err := reopened.sqlDB.QueryRow(
		`SELECT value FROM app_settings WHERE key = 'instance_id'`,
	).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != first {
		t.Fatalf("app_settings instance_id = %q, want %q", stored, first)
	}
}
