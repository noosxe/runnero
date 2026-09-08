package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	AppSettingSeedImported = "seed_imported"

	// SanitizedRedactedPlaceholder is placed in exported YAML where raw secrets would otherwise exist.
	SanitizedRedactedPlaceholder = "${REDACTED}"
)

type ImportMode string

const (
	ImportModeOverwrite ImportMode = "overwrite"
	ImportModeMerge     ImportMode = "merge"
)

// SeedConfig represents the top-level YAML configuration schema per docs/02 §4.
type SeedConfig struct {
	Version      string                     `yaml:"version"`
	Global       GlobalConfig               `yaml:"global,omitempty"`
	AuthProfiles map[string]SeedAuthProfile `yaml:"auth_profiles,omitempty"`
	Pools        []SeedPool                 `yaml:"pools,omitempty"`
}

type GlobalConfig struct {
	CheckIntervalSeconds   int      `yaml:"check_interval_seconds,omitempty"`
	DefaultLabels          []string `yaml:"default_labels,omitempty"`
	TotalAllowedRunners    int      `yaml:"total_allowed_runners,omitempty"`
	TotalIdleWarmPool      int      `yaml:"total_idle_warm_pool,omitempty"`
	ShutdownTimeoutSeconds int      `yaml:"shutdown_timeout_seconds,omitempty"`
	JobRetentionDays       int      `yaml:"job_retention_days,omitempty"`
}

type SeedAuthProfile struct {
	AuthMethod         string `yaml:"auth_method"`
	AppID              int64  `yaml:"app_id,omitempty"`
	PrivateKeyPath     string `yaml:"private_key_path,omitempty"`
	PrivateKey         string `yaml:"private_key,omitempty"`
	GiteaTokenEnvVar   string `yaml:"gitea_token_env_var,omitempty"`
	ForgejoTokenEnvVar string `yaml:"forgejo_token_env_var,omitempty"`
	TokenEnvVar        string `yaml:"token_env_var,omitempty"`
	Token              string `yaml:"token,omitempty"`
}

type SeedPool struct {
	Name                     string        `yaml:"name"`
	Provider                 string        `yaml:"provider"`
	RepositoryURL            string        `yaml:"repository_url"`
	Scope                    string        `yaml:"scope,omitempty"`
	AuthProfile              string        `yaml:"auth_profile"`
	MinIdleRunners           int64         `yaml:"min_idle_runners"`
	MaxConcurrency           int64         `yaml:"max_concurrency"`
	Labels                   []string      `yaml:"labels"`
	RunnerImage              string        `yaml:"runner_image"`
	AllowDocker              bool          `yaml:"allow_docker"`
	MaxRunnerLifetimeSeconds int64         `yaml:"max_runner_lifetime_seconds"`
	CPULimit                 string        `yaml:"cpu_limit,omitempty"`
	MemoryLimit              string        `yaml:"memory_limit,omitempty"`
	Renovate                 *SeedRenovate `yaml:"renovate,omitempty"`
}

type SeedRenovate struct {
	Enabled      bool   `yaml:"enabled"`
	CronSchedule string `yaml:"cron_schedule,omitempty"`
	Image        string `yaml:"image,omitempty"`
}

// ParseSeedConfig unmarshals YAML into SeedConfig and validates its structure.
func ParseSeedConfig(data []byte) (*SeedConfig, error) {
	var cfg SeedConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing YAML config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}
	return &cfg, nil
}

// ToYAML serializes the SeedConfig to YAML bytes.
func (s *SeedConfig) ToYAML() ([]byte, error) {
	return yaml.Marshal(s)
}

