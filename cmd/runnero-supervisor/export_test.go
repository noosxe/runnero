package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/keys"
)

func generateTestPEM(t *testing.T) string {
	t.Helper()
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}
	privDER := x509.MarshalPKCS1PrivateKey(privKey)
	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privDER,
	}); err != nil {
		t.Fatalf("encoding PEM: %v", err)
	}
	return buf.String()
}

func TestExportCLI_LeakageScanAndFilePermissions(t *testing.T) {
	validKeyEnv(t)
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "supervisor.db")
	t.Setenv("SUPERVISOR_DATA_DIR", dataDir)
	t.Setenv("SUPERVISOR_DB_PATH", dbPath)

	derived, err := keys.Derive("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("deriving keys: %v", err)
	}

	database, err := db.Open(db.Options{
		Path:          dbPath,
		EncryptionKey: derived.DBEncryptionKey,
	})
	if err != nil {
		t.Fatalf("opening DB: %v", err)
	}

	ctx := context.Background()
	rawPEM := generateTestPEM(t)
	rawPAT := "ghp_VerySecretSupervisorToken998877"
	rawGitea := "gitea_SecretSupervisorToken665544"

	// Create auth profiles directly via db queries
	_, err = database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:                "github-app",
		AuthMethod:          "github_app",
		AppID:               sql.NullInt64{Int64: 12345, Valid: true},
		PrivateKeyEncrypted: sql.NullString{String: "encrypted_key", Valid: true},
		TokenEncrypted:      sql.NullString{},
	})
	if err != nil {
		_ = database.Close()
		t.Fatalf("CreateAuthProfile github-app: %v", err)
	}

	p2, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:                "github-pat",
		AuthMethod:          "pat",
		AppID:               sql.NullInt64{},
		PrivateKeyEncrypted: sql.NullString{},
		TokenEncrypted:      sql.NullString{String: "encrypted_pat", Valid: true},
	})
	if err != nil {
		_ = database.Close()
		t.Fatalf("CreateAuthProfile github-pat: %v", err)
	}

	_, err = database.CreateRunnerPool(ctx, db.CreateRunnerPoolParams{
		Name:                     "cli-pool",
		Provider:                 "github",
		RepositoryUrl:            "https://github.com/myorg/myrepo",
		Scope:                    "repo",
		AuthProfileID:            p2.ID,
		MinIdleRunners:           1,
		MaxConcurrency:           5,
		Labels:                   `["linux","arm64"]`,
		RunnerImage:              "ghcr.io/noosxe/runnero:latest",
		AllowDocker:              false,
		MaxRunnerLifetimeSeconds: 7200,
	})
	_ = database.Close()
	if err != nil {
		t.Fatalf("CreateRunnerPool: %v", err)
	}

	// 1. Export to file via --output
	exportFile := filepath.Join(dataDir, "export-test.yml")
	rootExportFile := NewRootCommand()
	rootExportFile.SetArgs([]string{"export", "--output", exportFile})
	if err := rootExportFile.Execute(); err != nil {
		t.Fatalf("supervisor export --output failed: %v", err)
	}

	fi, err := os.Stat(exportFile)
	if err != nil {
		t.Fatalf("stat export file: %v", err)
	}
	// Verify strict file permissions (0600)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("export file permissions want 0600, got %o", fi.Mode().Perm())
	}

	fileBytes, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("reading export file: %v", err)
	}
	fileStr := string(fileBytes)

	// 2. Export to stdout
	var stdoutBuf bytes.Buffer
	rootExportStdout := NewRootCommand()
	rootExportStdout.SetOut(&stdoutBuf)
	rootExportStdout.SetArgs([]string{"export"})
	if err := rootExportStdout.Execute(); err != nil {
		t.Fatalf("supervisor export stdout failed: %v", err)
	}
	stdoutStr := stdoutBuf.String()

	// 3. Negative assertions on both outputs
	for targetName, content := range map[string]string{"file": fileStr, "stdout": stdoutStr} {
		for secretName, secretVal := range map[string]string{
			"raw PEM":              rawPEM,
			"PEM Header":           "-----BEGIN RSA PRIVATE KEY-----",
			"PEM Footer":           "-----END RSA PRIVATE KEY-----",
			"raw PAT":              rawPAT,
			"raw Gitea Token":      rawGitea,
			"DB Encryption Key":    "0123456789abcdef0123456789abcdef",
			"Ciphertext (privkey)": "encrypted_key",
			"Ciphertext (token)":   "encrypted_pat",
		} {
			if strings.Contains(content, secretVal) {
				t.Errorf("CRITICAL SECURITY LEAKAGE in %s: found %s: %q", targetName, secretName, secretVal)
			}
		}

		// Positive checks
		if !strings.Contains(content, "private_key_path: "+db.SanitizedRedactedPlaceholder) {
			t.Errorf("expected placeholder %s in %s", db.SanitizedRedactedPlaceholder, targetName)
		}
		if !strings.Contains(content, "token_env_var: SUPERVISOR_GITHUB-PAT_TOKEN") {
			t.Errorf("expected reference key SUPERVISOR_GITHUB-PAT_TOKEN in %s", targetName)
		}
	}
}
