package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// testKey is a valid (>= MinEncryptionKeyBytes) placeholder so most tests
// can focus on the behavior under test rather than the required key.
const testKey = "0123456789abcdef0123456789abcdef"

// testFlags mirrors the persistent flags registered by cmd/supervisor so
// the flag layer is exercised with production flag names and defaults.
func testFlags(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()
	fs := pflag.NewFlagSet("supervisor", pflag.ContinueOnError)
	var configPath, logLevel, dataDir, dbPath, dockerHost string
	var port int
	var secureCookie bool
	fs.StringVarP(&configPath, "config", "c", "", "path to the configuration file (YAML or TOML)")
	fs.StringVar(&logLevel, "log-level", DefaultLogLevel, "log level")
	fs.StringVar(&dataDir, "data-dir", DefaultDataDir, "data directory")
	fs.StringVar(&dbPath, "db-path", "", "path to the SQLite database file")
	fs.IntVar(&port, "port", DefaultPort, "HTTP port")
	fs.StringVar(&dockerHost, "docker-host", "", "Docker daemon endpoint")
	fs.BoolVar(&secureCookie, "secure-cookie", false, "set the Secure attribute on the session cookie")
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parsing test flags %v: %v", args, err)
	}
	return fs
}

// writeFile creates a config file inside the test's temp directory.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func wantErrContaining(t *testing.T, err error, want ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error containing %q, got nil", strings.Join(want, " "))
	}
	for _, sub := range want {
		if !strings.Contains(err.Error(), sub) {
			t.Fatalf("error %q does not contain %q", err.Error(), sub)
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)
	cfg, err := Load(Options{})
	if err != nil {
		t.Fatalf("loading defaults: %v", err)
	}
	if cfg.Port != DefaultPort {
		t.Errorf("port = %d, want %d", cfg.Port, DefaultPort)
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Errorf("log level = %q, want %q", cfg.LogLevel, DefaultLogLevel)
	}
	if cfg.DataDir != DefaultDataDir {
		t.Errorf("data dir = %q, want %q", cfg.DataDir, DefaultDataDir)
	}
	if want := filepath.Join(DefaultDataDir, DefaultDBFileName); cfg.DBPath != want {
		t.Errorf("db path = %q, want derived %q", cfg.DBPath, want)
	}
	if cfg.DockerHost != DefaultDockerHost {
		t.Errorf("docker host = %q, want %q", cfg.DockerHost, DefaultDockerHost)
	}
	if cfg.BackupIntervalHours != DefaultBackupIntervalHours {
		t.Errorf("backup interval = %d, want %d", cfg.BackupIntervalHours, DefaultBackupIntervalHours)
	}
	if cfg.BackupRetentionCount != DefaultBackupRetentionCount {
		t.Errorf("backup retention = %d, want %d", cfg.BackupRetentionCount, DefaultBackupRetentionCount)
	}
	if cfg.DBEncryptionKey != testKey {
		t.Errorf("db encryption key not carried through from the environment")
	}
	if cfg.SecureCookie {
		t.Errorf("secure cookie = true, want default false")
	}
}

func TestLoadYAMLFileOverridesDefaults(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)
	path := writeFile(t, "supervisor.yaml", `
port: 7070
log-level: debug
backup-interval-hours: 12
backup-retention-count: 3
`)
	cfg, err := Load(Options{Flags: testFlags(t, "--config", path)})
	if err != nil {
		t.Fatalf("loading yaml file: %v", err)
	}
	if cfg.Port != 7070 || cfg.LogLevel != "debug" ||
		cfg.BackupIntervalHours != 12 || cfg.BackupRetentionCount != 3 {
		t.Errorf("yaml file values not applied: %+v", cfg)
	}
	if cfg.ConfigFile != path {
		t.Errorf("config file = %q, want %q", cfg.ConfigFile, path)
	}
}