// Validate checks pool references, provider values, scopes, and auth methods.
func (s *SeedConfig) Validate() error {
	validAuthMethods := map[string]bool{
		"github_app":    true,
		"gitea_token":   true,
		"forgejo_token": true,
		"pat":           true,
	}
	for name, prof := range s.AuthProfiles {
		if strings.TrimSpace(name) == "" {
			return errors.New("auth_profile name must not be empty")
		}
		if !validAuthMethods[prof.AuthMethod] {
			return fmt.Errorf("auth_profile %q: invalid auth_method %q (allowed: github_app, gitea_token, forgejo_token, pat)", name, prof.AuthMethod)
		}
		if prof.AuthMethod == "github_app" && prof.AppID == 0 {
			return fmt.Errorf("auth_profile %q: github_app requires non-zero app_id", name)
		}
	}

	validProviders := map[string]bool{
		"github":  true,
		"gitea":   true,
		"forgejo": true,
	}
	validScopes := map[string]bool{
		"repo":   true,
		"org":    true,
		"global": true,
	}

	poolNames := make(map[string]bool)
	for i, pool := range s.Pools {
		if strings.TrimSpace(pool.Name) == "" {
			return fmt.Errorf("pool[%d]: name must not be empty", i)
		}
		if poolNames[pool.Name] {
			return fmt.Errorf("duplicate pool name %q", pool.Name)
		}
		poolNames[pool.Name] = true

		if !validProviders[pool.Provider] {
			return fmt.Errorf("pool %q: invalid provider %q (allowed: github, gitea, forgejo)", pool.Name, pool.Provider)
		}
		if strings.TrimSpace(pool.RepositoryURL) == "" {
			return fmt.Errorf("pool %q: repository_url must not be empty", pool.Name)
		}
		scope := pool.Scope
		if scope == "" {
			scope = "repo"
		}
		if !validScopes[scope] {
			return fmt.Errorf("pool %q: invalid scope %q (allowed: repo, org, global)", pool.Name, pool.Scope)
		}
		if len(pool.Labels) == 0 {
			return fmt.Errorf("pool %q: labels must not be empty", pool.Name)
		}
		if strings.TrimSpace(pool.RunnerImage) == "" {
			return fmt.Errorf("pool %q: runner_image must not be empty", pool.Name)
		}
		if strings.TrimSpace(pool.AuthProfile) == "" {
			return fmt.Errorf("pool %q: auth_profile reference must not be empty", pool.Name)
		}
	}
	return nil
}

// ShouldAutoImportSeed determines if an initial seed import is needed on boot.
func (d *DB) ShouldAutoImportSeed(ctx context.Context) (bool, error) {
	setting, err := d.GetAppSetting(ctx, AppSettingSeedImported)
	if err == nil && setting.Value == "true" {
		return false, nil
	}

	poolsCount, err := d.CountRunnerPools(ctx)
	if err != nil {
		return false, err
	}
	profilesCount, err := d.CountAuthProfiles(ctx)
	if err != nil {
		return false, err
	}

	return poolsCount == 0 && profilesCount == 0, nil
}

// MarkSeedImported records that seed data was imported so subsequent boots ignore YAML.
func (d *DB) MarkSeedImported(ctx context.Context) error {
	_, err := d.SetAppSetting(ctx, SetAppSettingParams{
		Key:   AppSettingSeedImported,
		Value: "true",
	})
	return err
}

