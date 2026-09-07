package db

import (
	"context"
	"path/filepath"
	"testing"
)

// TestInitialSchemaTablesAndSeeds verifies RUN-13 acceptance:
// - 001_initial_schema.sql applies cleanly on boot
// - All 8 tables match docs/07 field-for-field
// - Default app_settings are seeded per docs/07 and OQ #10
// - CHECK constraints are enforced
func TestInitialSchemaTablesAndSeeds(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "schema.db")

	database, err := Open(Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()

	ver, err := database.Version(ctx, nil)
	if err != nil {
		t.Fatalf("Version failed: %v", err)
	}
	if ver != 5 {
		t.Fatalf("database version = %d, want 5", ver)
	}

	// Verify all 10 tables and their columns field-for-field per docs/07.
	expectedTables := map[string][]string{
		"admin_users": {
			"id", "username", "password_hash", "created_at", "updated_at",
		},
		"sessions": {
			"id", "user_id", "token_hash", "expires_at", "created_at",
		},
		"auth_profiles": {
			"id", "name", "auth_method", "app_id", "private_key_encrypted", "token_encrypted", "created_at", "updated_at",
		},
		"runner_pools": {
			"id", "name", "provider", "repository_url", "scope", "auth_profile_id",
			"min_idle_runners", "max_concurrency", "labels", "runner_image",
			"allow_docker", "max_runner_lifetime_seconds", "cpu_limit", "memory_limit",
			"created_at", "updated_at",
		},
		"pool_targets": {
			"id", "pool_id", "target_url", "created_at",
		},
		"renovate_configs": {
			"id", "pool_id", "enabled", "cron_schedule", "image", "created_at", "updated_at",
		},
		"renovate_runs": {
			"id", "pool_id", "status", "started_at", "completed_at", "summary", "container_id", "created_at",
		},
		"job_history": {
			"id", "pool_id", "runner_name", "status", "queued_at", "started_at", "completed_at", "log_retention_path", "created_at",
		},
		"audit_logs": {
			"id", "user_id", "action", "resource_type", "resource_id", "details", "created_at",
		},
		"app_settings": {
			"key", "value", "updated_at",
		},
	}

	for table, cols := range expectedTables {
		actualCols := getTableColumns(t, database, table)
		if len(actualCols) == 0 {
			t.Errorf("table %q does not exist", table)
			continue
		}
		for _, col := range cols {
			if !actualCols[col] {
				t.Errorf("table %q missing column %q", table, col)
			}
		}
	}

	// Verify seeded app_settings
	expectedSettings := map[string]string{
		"total_allowed_runners":    "20",
		"total_idle_warm_pool":     "5",
		"shutdown_timeout_seconds": "300",
		"job_retention_days":       "30",
	}

	for key, wantVal := range expectedSettings {
		var val string
		err := database.SQL().QueryRow("SELECT value FROM app_settings WHERE key = ?;", key).Scan(&val)
		if err != nil {
			t.Errorf("query setting %q: %v", key, err)
			continue
		}
		if val != wantVal {
			t.Errorf("app_settings[%q] = %q, want %q", key, val, wantVal)
		}
	}

	// Verify CHECK constraints
	// 1. auth_profiles.auth_method
	_, err = database.SQL().Exec("INSERT INTO auth_profiles (name, auth_method) VALUES ('test', 'invalid_auth');")
	if err == nil {
		t.Error("expected CHECK constraint failure for invalid auth_method, got nil")
	}

	// 2. runner_pools.provider
	_, err = database.SQL().Exec(`
		INSERT INTO runner_pools (name, provider, repository_url, auth_profile_id, labels, runner_image)
		VALUES ('p1', 'invalid_provider', 'https://github.com/o/r', 1, '[]', 'img');
	`)
	if err == nil {
		t.Error("expected CHECK constraint failure for invalid provider, got nil")
	}

	// 3. runner_pools.scope
	_, err = database.SQL().Exec(`
		INSERT INTO runner_pools (name, provider, repository_url, scope, auth_profile_id, labels, runner_image)
		VALUES ('p2', 'github', 'https://github.com/o/r', 'invalid_scope', 1, '[]', 'img');
	`)
	if err == nil {
		t.Error("expected CHECK constraint failure for invalid scope, got nil")
	}

	// 4. job_history.status
	_, err = database.SQL().Exec(`
		INSERT INTO job_history (pool_id, runner_name, status)
		VALUES (1, 'runner-1', 'invalid_status');
	`)
	if err == nil {
		t.Error("expected CHECK constraint failure for invalid status, got nil")
	}
}

// TestInitialSchemaUpDownIdempotent verifies that migrating up, rolling down,
// and migrating back up is completely idempotent and drops/recreates all tables cleanly.
func TestInitialSchemaUpDownIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "up_down.db")

	database, err := Open(Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()

	// Initial state: version 5
	ver, err := database.Version(ctx, nil)
	if err != nil || ver != 5 {
		t.Fatalf("Version after boot = %d (err: %v), want 5", ver, err)
	}

	// Rollback all migrations down to version 0
	for ver > 0 {
		if err := database.Rollback(ctx, nil); err != nil {
			t.Fatalf("Rollback failed: %v", err)
		}
		ver, err = database.Version(ctx, nil)
		if err != nil {
			t.Fatalf("Version check failed: %v", err)
		}
	}

	if ver != 0 {
		t.Fatalf("Version after Rollback = %d, want 0", ver)
	}

	// Verify tables are dropped
	for _, table := range []string{
		"admin_users", "sessions", "auth_profiles", "runner_pools", "pool_targets",
		"renovate_configs", "renovate_runs", "job_history", "audit_logs", "app_settings",
	} {
		cols := getTableColumns(t, database, table)
		if len(cols) > 0 {
			t.Errorf("table %q should have been dropped by Down migration", table)
		}
	}

	// Migrate up again: should succeed and restore version 5
	if err := database.Migrate(ctx, nil); err != nil {
		t.Fatalf("Migrate up after rollback failed: %v", err)
	}

	verUp, err := database.Version(ctx, nil)
	if err != nil || verUp != 5 {
		t.Fatalf("Version after Migrate up = %d (err: %v), want 5", verUp, err)
	}

	// Verify app_settings seeded again
	var settingVal string
	err = database.SQL().QueryRow("SELECT value FROM app_settings WHERE key = 'total_allowed_runners';").Scan(&settingVal)
	if err != nil || settingVal != "20" {
		t.Errorf("total_allowed_runners = %q (err: %v), want '20'", settingVal, err)
	}

	// Additional Migrate call should be idempotent no-op
	if err := database.Migrate(ctx, nil); err != nil {
		t.Fatalf("Subsequent Migrate call failed: %v", err)
	}
}

func getTableColumns(t *testing.T, db *DB, tableName string) map[string]bool {
	t.Helper()
	rows, err := db.SQL().Query("PRAGMA table_info(" + tableName + ");")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", tableName, err)
	}
	defer func() { _ = rows.Close() }()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue *string
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scanning column info: %v", err)
		}
		cols[name] = true
	}
	return cols
}