func TestLoadTOMLFileOverridesDefaults(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)
	path := writeFile(t, "supervisor.toml", `
port = 7071
log-level = "warn"
data-dir = "/srv/supervisor"
`)
	cfg, err := Load(Options{Flags: testFlags(t, "--config", path)})
	if err != nil {
		t.Fatalf("loading toml file: %v", err)
	}
	if cfg.Port != 7071 || cfg.LogLevel != "warn" || cfg.DataDir != "/srv/supervisor" {
		t.Errorf("toml file values not applied: %+v", cfg)
	}
	if cfg.ConfigFile != path {
		t.Errorf("config file = %q, want %q", cfg.ConfigFile, path)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)
	path := writeFile(t, "supervisor.yaml", "port: 7070\nlog-level: debug\n")
	t.Setenv(EnvPort, "6060")
	cfg, err := Load(Options{Flags: testFlags(t, "--config", path)})
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if cfg.Port != 6060 {
		t.Errorf("port = %d, want env value 6060 to beat file value 7070", cfg.Port)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("log level = %q, want file value debug (env did not set it)", cfg.LogLevel)
	}
}

func TestFlagOverridesEnv(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)
	t.Setenv(EnvPort, "6060")
	t.Setenv(EnvDataDir, "/env/data")
	cfg, err := Load(Options{Flags: testFlags(t, "--port", "5050")})
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if cfg.Port != 5050 {
		t.Errorf("port = %d, want flag value 5050 to beat env value 6060", cfg.Port)
	}
	if cfg.DataDir != "/env/data" {
		t.Errorf("data dir = %q, want env value /env/data (flag not passed)", cfg.DataDir)
	}
}

func TestUnchangedFlagDefaultsDoNotOverrideEnv(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)
	t.Setenv(EnvPort, "6060")
	// The flag set carries the default port 8080, but the user never
	// passed --port: the environment must win.
	cfg, err := Load(Options{Flags: testFlags(t)})
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if cfg.Port != 6060 {
		t.Errorf("port = %d, want 6060: untouched flag default shadowed the environment", cfg.Port)
	}
}

func TestLoadSecureCookie(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)

	t.Run("from environment", func(t *testing.T) {
		t.Setenv(EnvSecureCookie, "true")
		cfg, err := Load(Options{})
		if err != nil {
			t.Fatalf("loading: %v", err)
		}
		if !cfg.SecureCookie {
			t.Errorf("secure-cookie = false, want true from env")
		}
	})

	t.Run("from yaml file", func(t *testing.T) {
		path := writeFile(t, "secure.yaml", "secure-cookie: true\n")
		cfg, err := Load(Options{Flags: testFlags(t, "--config", path)})
		if err != nil {
			t.Fatalf("loading: %v", err)
		}
		if !cfg.SecureCookie {
			t.Errorf("secure-cookie = false, want true from yaml")
		}
	})

	t.Run("from cli flag overriding false env", func(t *testing.T) {
		t.Setenv(EnvSecureCookie, "false")
		cfg, err := Load(Options{Flags: testFlags(t, "--secure-cookie")})
		if err != nil {
			t.Fatalf("loading: %v", err)
		}
		if !cfg.SecureCookie {
			t.Errorf("secure-cookie = false, want true from flag")
		}
	})
}

func TestLoadEnrichJobConclusions(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)

	t.Run("defaults to enabled", func(t *testing.T) {
		cfg, err := Load(Options{})
		if err != nil {
			t.Fatalf("loading: %v", err)
		}
		if !cfg.EnrichJobConclusions {
			t.Errorf("enrich-job-conclusions = false, want default true (docs/21 section 5.3)")
		}
	})

	t.Run("disable from environment", func(t *testing.T) {
		t.Setenv(EnvEnrichJobConclusions, "false")
		cfg, err := Load(Options{})
		if err != nil {
			t.Fatalf("loading: %v", err)
		}
		if cfg.EnrichJobConclusions {
			t.Errorf("enrich-job-conclusions = true, want false from env")
		}
	})
}

