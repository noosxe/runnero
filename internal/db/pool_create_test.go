package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// newPoolCreateDB seeds one auth profile so pool creates satisfy the FK
// requirement (RUN-163).
func newPoolCreateDB(t *testing.T) (*DB, int64, func()) {
	t.Helper()
	database, err := Open(Options{Path: filepath.Join(t.TempDir(), "pool_create_test.db")})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	cleanup := func() { _ = database.Close() }

	profile, err := database.CreateAuthProfile(context.Background(), CreateAuthProfileParams{
		Name:                "github-pool-create",
		AuthMethod:          "github_app",
		AppID:               sql.NullInt64{Int64: 12345, Valid: true},
		PrivateKeyEncrypted: sql.NullString{String: "enc_priv_key", Valid: true},
		TokenEncrypted:      sql.NullString{String: "enc_token", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}
	return database, profile.ID, cleanup
}

func poolCreateParams(profileID int64, name string) CreateRunnerPoolParams {
	return CreateRunnerPoolParams{
		Name:          name,
		Provider:      "github",
		RepositoryUrl: "https://github.com/myorg/myrepo",
		Scope:         "repo",
		AuthProfileID: profileID,
		Labels:        `["self-hosted","linux"]`,
		RunnerImage:   "ghcr.io/noosxe/runnero:latest",
	}
}

// TestCreatePoolAtomicRollback proves the whole create sequence rolls back
// when a leg fails mid-transaction (RUN-163): a test-only trigger aborts the
// renovate-config insert, and the earlier pool row must not survive either.
// Previously the legs ran sequentially with swallowed errors, so partial
// creates went unnoticed.
func TestCreatePoolAtomicRollback(t *testing.T) {
	database, profileID, cleanup := newPoolCreateDB(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := database.sqlDB.ExecContext(ctx,
		`CREATE TRIGGER pool_create_test_fail BEFORE INSERT ON renovate_configs
		 BEGIN SELECT RAISE(ABORT, 'boom-renovate'); END;`); err != nil {
		t.Fatalf("creating failure trigger failed: %v", err)
	}

	_, err := database.CreatePool(ctx, PoolCreate{
		Pool: poolCreateParams(profileID, "pool-create-boom"),
		Renovate: &CreateRenovateConfigParams{
			Enabled:      true,
			CronSchedule: sql.NullString{String: "0 3 * * *", Valid: true},
			Image:        "renovate/renovate:1.0",
		},
		Targets: []string{"https://github.com/myorg/myrepo"},
	})
	if err == nil || !strings.Contains(err.Error(), "boom-renovate") {
		t.Fatalf("expected the triggered insert failure, got %v", err)
	}

	if _, err := database.GetRunnerPoolByName(ctx, "pool-create-boom"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("pool row leaked through the rollback, lookup err = %v", err)
	}
}

// TestCreatePoolCommit verifies the full commit path: pool row, renovate
// config, and seed targets all land with the pool id threaded through.
func TestCreatePoolCommit(t *testing.T) {
	database, profileID, cleanup := newPoolCreateDB(t)
	defer cleanup()
	ctx := context.Background()

	created, err := database.CreatePool(ctx, PoolCreate{
		Pool: poolCreateParams(profileID, "pool-create-full"),
		Renovate: &CreateRenovateConfigParams{
			Enabled:      false,
			CronSchedule: sql.NullString{String: "0 3 * * *", Valid: true},
			Image:        "renovate/renovate:1.0",
		},
		Targets: []string{
			"https://github.com/myorg/myrepo",
			"https://github.com/myorg/other",
		},
	})
	if err != nil {
		t.Fatalf("CreatePool failed: %v", err)
	}

	renovate, err := database.GetRenovateConfigByPoolId(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetRenovateConfigByPoolId failed: %v", err)
	}
	if renovate.PoolID != created.ID || renovate.Image != "renovate/renovate:1.0" {
		t.Errorf("unexpected renovate config: %+v", renovate)
	}

	targets, err := database.ListPoolTargetsByPoolId(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListPoolTargetsByPoolId failed: %v", err)
	}
	if len(targets) != 2 || targets[0].TargetUrl != "https://github.com/myorg/myrepo" {
		t.Errorf("unexpected targets: %+v", targets)
	}
}

// TestCreatePoolOptionalLegs verifies that a nil Renovate creates no config
// row and empty targets create no target rows — absence is a valid request
// shape, not an error.
func TestCreatePoolOptionalLegs(t *testing.T) {
	database, profileID, cleanup := newPoolCreateDB(t)
	defer cleanup()
	ctx := context.Background()

	created, err := database.CreatePool(ctx, PoolCreate{
		Pool:    poolCreateParams(profileID, "pool-create-bare"),
		Targets: []string{},
	})
	if err != nil {
		t.Fatalf("CreatePool failed: %v", err)
	}

	if _, err := database.GetRenovateConfigByPoolId(ctx, created.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected no renovate config, got err = %v", err)
	}
	targets, err := database.ListPoolTargetsByPoolId(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListPoolTargetsByPoolId failed: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("expected no targets, got %+v", targets)
	}
}

// TestCreatePoolDuplicateName pins the UNIQUE violation surfacing (RUN-163):
// the server maps it to CodeAlreadyExists (docs/22 §3.1) via
// IsUniqueConstraintError, which must see through the transaction wrapper.
func TestCreatePoolDuplicateName(t *testing.T) {
	database, profileID, cleanup := newPoolCreateDB(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := database.CreatePool(ctx, PoolCreate{
		Pool:    poolCreateParams(profileID, "pool-create-dup"),
		Targets: []string{"https://github.com/myorg/myrepo"},
	}); err != nil {
		t.Fatalf("first CreatePool failed: %v", err)
	}

	_, err := database.CreatePool(ctx, PoolCreate{
		Pool:    poolCreateParams(profileID, "pool-create-dup"),
		Targets: []string{"https://github.com/myorg/myrepo"},
	})
	if !IsUniqueConstraintError(err) {
		t.Fatalf("expected UNIQUE constraint error, got %v", err)
	}
}
