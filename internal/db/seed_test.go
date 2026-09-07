package db

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleYAML = `version: "1.0"

global:
  check_interval_seconds: 10
  default_labels: ["self-hosted", "linux", "dynamic"]
  total_allowed_runners: 25
  total_idle_warm_pool: 7
  shutdown_timeout_seconds: 400
  job_retention_days: 45

auth_profiles:
  my_github_app:
    auth_method: github_app
    app_id: 123456
    private_key: "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0...\n-----END RSA PRIVATE KEY-----"
  my_gitea_token:
    auth_method: gitea_token
    token_env_var: "TEST_GITEA_TOKEN"

pools:
  - name: "frontend-repo-runners"
    provider: github
    repository_url: "https://github.com/my-org/frontend-project"
    scope: repo
    auth_profile: "my_github_app"
    min_idle_runners: 2
    max_concurrency: 5
    labels: ["frontend", "node-20"]
    runner_image: "ghcr.io/noosxe/runnero:latest"
    allow_docker: false
    max_runner_lifetime_seconds: 7200
    cpu_limit: "2.0"
    memory_limit: "4g"
    renovate:
      enabled: true
      cron_schedule: "0 2 * * *"
      image: "renovate/renovate:latest"

  - name: "gitea-project-runners"
    provider: gitea
    repository_url: "https://gitea.example.com/my-org/gitea-project"
    scope: repo
    auth_profile: "my_gitea_token"
    min_idle_runners: 1
    max_concurrency: 3
    labels: ["backend"]
    runner_image: "ghcr.io/noosxe/runnero:latest"
    allow_docker: true
    max_runner_lifetime_seconds: 7200
    cpu_limit: "4.0"
    memory_limit: "8g"
`

// TestSeedImportAndSanitizedExport verifies RUN-16 acceptance:
// - import creates profiles, pools, renovate configs, and global settings
// - export contains ZERO plaintext secrets
// - credentials are encrypted at rest
func TestSeedImportAndSanitizedExport(t *testing.T) {
	t.Setenv("TEST_GITEA_TOKEN", "super_secret_gitea_pat_9999")

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "seed.db")
	aesKey := bytes.Repeat([]byte{0x55}, 32)

	database, err := Open(Options{
		Path:          dbPath,
		EncryptionKey: aesKey,
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()

	// Initial state: empty DB needs seed import
	should, err := database.ShouldAutoImportSeed(ctx)
	if err != nil || !should {
		t.Fatalf("ShouldAutoImportSeed on empty DB = %v (err: %v), want true", should, err)
	}

	seedCfg, err := ParseSeedConfig([]byte(sampleYAML))
	if err != nil {
		t.Fatalf("ParseSeedConfig failed: %v", err)
	}

	// 1. Import
	if err := database.ImportSeedConfig(ctx, seedCfg, ImportModeOverwrite); err != nil {
		t.Fatalf("ImportSeedConfig failed: %v", err)
	}

	// Verify ShouldAutoImportSeed is now false
	shouldAfter, err := database.ShouldAutoImportSeed(ctx)
	if err != nil || shouldAfter {
		t.Fatalf("ShouldAutoImportSeed after import = %v, want false", shouldAfter)
	}

	// Verify profiles created
	profiles, err := database.ListAuthProfiles(ctx)
	if err != nil || len(profiles) != 2 {
		t.Fatalf("ListAuthProfiles count = %d, want 2", len(profiles))
	}

	// Verify pools created
	pools, err := database.ListRunnerPools(ctx)
	if err != nil || len(pools) != 2 {
		t.Fatalf("ListRunnerPools count = %d, want 2", len(pools))
	}

	// Verify global settings
	setting, err := database.GetAppSetting(ctx, "total_allowed_runners")
	if err != nil || setting.Value != "25" {
		t.Errorf("total_allowed_runners = %q, want '25'", setting.Value)
	}

	// 2. Export Sanitized Config
	exported, err := database.ExportSanitizedConfig(ctx)
	if err != nil {
		t.Fatalf("ExportSanitizedConfig failed: %v", err)
	}

	exportedYAML, err := exported.ToYAML()
	if err != nil {
		t.Fatalf("ToYAML failed: %v", err)
	}

	yamlStr := string(exportedYAML)

	// ZERO plaintext secrets in export
	if strings.Contains(yamlStr, "super_secret_gitea_pat_9999") {
		t.Fatal("SECURITY VIOLATION: Exported YAML contains plaintext token!")
	}
	if strings.Contains(yamlStr, "MIIEowIBAAKCAQEA0") {
		t.Fatal("SECURITY VIOLATION: Exported YAML contains plaintext private key!")
	}
	if !strings.Contains(yamlStr, SanitizedRedactedPlaceholder) {
		t.Errorf("Exported YAML should contain placeholder %s", SanitizedRedactedPlaceholder)
	}

	// Verify structure of exported YAML
	if !strings.Contains(yamlStr, "frontend-repo-runners") || !strings.Contains(yamlStr, "gitea-project-runners") {
		t.Error("Exported YAML missing pool names")
	}
}

// TestSeedImportValidationErrors verifies strict reference and schema validation.
func TestSeedImportValidationErrors(t *testing.T) {
	// Unknown auth_profile reference in pool
	invalidPoolRefYAML := `version: "1.0"
auth_profiles:
  prof1:
    auth_method: pat
    token: "tok"
pools:
  - name: "pool1"
    provider: github
    repository_url: "https://github.com/o/r"
    auth_profile: "non_existent_profile"
    labels: ["linux"]
    runner_image: "img"
`
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "val.db")
	aesKey := bytes.Repeat([]byte{0x77}, 32)
	database, _ := Open(Options{Path: dbPath, EncryptionKey: aesKey})
	defer func() { _ = database.Close() }()

	cfg, err := ParseSeedConfig([]byte(invalidPoolRefYAML))
	if err != nil {
		t.Fatalf("ParseSeedConfig: %v", err)
	}

	err = database.ImportSeedConfig(context.Background(), cfg, ImportModeMerge)
	if err == nil || !strings.Contains(err.Error(), "unknown auth_profile") {
		t.Fatalf("expected unknown auth_profile error, got: %v", err)
	}

	// Invalid provider
	badProviderYAML := `version: "1.0"
pools:
  - name: "p1"
    provider: "gitlab"
    repository_url: "https://gl.com/o/r"
    auth_profile: "p"
    labels: ["l"]
    runner_image: "img"
`
	_, err = ParseSeedConfig([]byte(badProviderYAML))
	if err == nil || !strings.Contains(err.Error(), "invalid provider") {
		t.Fatalf("expected invalid provider error, got: %v", err)
	}

	// Duplicate pool name
	dupPoolYAML := `version: "1.0"
pools:
  - name: "dup"
    provider: github
    repository_url: "https://github.com/o/r1"
    auth_profile: "p"
    labels: ["l"]
    runner_image: "img"
  - name: "dup"
    provider: github
    repository_url: "https://github.com/o/r2"
    auth_profile: "p"
    labels: ["l"]
    runner_image: "img"
`
	_, err = ParseSeedConfig([]byte(dupPoolYAML))
	if err == nil || !strings.Contains(err.Error(), "duplicate pool name") {
		t.Fatalf("expected duplicate pool name error, got: %v", err)
	}
}