func TestLoadWebhookSecrets(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)

	t.Run("unset by default", func(t *testing.T) {
		cfg, err := Load(Options{})
		if err != nil {
			t.Fatalf("loading: %v", err)
		}
		if cfg.WebhookGitHubSecret != "" || cfg.WebhookGiteaSecret != "" || cfg.WebhookForgejoSecret != "" {
			t.Errorf("webhook secrets = %q/%q/%q, want all empty by default", cfg.WebhookGitHubSecret, cfg.WebhookGiteaSecret, cfg.WebhookForgejoSecret)
		}
	})

	t.Run("from environment", func(t *testing.T) {
		t.Setenv(EnvWebhookGitHubSecret, " github-secret ")
		t.Setenv(EnvWebhookGiteaSecret, "gitea-secret")
		cfg, err := Load(Options{})
		if err != nil {
			t.Fatalf("loading: %v", err)
		}
		if cfg.WebhookGitHubSecret != "github-secret" {
			t.Errorf("webhook-github-secret = %q, want trimmed value from env", cfg.WebhookGitHubSecret)
		}
		if cfg.WebhookGiteaSecret != "gitea-secret" {
			t.Errorf("webhook-gitea-secret = %q, want value from env", cfg.WebhookGiteaSecret)
		}
	})

	t.Run("from yaml file", func(t *testing.T) {
		path := writeFile(t, "webhooks.yaml", "webhook-github-secret: yaml-secret\n")
		cfg, err := Load(Options{Flags: testFlags(t, "--config", path)})
		if err != nil {
			t.Fatalf("loading: %v", err)
		}
		if cfg.WebhookGitHubSecret != "yaml-secret" {
			t.Errorf("webhook-github-secret = %q, want value from yaml", cfg.WebhookGitHubSecret)
		}
	})
}

func TestConfigFileResolution(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)

	envPath := writeFile(t, "env-picked.yaml", "port: 4001\n")
	t.Setenv(EnvConfigFile, envPath)
	cfg, err := Load(Options{})
	if err != nil {
		t.Fatalf("loading file from env: %v", err)
	}
	if cfg.Port != 4001 {
		t.Errorf("port = %d, want file value 4001 picked via %s", cfg.Port, EnvConfigFile)
	}

	flagPath := writeFile(t, "flag-picked.yaml", "port: 4002\n")
	cfg, err = Load(Options{Flags: testFlags(t, "--config", flagPath)})
	if err != nil {
		t.Fatalf("loading with both env and flag config paths: %v", err)
	}
	if cfg.Port != 4002 {
		t.Errorf("port = %d, want 4002: --config must beat %s", cfg.Port, EnvConfigFile)
	}
}

func TestDBPathDerivation(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)
	t.Setenv(EnvDataDir, "/mnt/pool")
	cfg, err := Load(Options{})
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if want := "/mnt/pool/supervisor.db"; cfg.DBPath != want {
		t.Errorf("db path = %q, want derived %q", cfg.DBPath, want)
	}

	t.Setenv(EnvDBPath, "/explicit/supervisor.db")
	cfg, err = Load(Options{})
	if err != nil {
		t.Fatalf("loading with explicit db path: %v", err)
	}
	if cfg.DBPath != "/explicit/supervisor.db" {
		t.Errorf("db path = %q, want explicit value to beat derivation", cfg.DBPath)
	}
}

func TestUnknownSupervisorEnvVarsAreIgnored(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)
	// Pool runtime variables share the prefix but are not supervisor
	// settings; they must neither break loading nor leak into the struct.
	t.Setenv("SUPERVISOR_GITEA_TOKEN", "pool-token")
	if _, err := Load(Options{}); err != nil {
		t.Fatalf("unrelated SUPERVISOR_* variable must not break loading: %v", err)
	}
}

