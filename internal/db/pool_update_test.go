package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// newPoolUpdateDB seeds one auth profile, a pool with two targets, and (for
// the first pool only) a renovate config, mirroring the write legs UpdatePool
// must keep in step (RUN-129).
func newPoolUpdateDB(t *testing.T) (*DB, RunnerPool, RunnerPool, func()) {
	t.Helper()
	database, err := Open(Options{Path: filepath.Join(t.TempDir(), "pool_update_test.db")})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	cleanup := func() { _ = database.Close() }

	ctx := context.Background()
	profile, err := database.CreateAuthProfile(ctx, CreateAuthProfileParams{
		Name:                "github-pool-update",
		AuthMethod:          "github_app",
		AppID:               sql.NullInt64{Int64: 12345, Valid: true},
		PrivateKeyEncrypted: sql.NullString{String: "enc_priv_key", Valid: true},
		TokenEncrypted:      sql.NullString{String: "enc_token", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}

	pool, err := database.CreateRunnerPool(ctx, CreateRunnerPoolParams{
		Name:          "pool-update-pool",
		Provider:      "github",
		RepositoryUrl: "https://github.com/myorg/myrepo",
		Scope:         "repo",
		AuthProfileID: profile.ID,
		Labels:        `["self-hosted","linux"]`,
		RunnerImage:   "ghcr.io/noosxe/runnero:latest",
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool failed: %v", err)
	}
	for _, url := range []string{"https://github.com/myorg/myrepo", "https://github.com/myorg/other"} {
		if _, err := database.AddPoolTarget(ctx, AddPoolTargetParams{PoolID: pool.ID, TargetUrl: url}); err != nil {
			t.Fatalf("AddPoolTarget failed: %v", err)
		}
	}
	if _, err := database.CreateRenovateConfig(ctx, CreateRenovateConfigParams{
		PoolID:       pool.ID,
		Enabled:      false,
		CronSchedule: sql.NullString{String: "0 3 * * *", Valid: true},
		Image:        "renovate/renovate:1.0",
	}); err != nil {
		t.Fatalf("CreateRenovateConfig failed: %v", err)
	}

	// A second pool without a renovate config row exercises the in-transaction
	// update→create fallback.
	bare, err := database.CreateRunnerPool(ctx, CreateRunnerPoolParams{
		Name:          "pool-update-bare",
		Provider:      "github",
		RepositoryUrl: "https://github.com/myorg/bare",
		Scope:         "repo",
		AuthProfileID: profile.ID,
		Labels:        `["self-hosted"]`,
		RunnerImage:   "ghcr.io/noosxe/runnero:latest",
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool (bare) failed: %v", err)
	}

	return database, pool, bare, cleanup
}

func poolUpdateParams(pool RunnerPool, name string) UpdateRunnerPoolParams {
	return UpdateRunnerPoolParams{
		ID:                       pool.ID,
		Name:                     name,
		Provider:                 pool.Provider,
		RepositoryUrl:            pool.RepositoryUrl,
		Scope:                    pool.Scope,
		AuthProfileID:            pool.AuthProfileID,
		Labels:                   pool.Labels,
		RunnerImage:              pool.RunnerImage,
		MaxRunnerLifetimeSeconds: 3600,
	}
}

// TestUpdatePoolAtomicRollback proves the whole update sequence rolls back
// when a leg fails mid-transaction (RUN-129): a test-only trigger aborts the
// target insert, and the earlier pool-row and renovate writes must not
// survive.
func TestUpdatePoolAtomicRollback(t *testing.T) {
	database, pool, _, cleanup := newPoolUpdateDB(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := database.sqlDB.ExecContext(ctx,
		`CREATE TRIGGER pool_update_test_fail BEFORE INSERT ON pool_targets
		 WHEN NEW.target_url = 'https://github.com/myorg/boom' BEGIN SELECT RAISE(ABORT, 'boom-target'); END;`); err != nil {
		t.Fatalf("creating failure trigger failed: %v", err)
	}

	_, err := database.UpdatePool(ctx, PoolUpdate{
		Pool: poolUpdateParams(pool, "pool-update-renamed"),
		Renovate: &UpdateRenovateConfigParams{
			Enabled:      true,
			CronSchedule: sql.NullString{String: "0 5 * * *", Valid: true},
			Image:        "renovate/renovate:2.0",
		},
		Targets: []string{
			"https://github.com/myorg/myrepo",
			"https://github.com/myorg/boom",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "boom-target") {
		t.Fatalf("expected the triggered insert failure, got %v", err)
	}

	got, err := database.GetRunnerPoolById(ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetRunnerPoolById failed: %v", err)
	}
	if got.Name != "pool-update-pool" {
		t.Errorf("pool row leaked through the rollback: name = %q", got.Name)
	}

	renovate, err := database.GetRenovateConfigByPoolId(ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetRenovateConfigByPoolId failed: %v", err)
	}
	if renovate.Enabled || renovate.Image != "renovate/renovate:1.0" {
		t.Errorf("renovate config leaked through the rollback: enabled=%v image=%q", renovate.Enabled, renovate.Image)
	}

	targets, err := database.ListPoolTargetsByPoolId(ctx, pool.ID)
	if err != nil {
		t.Fatalf("ListPoolTargetsByPoolId failed: %v", err)
	}
	if len(targets) != 2 {
		t.Errorf("targets changed despite rollback: %d rows", len(targets))
	}
}

// TestUpdatePoolAtomicCommit: with no failure injected, all legs land — pool
// row, renovate config (the update→create fallback for a pool without one),
// and the full target rewrite.
func TestUpdatePoolAtomicCommit(t *testing.T) {
	database, pool, bare, cleanup := newPoolUpdateDB(t)
	defer cleanup()
	ctx := context.Background()

	updated, err := database.UpdatePool(ctx, PoolUpdate{
		Pool: poolUpdateParams(pool, "pool-update-renamed"),
		Renovate: &UpdateRenovateConfigParams{
			Enabled:      true,
			CronSchedule: sql.NullString{String: "0 5 * * *", Valid: true},
			Image:        "renovate/renovate:2.0",
		},
		Targets: []string{"https://github.com/myorg/replacement"},
	})
	if err != nil {
		t.Fatalf("UpdatePool failed: %v", err)
	}
	if updated.Name != "pool-update-renamed" {
		t.Errorf("name = %q, want pool-update-renamed", updated.Name)
	}

	renovate, err := database.GetRenovateConfigByPoolId(ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetRenovateConfigByPoolId failed: %v", err)
	}
	if !renovate.Enabled || renovate.Image != "renovate/renovate:2.0" {
		t.Errorf("renovate config not updated: enabled=%v image=%q", renovate.Enabled, renovate.Image)
	}

	targets, err := database.ListPoolTargetsByPoolId(ctx, pool.ID)
	if err != nil {
		t.Fatalf("ListPoolTargetsByPoolId failed: %v", err)
	}
	if len(targets) != 1 || targets[0].TargetUrl != "https://github.com/myorg/replacement" {
		t.Errorf("target rewrite wrong: %+v", targets)
	}

	// In-transaction create fallback for a pool that never had a config row.
	if _, err := database.UpdatePool(ctx, PoolUpdate{
		Pool: poolUpdateParams(bare, "pool-update-bare"),
		Renovate: &UpdateRenovateConfigParams{
			Enabled: true,
			Image:   "renovate/renovate:2.0",
		},
	}); err != nil {
		t.Fatalf("UpdatePool (bare) failed: %v", err)
	}
	created, err := database.GetRenovateConfigByPoolId(ctx, bare.ID)
	if err != nil {
		t.Fatalf("GetRenovateConfigByPoolId (bare) failed: %v", err)
	}
	if !created.Enabled || created.Image != "renovate/renovate:2.0" {
		t.Errorf("renovate config not created for bare pool: enabled=%v image=%q", created.Enabled, created.Image)
	}
}

// TestUpdatePoolNilLegsUntouched: nil Renovate and nil Targets leave the
// stored renovate config and target set alone while the pool row still
// updates.
func TestUpdatePoolNilLegsUntouched(t *testing.T) {
	database, pool, _, cleanup := newPoolUpdateDB(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := database.UpdatePool(ctx, PoolUpdate{
		Pool: poolUpdateParams(pool, "pool-update-renamed"),
	}); err != nil {
		t.Fatalf("UpdatePool failed: %v", err)
	}

	renovate, err := database.GetRenovateConfigByPoolId(ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetRenovateConfigByPoolId failed: %v", err)
	}
	if renovate.Enabled || renovate.Image != "renovate/renovate:1.0" {
		t.Errorf("nil Renovate leg changed the config: enabled=%v image=%q", renovate.Enabled, renovate.Image)
	}

	targets, err := database.ListPoolTargetsByPoolId(ctx, pool.ID)
	if err != nil {
		t.Fatalf("ListPoolTargetsByPoolId failed: %v", err)
	}
	if len(targets) != 2 {
		t.Errorf("nil Targets leg changed the rows: %d targets", len(targets))
	}
}
