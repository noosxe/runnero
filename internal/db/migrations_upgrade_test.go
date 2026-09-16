package db

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

// TestUpgradeFromV8PopulatedSessions reproduces the docs/32 phase-1 upgrade
// failure: migration 009 originally added session clock columns with
// DEFAULT CURRENT_TIMESTAMP, which SQLite rejects on any table that already
// holds rows ("Cannot add a column with non-constant default") - fresh
// databases sailed through tests while populated production databases failed
// to boot. The fix uses constant sentinel defaults plus backfills.
//
// This test builds a version-8 database with real rows, then migrates to
// head, asserting the upgrade path stays open.
func TestUpgradeFromV8PopulatedSessions(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "upgrade.db")

	// Migrate a fresh database up to version 8 only, using a filtered
	// filesystem over the embedded migrations.
	pre9, err := migrationsUpTo(8)
	if err != nil {
		t.Fatalf("build pre-9 filesystem: %v", err)
	}
	database, err := Open(Options{Path: dbPath, SkipMigrations: true})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	ctx := context.Background()
	if err := database.Migrate(ctx, pre9); err != nil {
		t.Fatalf("migrate to v8 failed: %v", err)
	}
	ver, err := database.Version(ctx, pre9)
	if err != nil {
		t.Fatalf("Version failed: %v", err)
	}
	if ver != 8 {
		t.Fatalf("database version = %d, want 8", ver)
	}

	// Seed data the way a pre-docs/32 deployment would have it: an admin
	// user and JWT-era session rows carrying only the v8 columns.
	seed := []string{
		`INSERT INTO admin_users (username, password_hash) VALUES ('admin', 'x')`,
		`INSERT INTO sessions (user_id, token_hash, expires_at, created_at)
		 VALUES (1, 'hash-a', '2026-01-01 12:00:00', '2026-01-01 06:00:00')`,
		`INSERT INTO sessions (user_id, token_hash, expires_at, created_at)
		 VALUES (1, 'hash-b', '2026-01-02 12:00:00', '2026-01-01 18:00:00')`,
		`INSERT INTO audit_logs (action) VALUES ('test.action')`,
	}
	for _, stmt := range seed {
		if _, err := database.sqlDB.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	// Upgrade to head (010) against the populated tables - the case that
	// used to fail.
	if err := database.Migrate(ctx, nil); err != nil {
		t.Fatalf("migrate to head failed: %v", err)
	}
	ver, err = database.Version(ctx, nil)
	if err != nil {
		t.Fatalf("Version after upgrade: %v", err)
	}
	if ver != 10 {
		t.Fatalf("database version = %d, want 10", ver)
	}
	// Backfills: existing rows keep their semantics under the new clocks.
	rows := []struct {
		tokenHash       string
		lastSeen        time.Time
		absoluteExpires time.Time
		expiresAt       time.Time
		userAgent       string
	}{
		{"hash-a", mustTime(t, "2026-01-01 06:00:00"), mustTime(t, "2026-01-01 12:00:00"), mustTime(t, "2026-01-01 12:00:00"), ""},
		{"hash-b", mustTime(t, "2026-01-01 18:00:00"), mustTime(t, "2026-01-02 12:00:00"), mustTime(t, "2026-01-02 12:00:00"), ""},
	}
	for _, want := range rows {
		var got struct {
			lastSeen        time.Time
			absoluteExpires time.Time
			expiresAt       time.Time
			userAgent       string
		}
		err := database.sqlDB.QueryRow(
			`SELECT last_seen_at, absolute_expires_at, expires_at, user_agent
			 FROM sessions WHERE token_hash = ?`, want.tokenHash,
		).Scan(&got.lastSeen, &got.absoluteExpires, &got.expiresAt, &got.userAgent)
		if err != nil {
			t.Fatalf("scan session %s: %v", want.tokenHash, err)
		}
		if !got.lastSeen.Equal(want.lastSeen) {
			t.Errorf("%s last_seen_at = %v, want %v (created_at backfill)", want.tokenHash, got.lastSeen, want.lastSeen)
		}
		if !got.absoluteExpires.Equal(want.absoluteExpires) {
			t.Errorf("%s absolute_expires_at = %v, want %v (expires_at backfill)", want.tokenHash, got.absoluteExpires, want.absoluteExpires)
		}
		if !got.expiresAt.Equal(want.expiresAt) {
			t.Errorf("%s expires_at = %v, want unchanged", want.tokenHash, got.expiresAt)
		}
		if got.userAgent != want.userAgent {
			t.Errorf("%s user_agent = %q, want %q", want.tokenHash, got.userAgent, want.userAgent)
		}
	}

	// Sentinel defaults must never survive: new rows set every column
	// explicitly (CreateSession).
	sentinel := mustTime(t, "1970-01-01 00:00:00")
	created, err := database.CreateSession(ctx, CreateSessionParams{
		UserID:            1,
		TokenHash:         "hash-c",
		ExpiresAt:         mustTime(t, "2026-03-01 12:00:00"),
		AbsoluteExpiresAt: mustTime(t, "2026-03-01 12:00:00"),
		UserAgent:         "Mozilla/5.0",
		LastSeenAt:        mustTime(t, "2026-02-28 12:00:00"),
	})
	if err != nil {
		t.Fatalf("CreateSession after upgrade: %v", err)
	}
	if created.LastSeenAt.Equal(sentinel) || !created.LastSeenAt.Equal(mustTime(t, "2026-02-28 12:00:00")) {
		t.Errorf("new session last_seen_at = %v, want explicit insert value", created.LastSeenAt)
	}

	_ = database.Close()
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return v
}

// migrationsUpTo returns a filesystem containing only the embedded migration
// files with version <= max, so goose stops at the requested version.
func migrationsUpTo(max int64) (fs.FS, error) {
	entries, err := fs.ReadDir(defaultMigrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	mapfs := fstest.MapFS{}
	for _, e := range entries {
		var version int64
		if _, err := fmt.Sscanf(e.Name(), "%d", &version); err != nil {
			continue
		}
		if version > max {
			continue
		}
		data, err := fs.ReadFile(defaultMigrationsFS, filepath.Join("migrations", e.Name()))
		if err != nil {
			return nil, err
		}
		mapfs[e.Name()] = &fstest.MapFile{Data: data}
	}
	return mapfs, nil
}