func TestValidationMissingEncryptionKey(t *testing.T) {
	// Explicitly empty beats any ambient value; an empty key is "missing".
	t.Setenv(EnvDBEncryptionKey, "")
	_, err := Load(Options{})
	wantErrContaining(t, err, EnvDBEncryptionKey, "refuses to start")
}

func TestValidationWhitespaceOnlyEncryptionKey(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, "   ")
	_, err := Load(Options{})
	wantErrContaining(t, err, EnvDBEncryptionKey, "refuses to start")
}

func TestValidationWeakEncryptionKey(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, strings.Repeat("a", MinEncryptionKeyBytes-1))
	_, err := Load(Options{})
	wantErrContaining(t, err, "too weak", "openssl rand -base64 32")
}

func TestValidationFailures(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "invalid log level",
			env:  map[string]string{EnvDBEncryptionKey: testKey, EnvLogLevel: "loud"},
			want: "invalid log level",
		},
		{
			name: "port too low",
			env:  map[string]string{EnvDBEncryptionKey: testKey, EnvPort: "0"},
			want: "invalid HTTP port 0",
		},
		{
			name: "port too high",
			env:  map[string]string{EnvDBEncryptionKey: testKey, EnvPort: "70000"},
			want: "invalid HTTP port 70000",
		},
		{
			name: "backup interval zero",
			env:  map[string]string{EnvDBEncryptionKey: testKey, EnvBackupIntervalHours: "0"},
			want: "backup interval",
		},
		{
			name: "backup retention zero",
			env:  map[string]string{EnvDBEncryptionKey: testKey, EnvBackupRetention: "0"},
			want: "backup retention",
		},
		{
			name: "empty docker host",
			env:  map[string]string{EnvDBEncryptionKey: testKey, EnvDockerHost: " "},
			want: "docker endpoint must not be empty",
		},
		{
			name: "non-numeric port",
			env:  map[string]string{EnvDBEncryptionKey: testKey, EnvPort: "https"},
			want: "decoding configuration",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := Load(Options{})
			wantErrContaining(t, err, tt.want)
		})
	}
}

func TestConfigFileErrors(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)

	t.Run("unsupported extension", func(t *testing.T) {
		path := writeFile(t, "supervisor.json", `{"port": 1}`)
		_, err := Load(Options{Flags: testFlags(t, "--config", path)})
		wantErrContaining(t, err, "unsupported config file extension", ".yaml")
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := Load(Options{Flags: testFlags(t, "--config", "/nonexistent/supervisor.yaml")})
		wantErrContaining(t, err, "loading config file")
	})

	t.Run("malformed yaml", func(t *testing.T) {
		path := writeFile(t, "supervisor.yaml", "port: [oops\n")
		_, err := Load(Options{Flags: testFlags(t, "--config", path)})
		wantErrContaining(t, err, "loading config file")
	})

	t.Run("misspelled key", func(t *testing.T) {
		path := writeFile(t, "supervisor.yaml", "prot: 8080\n")
		_, err := Load(Options{Flags: testFlags(t, "--config", path)})
		wantErrContaining(t, err, "prot")
	})
}

func TestLogLevelNormalization(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)
	t.Setenv(EnvLogLevel, " WARNING ")
	cfg, err := Load(Options{})
	if err != nil {
		t.Fatalf("loading with 'warning' log level: %v", err)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("log level = %q, want normalized %q", cfg.LogLevel, "warn")
	}
}

// --- Embedded Tailscale (RUN-155, docs/26 §4) ---

// TestTailscaleOffByDefaultIgnoresEverything pins the opt-in contract:
// with no auth key the feature is off and stray tailscale variables —
// including malformed booleans — never fail the boot.
func TestTailscaleOffByDefaultIgnoresEverything(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)
	t.Setenv(EnvTailscaleFunnel, "bogus")
	t.Setenv(EnvTailscaleUI, "also-bogus")
	cfg, err := Load(Options{})
	if err != nil {
		t.Fatalf("off-mode load must ignore tailscale values: %v", err)
	}
	if cfg.TailscaleEnabled() {
		t.Error("TailscaleEnabled() = true without an auth key")
	}
	if cfg.TailscaleHostname != DefaultTailscaleHostname {
		t.Errorf("hostname = %q, want default %q", cfg.TailscaleHostname, DefaultTailscaleHostname)
	}
}