// TestSeedImportModes verifies Merge vs Overwrite behaviors.
func TestSeedImportModes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "modes.db")
	aesKey := bytes.Repeat([]byte{0x88}, 32)
	database, _ := Open(Options{Path: dbPath, EncryptionKey: aesKey})
	defer func() { _ = database.Close() }()

	ctx := context.Background()

	cfg1YAML := `version: "1.0"
auth_profiles:
  p1:
    auth_method: pat
    token: "tok1"
pools:
  - name: "pool1"
    provider: github
    repository_url: "https://github.com/o/r1"
    auth_profile: "p1"
    labels: ["linux"]
    runner_image: "img1"
`
	cfg1, _ := ParseSeedConfig([]byte(cfg1YAML))
	if err := database.ImportSeedConfig(ctx, cfg1, ImportModeMerge); err != nil {
		t.Fatalf("Import 1 failed: %v", err)
	}

	// Merge with pool2: should now have pool1 and pool2
	cfg2YAML := `version: "1.0"
auth_profiles:
  p2:
    auth_method: pat
    token: "tok2"
pools:
  - name: "pool2"
    provider: github
    repository_url: "https://github.com/o/r2"
    auth_profile: "p2"
    labels: ["arm64"]
    runner_image: "img2"
`
	cfg2, _ := ParseSeedConfig([]byte(cfg2YAML))
	if err := database.ImportSeedConfig(ctx, cfg2, ImportModeMerge); err != nil {
		t.Fatalf("Import 2 (merge) failed: %v", err)
	}

	pools, _ := database.ListRunnerPools(ctx)
	if len(pools) != 2 {
		t.Fatalf("after merge, pool count = %d, want 2", len(pools))
	}

	// Overwrite with pool3 only: pool1 and pool2 should be removed
	cfg3YAML := `version: "1.0"
auth_profiles:
  p3:
    auth_method: pat
    token: "tok3"
pools:
  - name: "pool3"
    provider: github
    repository_url: "https://github.com/o/r3"
    auth_profile: "p3"
    labels: ["x86"]
    runner_image: "img3"
`
	cfg3, _ := ParseSeedConfig([]byte(cfg3YAML))
	if err := database.ImportSeedConfig(ctx, cfg3, ImportModeOverwrite); err != nil {
		t.Fatalf("Import 3 (overwrite) failed: %v", err)
	}

	poolsAfter, _ := database.ListRunnerPools(ctx)
	if len(poolsAfter) != 1 || poolsAfter[0].Name != "pool3" {
		t.Fatalf("after overwrite, expected only pool3, got: %+v", poolsAfter)
	}
}

// TestPrivateKeyFileResolution verifies reading private keys from file paths during import.
func TestPrivateKeyFileResolution(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.pem")
	keyContent := "-----BEGIN RSA PRIVATE KEY-----\nMIIE...\n-----END RSA PRIVATE KEY-----"
	if err := os.WriteFile(keyFile, []byte(keyContent), 0o600); err != nil {
		t.Fatalf("writing key file: %v", err)
	}

	prof := SeedAuthProfile{
		AuthMethod:     "github_app",
		AppID:          1234,
		PrivateKeyPath: keyFile,
	}

	privKey, _, err := resolveCredentials(prof)
	if err != nil {
		t.Fatalf("resolveCredentials failed: %v", err)
	}
	if privKey != keyContent {
		t.Fatalf("resolved key mismatch: got %q, want %q", privKey, keyContent)
	}
}
