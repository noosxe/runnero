package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestQueriesRoundTrip exercises all sqlc-generated type-safe queries
// against a temporary SQLite database (RUN-14 acceptance).
func TestQueriesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "queries_test.db")

	database, err := Open(Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()

	// 1. admin_users
	t.Run("admin_users", func(t *testing.T) {
		count, err := database.CountAdminUsers(ctx)
		if err != nil || count != 0 {
			t.Fatalf("CountAdminUsers = %d (err: %v), want 0", count, err)
		}

		user, err := database.CreateAdminUser(ctx, CreateAdminUserParams{
			Username:     "admin",
			PasswordHash: "$2a$12$hashedpasswordsample",
		})
		if err != nil {
			t.Fatalf("CreateAdminUser failed: %v", err)
		}
		if user.Username != "admin" || user.ID <= 0 {
			t.Fatalf("unexpected user created: %+v", user)
		}

		byID, err := database.GetAdminUserById(ctx, user.ID)
		if err != nil || byID.Username != "admin" {
			t.Fatalf("GetAdminUserById failed: %v, got %+v", err, byID)
		}

		byName, err := database.GetAdminUserByUsername(ctx, "admin")
		if err != nil || byName.ID != user.ID {
			t.Fatalf("GetAdminUserByUsername failed: %v, got %+v", err, byName)
		}

		users, err := database.ListAdminUsers(ctx)
		if err != nil || len(users) != 1 {
			t.Fatalf("ListAdminUsers failed: %v, count %d", err, len(users))
		}

		updated, err := database.UpdateAdminPassword(ctx, UpdateAdminPasswordParams{
			PasswordHash: "new_hash",
			ID:           user.ID,
		})
		if err != nil || updated.PasswordHash != "new_hash" {
			t.Fatalf("UpdateAdminPassword failed: %v, got %+v", err, updated)
		}

		countAfter, err := database.CountAdminUsers(ctx)
		if err != nil || countAfter != 1 {
			t.Fatalf("CountAdminUsers = %d, want 1", countAfter)
		}
	})

	// 2. sessions
	t.Run("sessions", func(t *testing.T) {
		admin, err := database.GetAdminUserByUsername(ctx, "admin")
		if err != nil {
			t.Fatalf("fetching admin user: %v", err)
		}

		sess, err := database.CreateSession(ctx, CreateSessionParams{
			UserID:    admin.ID,
			TokenHash: "token_hash_abc123",
			ExpiresAt: time.Now().Add(24 * time.Hour),
		})
		if err != nil {
			t.Fatalf("CreateSession failed: %v", err)
		}
		if sess.TokenHash != "token_hash_abc123" {
			t.Fatalf("unexpected session: %+v", sess)
		}

		byHash, err := database.GetSessionByTokenHash(ctx, "token_hash_abc123")
		if err != nil || byHash.ID != sess.ID {
			t.Fatalf("GetSessionByTokenHash failed: %v, got %+v", err, byHash)
		}

		byUser, err := database.ListSessionsByUserId(ctx, admin.ID)
		if err != nil || len(byUser) != 1 {
			t.Fatalf("ListSessionsByUserId failed: %v, count %d", err, len(byUser))
		}

		if err := database.DeleteExpiredSessions(ctx, time.Now().Add(48*time.Hour)); err != nil {
			t.Fatalf("DeleteExpiredSessions failed: %v", err)
		}

		_, err = database.GetSessionByTokenHash(ctx, "token_hash_abc123")
		if err != sql.ErrNoRows {
			t.Fatalf("expected sql.ErrNoRows after DeleteExpiredSessions, got %v", err)
		}
	})

	// 3. auth_profiles
	t.Run("auth_profiles", func(t *testing.T) {
		profile, err := database.CreateAuthProfile(ctx, CreateAuthProfileParams{
			Name:                "github-prod",
			AuthMethod:          "github_app",
			AppID:               sql.NullInt64{Int64: 12345, Valid: true},
			PrivateKeyEncrypted: sql.NullString{String: "enc_priv_key", Valid: true},
			TokenEncrypted:      sql.NullString{String: "enc_token", Valid: true},
		})
		if err != nil {
			t.Fatalf("CreateAuthProfile failed: %v", err)
		}
		if profile.Name != "github-prod" {
			t.Fatalf("unexpected profile: %+v", profile)
		}

		count, err := database.CountAuthProfiles(ctx)
		if err != nil || count != 1 {
			t.Fatalf("CountAuthProfiles = %d, want 1", count)
		}

		byID, err := database.GetAuthProfileById(ctx, profile.ID)
		if err != nil || byID.Name != "github-prod" {
			t.Fatalf("GetAuthProfileById failed: %v", err)
		}

		byName, err := database.GetAuthProfileByName(ctx, "github-prod")
		if err != nil || byName.ID != profile.ID {
			t.Fatalf("GetAuthProfileByName failed: %v", err)
		}

		updated, err := database.UpdateAuthProfile(ctx, UpdateAuthProfileParams{
			Name:                "github-prod-renamed",
			AuthMethod:          "github_app",
			AppID:               sql.NullInt64{Int64: 99999, Valid: true},
			PrivateKeyEncrypted: sql.NullString{String: "enc_priv_key_updated", Valid: true},
			TokenEncrypted:      sql.NullString{String: "enc_token_updated", Valid: true},
			ID:                  profile.ID,
		})
		if err != nil || updated.Name != "github-prod-renamed" {
			t.Fatalf("UpdateAuthProfile failed: %v, got %+v", err, updated)
		}

		list, err := database.ListAuthProfiles(ctx)
		if err != nil || len(list) != 1 {
			t.Fatalf("ListAuthProfiles failed: %v", err)
		}
	})

	// 4. runner_pools
	t.Run("runner_pools", func(t *testing.T) {
		profiles, err := database.ListAuthProfiles(ctx)
		if err != nil || len(profiles) == 0 {
			t.Fatalf("ListAuthProfiles empty: %v", err)
		}
		authID := profiles[0].ID

		pool, err := database.CreateRunnerPool(ctx, CreateRunnerPoolParams{
			Name:                     "arm64-pool",
			Provider:                 "github",
			RepositoryUrl:            "https://github.com/myorg/myrepo",
			Scope:                    "repo",
			AuthProfileID:            authID,
			MinIdleRunners:           2,
			MaxConcurrency:           10,
			Labels:                   `["self-hosted","linux","arm64"]`,
			RunnerImage:              "ghcr.io/noosxe/runnero:latest",
			AllowDocker:              true,
			MaxRunnerLifetimeSeconds: 3600,
			CpuLimit:                 sql.NullString{String: "2", Valid: true},
			MemoryLimit:              sql.NullString{String: "4Gi", Valid: true},
		})
		if err != nil {
			t.Fatalf("CreateRunnerPool failed: %v", err)
		}
		if pool.Name != "arm64-pool" || pool.MinIdleRunners != 2 {
			t.Fatalf("unexpected pool: %+v", pool)
		}

		count, err := database.CountRunnerPools(ctx)
		if err != nil || count != 1 {
			t.Fatalf("CountRunnerPools = %d, want 1", count)
		}

		byID, err := database.GetRunnerPoolById(ctx, pool.ID)
		if err != nil || byID.Name != "arm64-pool" {
			t.Fatalf("GetRunnerPoolById failed: %v", err)
		}

		byName, err := database.GetRunnerPoolByName(ctx, "arm64-pool")
		if err != nil || byName.ID != pool.ID {
			t.Fatalf("GetRunnerPoolByName failed: %v", err)
		}

		updated, err := database.UpdateRunnerPool(ctx, UpdateRunnerPoolParams{
			Name:                     "arm64-pool-v2",
			Provider:                 "github",
			RepositoryUrl:            "https://github.com/myorg/myrepo",
			Scope:                    "repo",
			AuthProfileID:            authID,
			MinIdleRunners:           3,
			MaxConcurrency:           15,
			Labels:                   `["self-hosted","linux","arm64","heavy"]`,
			RunnerImage:              "ghcr.io/noosxe/runnero:v2",
			AllowDocker:              false,
			MaxRunnerLifetimeSeconds: 7200,
			CpuLimit:                 sql.NullString{String: "4", Valid: true},
			MemoryLimit:              sql.NullString{String: "8Gi", Valid: true},
			ID:                       pool.ID,
		})
		if err != nil || updated.Name != "arm64-pool-v2" || updated.MinIdleRunners != 3 {
			t.Fatalf("UpdateRunnerPool failed: %v, got %+v", err, updated)
		}

		pools, err := database.ListRunnerPools(ctx)
		if err != nil || len(pools) != 1 {
			t.Fatalf("ListRunnerPools failed: %v", err)
		}
	})

	// 5. renovate_configs
	t.Run("renovate_configs", func(t *testing.T) {
		pools, err := database.ListRunnerPools(ctx)
		if err != nil || len(pools) == 0 {
			t.Fatalf("ListRunnerPools empty: %v", err)
		}
		poolID := pools[0].ID

		renovate, err := database.CreateRenovateConfig(ctx, CreateRenovateConfigParams{
			PoolID:       poolID,
			Enabled:      true,
			CronSchedule: sql.NullString{String: "0 0 * * *", Valid: true},
			Image:        "renovate/renovate:latest",
		})
		if err != nil {
			t.Fatalf("CreateRenovateConfig failed: %v", err)
		}
		if !renovate.Enabled || renovate.PoolID != poolID {
			t.Fatalf("unexpected renovate config: %+v", renovate)
		}

		byPool, err := database.GetRenovateConfigByPoolId(ctx, poolID)
		if err != nil || byPool.ID != renovate.ID {
			t.Fatalf("GetRenovateConfigByPoolId failed: %v", err)
		}

		enabledList, err := database.ListEnabledRenovateConfigs(ctx)
		if err != nil || len(enabledList) != 1 {
			t.Fatalf("ListEnabledRenovateConfigs failed: %v", err)
		}

		updated, err := database.UpdateRenovateConfig(ctx, UpdateRenovateConfigParams{
			Enabled:      false,
			CronSchedule: sql.NullString{String: "0 12 * * *", Valid: true},
			Image:        "renovate/renovate:38",
			PoolID:       poolID,
		})
		if err != nil || updated.Enabled {
			t.Fatalf("UpdateRenovateConfig failed: %v, got %+v", err, updated)
		}

		enabledListAfter, err := database.ListEnabledRenovateConfigs(ctx)
		if err != nil || len(enabledListAfter) != 0 {
			t.Fatalf("ListEnabledRenovateConfigs after disable should be 0, got %d", len(enabledListAfter))
		}
	})

	// 6. job_history
	t.Run("job_history", func(t *testing.T) {
		pools, err := database.ListRunnerPools(ctx)
		if err != nil || len(pools) == 0 {
			t.Fatalf("ListRunnerPools empty: %v", err)
		}
		poolID := pools[0].ID

		now := time.Now()
		job, err := database.CreateJobHistory(ctx, CreateJobHistoryParams{
			PoolID:           poolID,
			RunnerName:       "runner-node-1",
			Status:           "success",
			QueuedAt:         sql.NullTime{Time: now.Add(-10 * time.Minute), Valid: true},
			StartedAt:        sql.NullTime{Time: now.Add(-9 * time.Minute), Valid: true},
			CompletedAt:      sql.NullTime{Time: now, Valid: true},
			LogRetentionPath: sql.NullString{String: "/logs/runner-1.log", Valid: true},
			Source:           "webhook",
		})
		if err != nil {
			t.Fatalf("CreateJobHistory failed: %v", err)
		}
		if job.RunnerName != "runner-node-1" || job.Status != "success" {
			t.Fatalf("unexpected job: %+v", job)
		}

		byID, err := database.GetJobHistoryById(ctx, job.ID)
		if err != nil || byID.RunnerName != "runner-node-1" {
			t.Fatalf("GetJobHistoryById failed: %v", err)
		}

		jobs, err := database.ListJobHistory(ctx, ListJobHistoryParams{Limit: 10, Offset: 0})
		if err != nil || len(jobs) != 1 {
			t.Fatalf("ListJobHistory failed: %v", err)
		}

		poolJobs, err := database.ListJobHistoryByPoolId(ctx, ListJobHistoryByPoolIdParams{
			PoolID: poolID,
			Limit:  10,
			Offset: 0,
		})
		if err != nil || len(poolJobs) != 1 {
			t.Fatalf("ListJobHistoryByPoolId failed: %v", err)
		}

		count, err := database.CountJobHistory(ctx)
		if err != nil || count != 1 {
			t.Fatalf("CountJobHistory = %d, want 1", count)
		}

		countPool, err := database.CountJobHistoryByPoolId(ctx, poolID)
		if err != nil || countPool != 1 {
			t.Fatalf("CountJobHistoryByPoolId = %d, want 1", countPool)
		}

		updated, err := database.UpdateJobHistoryStatus(ctx, UpdateJobHistoryStatusParams{
			Status:           "timeout",
			CompletedAt:      sql.NullTime{Time: now.Add(time.Minute), Valid: true},
			LogRetentionPath: sql.NullString{String: "/logs/runner-1-timeout.log", Valid: true},
			ID:               job.ID,
		})
		if err != nil || updated.Status != "timeout" {
			t.Fatalf("UpdateJobHistoryStatus failed: %v, got %+v", err, updated)
		}

		// Retention pruning test
		if err := database.DeleteJobHistoryOlderThan(ctx, sql.NullTime{Time: now.Add(time.Hour), Valid: true}); err != nil {
			t.Fatalf("DeleteJobHistoryOlderThan failed: %v", err)
		}

		countPruned, err := database.CountJobHistory(ctx)
		if err != nil || countPruned != 0 {
			t.Fatalf("CountJobHistory after pruning = %d, want 0", countPruned)
		}
	})

	// 7. audit_logs
	t.Run("audit_logs", func(t *testing.T) {
		admin, err := database.GetAdminUserByUsername(ctx, "admin")
		if err != nil {
			t.Fatalf("fetching admin user: %v", err)
		}

		logEntry, err := database.CreateAuditLog(ctx, CreateAuditLogParams{
			UserID:       sql.NullInt64{Int64: admin.ID, Valid: true},
			Action:       "pool.create",
			ResourceType: sql.NullString{String: "runner_pool", Valid: true},
			ResourceID:   sql.NullInt64{Int64: 1, Valid: true},
			Details:      sql.NullString{String: `{"name":"arm64-pool"}`, Valid: true},
		})
		if err != nil {
			t.Fatalf("CreateAuditLog failed: %v", err)
		}
		if logEntry.Action != "pool.create" {
			t.Fatalf("unexpected audit log: %+v", logEntry)
		}

		count, err := database.CountAuditLogs(ctx)
		if err != nil || count != 1 {
			t.Fatalf("CountAuditLogs = %d, want 1", count)
		}

		byID, err := database.GetAuditLogById(ctx, logEntry.ID)
		if err != nil || byID.Action != "pool.create" {
			t.Fatalf("GetAuditLogById failed: %v", err)
		}

		logs, err := database.ListAuditLogs(ctx, ListAuditLogsParams{Limit: 10, Offset: 0})
		if err != nil || len(logs) != 1 {
			t.Fatalf("ListAuditLogs failed: %v", err)
		}

		userLogs, err := database.ListAuditLogsByUserId(ctx, ListAuditLogsByUserIdParams{
			UserID: sql.NullInt64{Int64: admin.ID, Valid: true},
			Limit:  10,
			Offset: 0,
		})
		if err != nil || len(userLogs) != 1 {
			t.Fatalf("ListAuditLogsByUserId failed: %v", err)
		}
	})

	// 8. app_settings
	t.Run("app_settings", func(t *testing.T) {
		setting, err := database.GetAppSetting(ctx, "total_allowed_runners")
		if err != nil || setting.Value != "20" {
			t.Fatalf("GetAppSetting(total_allowed_runners) = %q (err: %v), want '20'", setting.Value, err)
		}

		// Upsert / SetAppSetting
		updated, err := database.SetAppSetting(ctx, SetAppSettingParams{
			Key:   "total_allowed_runners",
			Value: "50",
		})
		if err != nil || updated.Value != "50" {
			t.Fatalf("SetAppSetting failed: %v, got %+v", err, updated)
		}

		verify, err := database.GetAppSetting(ctx, "total_allowed_runners")
		if err != nil || verify.Value != "50" {
			t.Fatalf("verify SetAppSetting = %q, want '50'", verify.Value)
		}

		// New setting
		custom, err := database.SetAppSetting(ctx, SetAppSettingParams{
			Key:   "custom_feature_flag",
			Value: "enabled",
		})
		if err != nil || custom.Value != "enabled" {
			t.Fatalf("SetAppSetting new key failed: %v", err)
		}

		list, err := database.ListAppSettings(ctx)
		if err != nil || len(list) != 5 {
			t.Fatalf("ListAppSettings count = %d, want 5", len(list))
		}

		if err := database.DeleteAppSetting(ctx, "custom_feature_flag"); err != nil {
			t.Fatalf("DeleteAppSetting failed: %v", err)
		}

		listAfter, err := database.ListAppSettings(ctx)
		if err != nil || len(listAfter) != 4 {
			t.Fatalf("ListAppSettings count after delete = %d, want 4", len(listAfter))
		}
	})

	// 9. renovate_runs
	t.Run("renovate_runs", func(t *testing.T) {
		pools, err := database.ListRunnerPools(ctx)
		if err != nil || len(pools) == 0 {
			t.Fatalf("ListRunnerPools failed: %v", err)
		}
		poolID := pools[0].ID

		count, err := database.CountRenovateRunsByPoolId(ctx, poolID)
		if err != nil || count != 0 {
			t.Fatalf("CountRenovateRunsByPoolId = %d (err: %v), want 0", count, err)
		}

		now := time.Now().UTC()
		run, err := database.CreateRenovateRun(ctx, CreateRenovateRunParams{
			PoolID:    poolID,
			Status:    "running",
			StartedAt: now,
		})
		if err != nil {
			t.Fatalf("CreateRenovateRun failed: %v", err)
		}
		if run.Status != "running" || run.PoolID != poolID {
			t.Fatalf("unexpected renovate run: %+v", run)
		}

		err = database.UpdateRenovateRunContainerID(ctx, UpdateRenovateRunContainerIDParams{
			ContainerID: sql.NullString{String: "c-123", Valid: true},
			ID:          run.ID,
		})
		if err != nil {
			t.Fatalf("UpdateRenovateRunContainerID failed: %v", err)
		}

		byContainer, err := database.GetRenovateRunByContainerID(ctx, sql.NullString{String: "c-123", Valid: true})
		if err != nil || byContainer.ID != run.ID {
			t.Fatalf("GetRenovateRunByContainerID failed: %v, got %+v", err, byContainer)
		}

		completedAt := time.Now().UTC()
		completed, err := database.CompleteRenovateRun(ctx, CompleteRenovateRunParams{
			Status:      "success",
			CompletedAt: sql.NullTime{Time: completedAt, Valid: true},
			Summary:     "3 PRs created",
			ID:          run.ID,
		})
		if err != nil {
			t.Fatalf("CompleteRenovateRun failed: %v", err)
		}
		if completed.Status != "success" || completed.Summary != "3 PRs created" {
			t.Fatalf("unexpected completed run: %+v", completed)
		}

		latest, err := database.GetLatestRenovateRunByPoolId(ctx, poolID)
		if err != nil || latest.ID != run.ID {
			t.Fatalf("GetLatestRenovateRunByPoolId failed: %v, got %+v", err, latest)
		}

		runsList, err := database.ListRenovateRunsByPoolId(ctx, ListRenovateRunsByPoolIdParams{
			PoolID: poolID,
			Limit:  10,
			Offset: 0,
		})
		if err != nil || len(runsList) != 1 {
			t.Fatalf("ListRenovateRunsByPoolId failed: %v, count %d", err, len(runsList))
		}
	})

	// 10. pool_targets
	t.Run("pool_targets", func(t *testing.T) {
		pools, err := database.ListRunnerPools(ctx)
		if err != nil || len(pools) == 0 {
			t.Fatalf("ListRunnerPools failed: %v", err)
		}
		poolID := pools[0].ID

		// Targets backfilled or added
		initialTargets, err := database.ListPoolTargetsByPoolId(ctx, poolID)
		if err != nil {
			t.Fatalf("ListPoolTargetsByPoolId failed: %v", err)
		}

		target2, err := database.AddPoolTarget(ctx, AddPoolTargetParams{
			PoolID:    poolID,
			TargetUrl: "https://github.com/another/repo",
		})
		if err != nil {
			t.Fatalf("AddPoolTarget failed: %v", err)
		}
		if target2.TargetUrl != "https://github.com/another/repo" || target2.PoolID != poolID {
			t.Fatalf("unexpected target created: %+v", target2)
		}

		// Verify duplicate rejection
		_, err = database.AddPoolTarget(ctx, AddPoolTargetParams{
			PoolID:    poolID,
			TargetUrl: "https://github.com/another/repo",
		})
		if err == nil {
			t.Fatal("expected error on duplicate pool target, got nil")
		}

		allTargets, err := database.ListPoolTargetsByPoolId(ctx, poolID)
		if err != nil || len(allTargets) != len(initialTargets)+1 {
			t.Fatalf("unexpected target count: %d (initial: %d)", len(allTargets), len(initialTargets))
		}

		foundPool, err := database.GetPoolByTargetUrl(ctx, "https://github.com/another/repo")
		if err != nil || foundPool.ID != poolID {
			t.Fatalf("GetPoolByTargetUrl failed: %v, got %+v", err, foundPool)
		}

		err = database.DeletePoolTarget(ctx, DeletePoolTargetParams{
			PoolID:    poolID,
			TargetUrl: "https://github.com/another/repo",
		})
		if err != nil {
			t.Fatalf("DeletePoolTarget failed: %v", err)
		}

		_, err = database.GetPoolByTargetUrl(ctx, "https://github.com/another/repo")
		if err != sql.ErrNoRows {
			t.Fatalf("expected sql.ErrNoRows after delete, got %v", err)
		}
	})

	// 11. Transactions via WithTx
	t.Run("transactions_with_tx", func(t *testing.T) {
		tx, err := database.SQL().BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx failed: %v", err)
		}
		qtx := database.WithTx(tx)

		_, err = qtx.SetAppSetting(ctx, SetAppSettingParams{
			Key:   "tx_setting",
			Value: "tx_val",
		})
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("SetAppSetting in tx failed: %v", err)
		}

		if err := tx.Rollback(); err != nil {
			t.Fatalf("Rollback failed: %v", err)
		}

		// Setting should not exist after rollback
		_, err = database.GetAppSetting(ctx, "tx_setting")
		if err != sql.ErrNoRows {
			t.Fatalf("expected sql.ErrNoRows after rollback, got %v", err)
		}
	})
}