// TestTailscaleEnabledDefaults: an auth key alone enables the integration
// with the documented defaults (runnero, both listeners on, state dir
// derived under the data dir).
func TestTailscaleEnabledDefaults(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)
	t.Setenv(EnvTailscaleAuthKey, "tskey-authority-0123456789abcdef")
	cfg, err := Load(Options{})
	if err != nil {
		t.Fatalf("loading with auth key only: %v", err)
	}
	if !cfg.TailscaleEnabled() {
		t.Fatal("TailscaleEnabled() = false with an auth key set")
	}
	if cfg.TailscaleHostname != DefaultTailscaleHostname {
		t.Errorf("hostname = %q, want default %q", cfg.TailscaleHostname, DefaultTailscaleHostname)
	}
	if !cfg.TailscaleFunnelOn() {
		t.Error("funnel off by default, want on")
	}
	if !cfg.TailscaleUIOn() {
		t.Error("ui off by default, want on")
	}
	if want := filepath.Join(DefaultDataDir, DefaultTailscaleStateDirName); cfg.TailscaleStateDir != want {
		t.Errorf("state dir = %q, want derived %q", cfg.TailscaleStateDir, want)
	}
}

// TestTailscaleEnabledCustomValues: every knob is overridable, and the
// boolean parsing accepts strconv spellings.
func TestTailscaleEnabledCustomValues(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)
	t.Setenv(EnvTailscaleAuthKey, " tskey-x ")
	t.Setenv(EnvTailscaleHostname, " lab-runnero ")
	t.Setenv(EnvTailscaleFunnel, "false")
	t.Setenv(EnvTailscaleUI, "1")
	t.Setenv(EnvTailscaleStateDir, " /var/lib/runnero-ts ")
	cfg, err := Load(Options{})
	if err != nil {
		t.Fatalf("loading custom tailscale values: %v", err)
	}
	if cfg.TailscaleAuthKey != "tskey-x" || cfg.TailscaleHostname != "lab-runnero" || cfg.TailscaleStateDir != "/var/lib/runnero-ts" {
		t.Errorf("string values not trimmed: authkey=%q hostname=%q statedir=%q", cfg.TailscaleAuthKey, cfg.TailscaleHostname, cfg.TailscaleStateDir)
	}
	if cfg.TailscaleFunnelOn() {
		t.Error("funnel on, want false")
	}
	if !cfg.TailscaleUIOn() {
		t.Error("ui off, want on ('1')")
	}
}

// TestTailscaleBothListenersOffFailsBoot: a node with no listeners serves
// nothing — a misconfiguration, not a mode (docs/26 §4).
func TestTailscaleBothListenersOffFailsBoot(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)
	t.Setenv(EnvTailscaleAuthKey, "tskey-x")
	t.Setenv(EnvTailscaleFunnel, "false")
	t.Setenv(EnvTailscaleUI, "false")
	_, err := Load(Options{})
	wantErrContaining(t, err, "both listeners are disabled")
}

// TestTailscaleBadBoolFailsOnlyWhenEnabled: malformed booleans are boot
// errors in enabled mode (never silently defaulted).
func TestTailscaleBadBoolFailsOnlyWhenEnabled(t *testing.T) {
	t.Setenv(EnvDBEncryptionKey, testKey)
	t.Setenv(EnvTailscaleAuthKey, "tskey-x")
	t.Setenv(EnvTailscaleFunnel, "maybe")
	_, err := Load(Options{})
	wantErrContaining(t, err, "invalid tailscale funnel setting")
}
