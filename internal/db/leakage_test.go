package db

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"path/filepath"
	"strings"
	"testing"
)

// generateTestRSAPEM creates a valid 2048-bit RSA private key in PKCS#1 PEM format.
func generateTestRSAPEM(t *testing.T) string {
	t.Helper()
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}
	privDER := x509.MarshalPKCS1PrivateKey(privKey)
	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privDER,
	}); err != nil {
		t.Fatalf("failed to encode RSA PEM: %v", err)
	}
	return buf.String()
}

func TestExportSanitization_NegativeLeakageScan(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "leakage.db")

	encKey := bytes.Repeat([]byte{0x42}, 32)
	database, err := Open(Options{
		Path:          dbPath,
		EncryptionKey: encKey,
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = database.Close() }()

	// 1. Seed sensitive credentials
	rawRSAPEM := generateTestRSAPEM(t)
	// Extract a unique 32-char chunk from the PEM body to test deep substring leakage
	lines := strings.Split(strings.TrimSpace(rawRSAPEM), "\n")
	if len(lines) < 4 {
		t.Fatalf("unexpected PEM structure: %s", rawRSAPEM)
	}
	pemBodyChunk := lines[2]

	classicPAT := "ghp_TopSecretClassicPAT999999999999ABCDEF"
	fineGrainedPAT := "github_pat_11SECRET_FINE_GRAINED_TOKEN_abcdef1234567890XYZ"
	giteaSecretToken := "gitea_secret_token_99887766554433221100aabbccddeeff"
	forgejoSecretToken := "forgejo_secret_token_11223344556677889900ffeeddccbbaa"
	adminPasswordHash := "$2a$12$e8Y9c0K7zP4Q4O/J5B.p..someHashedPasswordStringGoesHere"
	sessionTokenHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	// Insert Admin User & Session
	adminUser, err := database.CreateAdminUser(ctx, CreateAdminUserParams{
		Username:     "superadmin",
		PasswordHash: adminPasswordHash,
	})
	if err != nil {
		t.Fatalf("CreateAdminUser failed: %v", err)
	}

	_, err = database.CreateSession(ctx, CreateSessionParams{
		UserID:    adminUser.ID,
		TokenHash: sessionTokenHash,
	})
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Encrypt credentials
	encRSA, err := database.encryptor.EncryptNullString(sql.NullString{String: rawRSAPEM, Valid: true})
	if err != nil {
		t.Fatalf("Encrypt RSA failed: %v", err)
	}
	encClassicPAT, err := database.encryptor.EncryptNullString(sql.NullString{String: classicPAT, Valid: true})
	if err != nil {
		t.Fatalf("Encrypt classic PAT failed: %v", err)
	}
	encFinePAT, err := database.encryptor.EncryptNullString(sql.NullString{String: fineGrainedPAT, Valid: true})
	if err != nil {
		t.Fatalf("Encrypt fine PAT failed: %v", err)
	}
	encGitea, err := database.encryptor.EncryptNullString(sql.NullString{String: giteaSecretToken, Valid: true})
	if err != nil {
		t.Fatalf("Encrypt Gitea token failed: %v", err)
	}
	encForgejo, err := database.encryptor.EncryptNullString(sql.NullString{String: forgejoSecretToken, Valid: true})
	if err != nil {
		t.Fatalf("Encrypt Forgejo token failed: %v", err)
	}

	// Create Auth Profiles
	profApp, err := database.CreateAuthProfile(ctx, CreateAuthProfileParams{
		Name:                "github-app-prod",
		AuthMethod:          "github_app",
		AppID:               sql.NullInt64{Int64: 98765, Valid: true},
		PrivateKeyEncrypted: encRSA,
		TokenEncrypted:      sql.NullString{},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile github-app-prod failed: %v", err)
	}

	profClassicPAT, err := database.CreateAuthProfile(ctx, CreateAuthProfileParams{
		Name:                "github-pat-classic",
		AuthMethod:          "pat",
		AppID:               sql.NullInt64{},
		PrivateKeyEncrypted: sql.NullString{},
		TokenEncrypted:      encClassicPAT,
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile github-pat-classic failed: %v", err)
	}

	profFinePAT, err := database.CreateAuthProfile(ctx, CreateAuthProfileParams{
		Name:                "github-pat-fine",
		AuthMethod:          "pat",
		AppID:               sql.NullInt64{},
		PrivateKeyEncrypted: sql.NullString{},
		TokenEncrypted:      encFinePAT,
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile github-pat-fine failed: %v", err)
	}

	profGitea, err := database.CreateAuthProfile(ctx, CreateAuthProfileParams{
		Name:                "gitea-profile",
		AuthMethod:          "gitea_token",
		AppID:               sql.NullInt64{},
		PrivateKeyEncrypted: sql.NullString{},
		TokenEncrypted:      encGitea,
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile gitea-profile failed: %v", err)
	}

	profForgejo, err := database.CreateAuthProfile(ctx, CreateAuthProfileParams{
		Name:                "forgejo-profile",
		AuthMethod:          "forgejo_token",
		AppID:               sql.NullInt64{},
		PrivateKeyEncrypted: sql.NullString{},
		TokenEncrypted:      encForgejo,
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile forgejo-profile failed: %v", err)
	}

	// Create Runner Pools
	_, err = database.CreateRunnerPool(ctx, CreateRunnerPoolParams{
		Name:                     "gh-app-pool",
		Provider:                 "github",
		RepositoryUrl:            "https://github.com/myorg/repo-app",
		Scope:                    "repo",
		AuthProfileID:            profApp.ID,
		MinIdleRunners:           2,
		MaxConcurrency:           10,
		Labels:                   `["self-hosted","linux"]`,
		RunnerImage:              "ghcr.io/noosxe/runnero:latest",
		AllowDocker:              false,
		MaxRunnerLifetimeSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool gh-app-pool failed: %v", err)
	}

	_, err = database.CreateRunnerPool(ctx, CreateRunnerPoolParams{
		Name:                     "gh-classic-pool",
		Provider:                 "github",
		RepositoryUrl:            "https://github.com/myorg/repo-pat",
		Scope:                    "repo",
		AuthProfileID:            profClassicPAT.ID,
		MinIdleRunners:           1,
		MaxConcurrency:           5,
		Labels:                   `["self-hosted","arm64"]`,
		RunnerImage:              "ghcr.io/noosxe/runnero:latest",
		AllowDocker:              false,
		MaxRunnerLifetimeSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool gh-classic-pool failed: %v", err)
	}

	_, err = database.CreateRunnerPool(ctx, CreateRunnerPoolParams{
		Name:                     "gh-fine-pool",
		Provider:                 "github",
		RepositoryUrl:            "https://github.com/myorg/repo-fine",
		Scope:                    "repo",
		AuthProfileID:            profFinePAT.ID,
		MinIdleRunners:           1,
		MaxConcurrency:           5,
		Labels:                   `["self-hosted","fine"]`,
		RunnerImage:              "ghcr.io/noosxe/runnero:latest",
		AllowDocker:              false,
		MaxRunnerLifetimeSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool gh-fine-pool failed: %v", err)
	}

	_, err = database.CreateRunnerPool(ctx, CreateRunnerPoolParams{
		Name:                     "gitea-pool",
		Provider:                 "gitea",
		RepositoryUrl:            "https://gitea.example.com/org/repo",
		Scope:                    "repo",
		AuthProfileID:            profGitea.ID,
		MinIdleRunners:           1,
		MaxConcurrency:           3,
		Labels:                   `["gitea","dood"]`,
		RunnerImage:              "ghcr.io/noosxe/runnero:latest",
		AllowDocker:              true,
		MaxRunnerLifetimeSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool gitea-pool failed: %v", err)
	}

	_, err = database.CreateRunnerPool(ctx, CreateRunnerPoolParams{
		Name:                     "forgejo-pool",
		Provider:                 "forgejo",
		RepositoryUrl:            "https://forgejo.example.com/org/repo",
		Scope:                    "repo",
		AuthProfileID:            profForgejo.ID,
		MinIdleRunners:           1,
		MaxConcurrency:           3,
		Labels:                   `["forgejo","dood"]`,
		RunnerImage:              "ghcr.io/noosxe/runnero:latest",
		AllowDocker:              true,
		MaxRunnerLifetimeSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool forgejo-pool failed: %v", err)
	}

	// 2. Export sanitized config
	exportedConfig, err := database.ExportSanitizedConfig(ctx)
	if err != nil {
		t.Fatalf("ExportSanitizedConfig failed: %v", err)
	}

	yamlBytes, err := exportedConfig.ToYAML()
	if err != nil {
		t.Fatalf("ToYAML failed: %v", err)
	}
	yamlStr := string(yamlBytes)

	// 3. Negative assertions: ZERO secrets must leak into the export string
	forbiddenSecrets := map[string]string{
		"RSA PEM Full":            rawRSAPEM,
		"RSA PEM Header":          "-----BEGIN RSA PRIVATE KEY-----",
		"RSA PEM Footer":          "-----END RSA PRIVATE KEY-----",
		"RSA PEM Body Chunk":      pemBodyChunk,
		"Classic PAT":             classicPAT,
		"Fine-Grained PAT":        fineGrainedPAT,
		"Gitea Token":             giteaSecretToken,
		"Forgejo Token":           forgejoSecretToken,
		"Admin Password Hash":     adminPasswordHash,
		"Session Token Hash":      sessionTokenHash,
		"Encrypted Ciphertext":    encRSA.String,
		"Raw Master Key (hex)":    string(encKey),
	}

	for label, secret := range forbiddenSecrets {
		if strings.Contains(yamlStr, secret) {
			t.Fatalf("CRITICAL SECURITY VIOLATION: Exported YAML contains raw secret [%s]: %q", label, secret)
		}
	}

	// 4. Positive assertions: All secrets replaced with placeholders or reference env keys
	expectedPlaceholders := []string{
		"private_key_path: " + SanitizedRedactedPlaceholder,
		"token_env_var: SUPERVISOR_GITHUB-PAT-CLASSIC_TOKEN",
		"token_env_var: SUPERVISOR_GITHUB-PAT-FINE_TOKEN",
		"gitea_token_env_var: SUPERVISOR_GITEA-PROFILE_TOKEN",
		"forgejo_token_env_var: SUPERVISOR_FORGEJO-PROFILE_TOKEN",
	}

	for _, ph := range expectedPlaceholders {
		if !strings.Contains(yamlStr, ph) {
			t.Errorf("expected placeholder %q missing from exported YAML:\n%s", ph, yamlStr)
		}
	}

	// 5. Test Roundtrip & Preservation in Merge Mode:
	// If the sanitized YAML is re-imported in "merge" mode, existing encrypted credentials
	// in the database MUST NOT be overwritten or corrupted by the redacted placeholder.
	reParsed, err := ParseSeedConfig(yamlBytes)
	if err != nil {
		t.Fatalf("ParseSeedConfig failed: %v", err)
	}

	if err := database.ImportSeedConfig(ctx, reParsed, ImportModeMerge); err != nil {
		t.Fatalf("ImportSeedConfig (merge) failed: %v", err)
	}

	// Verify all credentials in DB remain intact and decrypt correctly
	checkProfile := func(name, wantPrivKey, wantToken string) {
		p, err := database.GetAuthProfileByName(ctx, name)
		if err != nil {
			t.Fatalf("GetAuthProfileByName %q failed: %v", name, err)
		}
		decPriv, err := database.encryptor.DecryptNullString(p.PrivateKeyEncrypted)
		if err != nil {
			t.Fatalf("decrypt private key for %q failed: %v", name, err)
		}
		if decPriv.String != wantPrivKey {
			t.Errorf("profile %q private key corrupted after re-import: got %q, want %q", name, decPriv.String, wantPrivKey)
		}
		decTok, err := database.encryptor.DecryptNullString(p.TokenEncrypted)
		if err != nil {
			t.Fatalf("decrypt token for %q failed: %v", name, err)
		}
		if decTok.String != wantToken {
			t.Errorf("profile %q token corrupted after re-import: got %q, want %q", name, decTok.String, wantToken)
		}
	}

	checkProfile("github-app-prod", rawRSAPEM, "")
	checkProfile("github-pat-classic", "", classicPAT)
	checkProfile("github-pat-fine", "", fineGrainedPAT)
	checkProfile("gitea-profile", "", giteaSecretToken)
	checkProfile("forgejo-profile", "", forgejoSecretToken)
}