// ImportSeedConfig imports a validated SeedConfig into the SQLite database.
func (d *DB) ImportSeedConfig(ctx context.Context, cfg *SeedConfig, mode ImportMode) error {
	if d.encryptor == nil {
		return ErrNoEncryptor
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	tx, err := d.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := d.WithTx(tx)

	if mode == ImportModeOverwrite {
		// Clean out existing pools and auth profiles
		pools, err := qtx.ListRunnerPools(ctx)
		if err != nil {
			return fmt.Errorf("listing pools for overwrite: %w", err)
		}
		for _, p := range pools {
			if err := qtx.DeleteRunnerPool(ctx, p.ID); err != nil {
				return fmt.Errorf("deleting pool %q: %w", p.Name, err)
			}
		}

		profiles, err := qtx.ListAuthProfiles(ctx)
		if err != nil {
			return fmt.Errorf("listing auth profiles for overwrite: %w", err)
		}
		for _, p := range profiles {
			if err := qtx.DeleteAuthProfile(ctx, p.ID); err != nil {
				return fmt.Errorf("deleting auth profile %q: %w", p.Name, err)
			}
		}
	}

	// 1. Import Global Settings
	if cfg.Global.TotalAllowedRunners > 0 {
		if _, err := qtx.SetAppSetting(ctx, SetAppSettingParams{
			Key: "total_allowed_runners", Value: strconv.Itoa(cfg.Global.TotalAllowedRunners),
		}); err != nil {
			return fmt.Errorf("setting total_allowed_runners: %w", err)
		}
	}
	if cfg.Global.TotalIdleWarmPool > 0 {
		if _, err := qtx.SetAppSetting(ctx, SetAppSettingParams{
			Key: "total_idle_warm_pool", Value: strconv.Itoa(cfg.Global.TotalIdleWarmPool),
		}); err != nil {
			return fmt.Errorf("setting total_idle_warm_pool: %w", err)
		}
	}
	if cfg.Global.ShutdownTimeoutSeconds > 0 {
		if _, err := qtx.SetAppSetting(ctx, SetAppSettingParams{
			Key: "shutdown_timeout_seconds", Value: strconv.Itoa(cfg.Global.ShutdownTimeoutSeconds),
		}); err != nil {
			return fmt.Errorf("setting shutdown_timeout_seconds: %w", err)
		}
	}
	if cfg.Global.JobRetentionDays > 0 {
		if _, err := qtx.SetAppSetting(ctx, SetAppSettingParams{
			Key: "job_retention_days", Value: strconv.Itoa(cfg.Global.JobRetentionDays),
		}); err != nil {
			return fmt.Errorf("setting job_retention_days: %w", err)
		}
	}

	// 2. Import Auth Profiles
	authProfileIDs := make(map[string]int64)
	for name, prof := range cfg.AuthProfiles {
		privKey, token, err := resolveCredentials(prof)
		if err != nil {
			return fmt.Errorf("auth_profile %q: resolving credentials: %w", name, err)
		}

		encPriv, err := d.encryptor.EncryptNullString(sql.NullString{String: privKey, Valid: privKey != ""})
		if err != nil {
			return fmt.Errorf("encrypting private key for %q: %w", name, err)
		}
		encTok, err := d.encryptor.EncryptNullString(sql.NullString{String: token, Valid: token != ""})
		if err != nil {
			return fmt.Errorf("encrypting token for %q: %w", name, err)
		}

		appID := sql.NullInt64{Int64: prof.AppID, Valid: prof.AppID > 0}

		existing, err := qtx.GetAuthProfileByName(ctx, name)
		if err == nil {
			// Update existing: preserve existing encrypted secrets if incoming credentials are empty or redacted
			if !encPriv.Valid && existing.PrivateKeyEncrypted.Valid {
				encPriv = existing.PrivateKeyEncrypted
			}
			if !encTok.Valid && existing.TokenEncrypted.Valid {
				encTok = existing.TokenEncrypted
			}

			updated, err := qtx.UpdateAuthProfile(ctx, UpdateAuthProfileParams{
				Name:                name,
				AuthMethod:          prof.AuthMethod,
				AppID:               appID,
				PrivateKeyEncrypted: encPriv,
				TokenEncrypted:      encTok,
				ID:                  existing.ID,
			})
			if err != nil {
				return fmt.Errorf("updating auth profile %q: %w", name, err)
			}
			authProfileIDs[name] = updated.ID
		} else if errors.Is(err, sql.ErrNoRows) {
			// Insert new
			created, err := qtx.CreateAuthProfile(ctx, CreateAuthProfileParams{
				Name:                name,
				AuthMethod:          prof.AuthMethod,
				AppID:               appID,
				PrivateKeyEncrypted: encPriv,
				TokenEncrypted:      encTok,
			})
			if err != nil {
				return fmt.Errorf("creating auth profile %q: %w", name, err)
			}
			authProfileIDs[name] = created.ID
		} else {
			return fmt.Errorf("checking auth profile %q: %w", name, err)
		}
	}

	// 3. Import Pools
	for _, pool := range cfg.Pools {
		authID, ok := authProfileIDs[pool.AuthProfile]
		if !ok {
			// Check if already in DB
			existingAuth, err := qtx.GetAuthProfileByName(ctx, pool.AuthProfile)
			if err != nil {
				return fmt.Errorf("pool %q references unknown auth_profile %q", pool.Name, pool.AuthProfile)
			}
			authID = existingAuth.ID
		}

		scope := pool.Scope
		if scope == "" {
			scope = "repo"
		}

		labelsJSON, err := json.Marshal(pool.Labels)
		if err != nil {
			return fmt.Errorf("pool %q: serializing labels: %w", pool.Name, err)
		}

		minIdle := pool.MinIdleRunners
		if minIdle <= 0 {
			minIdle = 1
		}
		maxConc := pool.MaxConcurrency
		if maxConc <= 0 {
			maxConc = 5
		}
		lifetime := pool.MaxRunnerLifetimeSeconds
		if lifetime <= 0 {
			lifetime = 7200
		}

		var poolID int64
		existingPool, err := qtx.GetRunnerPoolByName(ctx, pool.Name)
		if err == nil {
			// Update pool
			updated, err := qtx.UpdateRunnerPool(ctx, UpdateRunnerPoolParams{
				Name:                     pool.Name,
				Provider:                 pool.Provider,
				RepositoryUrl:            pool.RepositoryURL,
				Scope:                    scope,
				AuthProfileID:            authID,
				MinIdleRunners:           minIdle,
				MaxConcurrency:           maxConc,
				Labels:                   string(labelsJSON),
				RunnerImage:              pool.RunnerImage,
				AllowDocker:              pool.AllowDocker,
				MaxRunnerLifetimeSeconds: lifetime,
				CpuLimit:                 sql.NullString{String: pool.CPULimit, Valid: pool.CPULimit != ""},
				MemoryLimit:              sql.NullString{String: pool.MemoryLimit, Valid: pool.MemoryLimit != ""},
				ID:                       existingPool.ID,
			})
			if err != nil {
				return fmt.Errorf("updating pool %q: %w", pool.Name, err)
			}
			poolID = updated.ID
		} else if errors.Is(err, sql.ErrNoRows) {
			created, err := qtx.CreateRunnerPool(ctx, CreateRunnerPoolParams{
				Name:                     pool.Name,
				Provider:                 pool.Provider,
				RepositoryUrl:            pool.RepositoryURL,
				Scope:                    scope,
				AuthProfileID:            authID,
				MinIdleRunners:           minIdle,
				MaxConcurrency:           maxConc,
				Labels:                   string(labelsJSON),
				RunnerImage:              pool.RunnerImage,
				AllowDocker:              pool.AllowDocker,
				MaxRunnerLifetimeSeconds: lifetime,
				CpuLimit:                 sql.NullString{String: pool.CPULimit, Valid: pool.CPULimit != ""},
				MemoryLimit:              sql.NullString{String: pool.MemoryLimit, Valid: pool.MemoryLimit != ""},
			})
			if err != nil {
				return fmt.Errorf("creating pool %q: %w", pool.Name, err)
			}
			poolID = created.ID
		} else {
			return fmt.Errorf("checking pool %q: %w", pool.Name, err)
		}

		// Ensure primary repository_url is in pool_targets
		if pool.RepositoryURL != "" {
			targets, _ := qtx.ListPoolTargetsByPoolId(ctx, poolID)
			found := false
			for _, t := range targets {
				if t.TargetUrl == pool.RepositoryURL {
					found = true
					break
				}
			}
			if !found {
				if _, err := qtx.AddPoolTarget(ctx, AddPoolTargetParams{
					PoolID:    poolID,
					TargetUrl: pool.RepositoryURL,
				}); err != nil {
					return fmt.Errorf("adding pool target for pool %q: %w", pool.Name, err)
				}
			}
		}

		// Handle Renovate Config for pool
		if pool.Renovate != nil {
			image := pool.Renovate.Image
			if image == "" {
				image = "renovate/renovate:latest"
			}
			existingRenovate, err := qtx.GetRenovateConfigByPoolId(ctx, poolID)
			if err == nil {
				_, err = qtx.UpdateRenovateConfig(ctx, UpdateRenovateConfigParams{
					Enabled:      pool.Renovate.Enabled,
					CronSchedule: sql.NullString{String: pool.Renovate.CronSchedule, Valid: pool.Renovate.CronSchedule != ""},
					Image:        image,
					PoolID:       existingRenovate.PoolID,
				})
				if err != nil {
					return fmt.Errorf("updating renovate config for pool %q: %w", pool.Name, err)
				}
			} else if errors.Is(err, sql.ErrNoRows) {
				_, err = qtx.CreateRenovateConfig(ctx, CreateRenovateConfigParams{
					PoolID:       poolID,
					Enabled:      pool.Renovate.Enabled,
					CronSchedule: sql.NullString{String: pool.Renovate.CronSchedule, Valid: pool.Renovate.CronSchedule != ""},
					Image:        image,
				})
				if err != nil {
					return fmt.Errorf("creating renovate config for pool %q: %w", pool.Name, err)
				}
			} else {
				return fmt.Errorf("checking renovate config for pool %q: %w", pool.Name, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing import transaction: %w", err)
	}

	return d.MarkSeedImported(ctx)
}

// ExportSanitizedConfig exports current database state into a sanitized SeedConfig,
// with all raw secrets replaced by placeholders (SanitizedRedactedPlaceholder / reference keys).
// Zero plaintext credentials are ever exported.
func (d *DB) ExportSanitizedConfig(ctx context.Context) (*SeedConfig, error) {
	cfg := &SeedConfig{
		Version:      "1.0",
		AuthProfiles: make(map[string]SeedAuthProfile),
		Pools:        make([]SeedPool, 0),
	}

	// 1. Export Global settings
	settings, err := d.ListAppSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing app settings: %w", err)
	}
	for _, s := range settings {
		val, _ := strconv.Atoi(s.Value)
		switch s.Key {
		case "total_allowed_runners":
			cfg.Global.TotalAllowedRunners = val
		case "total_idle_warm_pool":
			cfg.Global.TotalIdleWarmPool = val
		case "shutdown_timeout_seconds":
			cfg.Global.ShutdownTimeoutSeconds = val
		case "job_retention_days":
			cfg.Global.JobRetentionDays = val
		}
	}

	// 2. Export Auth Profiles (sanitized)
	profiles, err := d.ListAuthProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing auth profiles: %w", err)
	}

	authProfileNameByID := make(map[int64]string)
	for _, p := range profiles {
		authProfileNameByID[p.ID] = p.Name

		sanitized := SeedAuthProfile{
			AuthMethod: p.AuthMethod,
		}
		if p.AppID.Valid {
			sanitized.AppID = p.AppID.Int64
		}

		// Sanitize credentials: placeholders only, ZERO plaintext secrets
		if p.PrivateKeyEncrypted.Valid && p.PrivateKeyEncrypted.String != "" {
			sanitized.PrivateKeyPath = SanitizedRedactedPlaceholder
		}
		if p.TokenEncrypted.Valid && p.TokenEncrypted.String != "" {
			switch p.AuthMethod {
			case "gitea_token":
				sanitized.GiteaTokenEnvVar = fmt.Sprintf("SUPERVISOR_%s_TOKEN", strings.ToUpper(p.Name))
			case "forgejo_token":
				sanitized.ForgejoTokenEnvVar = fmt.Sprintf("SUPERVISOR_%s_TOKEN", strings.ToUpper(p.Name))
			default:
				sanitized.TokenEnvVar = fmt.Sprintf("SUPERVISOR_%s_TOKEN", strings.ToUpper(p.Name))
			}
		}

		cfg.AuthProfiles[p.Name] = sanitized
	}

	// 3. Export Pools
	pools, err := d.ListRunnerPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing runner pools: %w", err)
	}

	for _, p := range pools {
		var labels []string
		if err := json.Unmarshal([]byte(p.Labels), &labels); err != nil {
			labels = []string{p.Labels}
		}

		pool := SeedPool{
			Name:                     p.Name,
			Provider:                 p.Provider,
			RepositoryURL:            p.RepositoryUrl,
			Scope:                    p.Scope,
			AuthProfile:              authProfileNameByID[p.AuthProfileID],
			MinIdleRunners:           p.MinIdleRunners,
			MaxConcurrency:           p.MaxConcurrency,
			Labels:                   labels,
			RunnerImage:              p.RunnerImage,
			AllowDocker:              p.AllowDocker,
			MaxRunnerLifetimeSeconds: p.MaxRunnerLifetimeSeconds,
			CPULimit:                 p.CpuLimit.String,
			MemoryLimit:              p.MemoryLimit.String,
		}

		renovate, err := d.GetRenovateConfigByPoolId(ctx, p.ID)
		if err == nil {
			pool.Renovate = &SeedRenovate{
				Enabled:      renovate.Enabled,
				CronSchedule: renovate.CronSchedule.String,
				Image:        renovate.Image,
			}
		}

		cfg.Pools = append(cfg.Pools, pool)
	}

	return cfg, nil
}

// resolveCredentials extracts plaintext credentials from either inline values,
// file paths, or environment variable references specified in the seed profile.
func resolveCredentials(prof SeedAuthProfile) (privateKey, token string, err error) {
	// Private key resolution
	if prof.PrivateKey != "" {
		privateKey = prof.PrivateKey
	} else if prof.PrivateKeyPath != "" && prof.PrivateKeyPath != SanitizedRedactedPlaceholder {
		content, err := os.ReadFile(filepath.Clean(prof.PrivateKeyPath))
		if err != nil {
			return "", "", fmt.Errorf("reading private_key_path %q: %w", prof.PrivateKeyPath, err)
		}
		privateKey = string(content)
	}

	// Token resolution
	if prof.Token != "" {
		token = prof.Token
	} else if prof.GiteaTokenEnvVar != "" {
		token = os.Getenv(prof.GiteaTokenEnvVar)
	} else if prof.ForgejoTokenEnvVar != "" {
		token = os.Getenv(prof.ForgejoTokenEnvVar)
	} else if prof.TokenEnvVar != "" {
		token = os.Getenv(prof.TokenEnvVar)
	}

	return privateKey, token, nil
}
