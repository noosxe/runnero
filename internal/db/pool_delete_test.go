package db

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// TestDeleteRunnerPoolTombstoned verifies the docs/33 §3.1 ownership
// evidence: the tombstone lands in the same transaction as the pool delete,
// the lookup reflects it, and unrelated pool ids report no tombstone.
func TestDeleteRunnerPoolTombstoned(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(Options{Path: filepath.Join(dir, "tombstone.db")})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()

	// Seed two pools directly (columns shared by every migration state).
	if _, err := database.sqlDB.Exec(
		`INSERT INTO auth_profiles (id, name, auth_method) VALUES (1, 'profile', 'pat')`,
	); err != nil {
		t.Fatalf("seed auth profile: %v", err)
	}
	for _, id := range []int64{1, 2} {
		if _, err := database.sqlDB.Exec(
			`INSERT INTO runner_pools (id, name, provider, repository_url, scope, auth_profile_id, labels, runner_image)
			 VALUES (?, ?, 'github', 'https://example.invalid/x', 'repo', 1, '[]', 'ghcr.io/noosxe/runnero:latest')`,
			id, fmt.Sprintf("pool-%d", id),
		); err != nil {
			t.Fatalf("seed pool %d: %v", id, err)
		}
	}

	if err := database.DeleteRunnerPoolTombstoned(ctx, 1, "gone-pool"); err != nil {
		t.Fatalf("DeleteRunnerPoolTombstoned: %v", err)
	}

	// Pool row gone.
	var count int
	if err := database.sqlDB.QueryRow(`SELECT COUNT(*) FROM runner_pools WHERE id = 1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("pool row must be deleted, found %d", count)
	}

	// Tombstone present for the deleted id, absent for the survivor.
	present, err := database.PoolTombstoneExists(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("deleted pool must have a tombstone")
	}
	present, err = database.PoolTombstoneExists(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("surviving pool must not have a tombstone")
	}

	// The tombstone survives a full close/reopen (it is the restart case).
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(Options{Path: filepath.Join(dir, "tombstone.db")})
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	present, err = reopened.PoolTombstoneExists(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("tombstone must survive a restart - it is the restart case")
	}
}
