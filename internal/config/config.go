package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"
)

// EnvPrefix scopes every supervisor environment variable. Variables named
// SUPERVISOR_<CONTRACT_KEY> map onto the same setting as the matching CLI
// flag or file key, e.g. SUPERVISOR_DB_PATH -> key "db-path" -> --db-path.
const EnvPrefix = "SUPERVISOR_"

// Environment variable names for the supervisor contract.
const (
	EnvDBEncryptionKey      = EnvPrefix + "DB_ENCRYPTION_KEY"
	EnvPort                 = EnvPrefix + "PORT"
	EnvDBPath               = EnvPrefix + "DB_PATH"
	EnvLogLevel             = EnvPrefix + "LOG_LEVEL"
	EnvDockerHost           = EnvPrefix + "DOCKER_HOST"
	EnvDataDir              = EnvPrefix + "DATA_DIR"
	EnvBackupIntervalHours  = EnvPrefix + "BACKUP_INTERVAL_HOURS"
	EnvBackupRetention      = EnvPrefix + "BACKUP_RETENTION_COUNT"
	EnvConfigFile           = EnvPrefix + "CONFIG"
	EnvSecureCookie         = EnvPrefix + "SECURE_COOKIE"
	EnvEnrichJobConclusions = EnvPrefix + "ENRICH_JOB_CONCLUSIONS"
	EnvWebhookGitHubSecret  = EnvPrefix + "WEBHOOK_GITHUB_SECRET"
	EnvWebhookGiteaSecret   = EnvPrefix + "WEBHOOK_GITEA_SECRET"
	EnvWebhookForgejoSecret = EnvPrefix + "WEBHOOK_FORGEJO_SECRET"

	// Embedded Tailscale integration (RUN-155, docs/26). The feature is
	// completely off unless SUPERVISOR_TAILSCALE_AUTHKEY is set; every other
	// variable is ignored (and never validated) in off mode.
	EnvTailscaleAuthKey  = EnvPrefix + "TAILSCALE_AUTHKEY"
	EnvTailscaleHostname = EnvPrefix + "TAILSCALE_HOSTNAME"
	EnvTailscaleFunnel   = EnvPrefix + "TAILSCALE_FUNNEL"
	EnvTailscaleUI       = EnvPrefix + "TAILSCALE_UI"
	EnvTailscaleStateDir = EnvPrefix + "TAILSCALE_STATE_DIR"
)

// Default values for the supervisor environment contract (docs/open-questions.md #3).
const (
	DefaultPort                 = 8090
	DefaultLogLevel             = "info"
	DefaultDataDir              = "/data"
	DefaultDBFileName           = "supervisor.db"
	DefaultDockerHost           = "unix:///var/run/docker.sock"
	DefaultBackupIntervalHours  = 6
	DefaultBackupRetentionCount = 7

	// Embedded Tailscale defaults (RUN-155, docs/26 §4).
	DefaultTailscaleHostname     = "runnero"
	DefaultTailscaleStateDirName = "tailscale"
	DefaultTailscaleFunnel       = "true"
	DefaultTailscaleUI           = "true"
)

// MinEncryptionKeyBytes is the minimum acceptable length for
// DBEncryptionKey in bytes. The key seeds AES-256 encryption of
// credentials at rest and, via HKDF, the JWT signing secret (RUN-9), so it
// must carry at least 256 bits of key material.
const MinEncryptionKeyBytes = 32

// envKeys maps contract environment variables onto flat koanf keys. The
// keys double as the YAML/TOML file keys and (with dashes) the CLI flag
// names, keeping all three layers spelled identically.
var envKeys = map[string]string{
	EnvDBEncryptionKey:      "db-encryption-key",
	EnvPort:                 "port",
	EnvDBPath:               "db-path",
	EnvLogLevel:             "log-level",
	EnvDockerHost:           "docker-host",
	EnvDataDir:              "data-dir",
	EnvBackupIntervalHours:  "backup-interval-hours",
	EnvBackupRetention:      "backup-retention-count",
	EnvEnrichJobConclusions: "enrich-job-conclusions",
	EnvConfigFile:           "config",
	EnvSecureCookie:         "secure-cookie",
	EnvWebhookGitHubSecret:  "webhook-github-secret",
	EnvWebhookGiteaSecret:   "webhook-gitea-secret",
	EnvWebhookForgejoSecret: "webhook-forgejo-secret",

	EnvTailscaleAuthKey:  "tailscale-auth-key",
	EnvTailscaleHostname: "tailscale-hostname",
	EnvTailscaleFunnel:   "tailscale-funnel",
	EnvTailscaleUI:       "tailscale-ui",
	EnvTailscaleStateDir: "tailscale-state-dir",
}

// Config is the typed result of loading every configuration layer. Field
// tags carry the canonical flat key shared by files, environment, and flags.
type Config struct {
	DBEncryptionKey      string `koanf:"db-encryption-key"`
	Port                 int    `koanf:"port"`
	DBPath               string `koanf:"db-path"`
	LogLevel             string `koanf:"log-level"`
	DockerHost           string `koanf:"docker-host"`
	DataDir              string `koanf:"data-dir"`
	BackupIntervalHours  int    `koanf:"backup-interval-hours"`
	BackupRetentionCount int    `koanf:"backup-retention-count"`
	ConfigFile           string `koanf:"config"`
	SecureCookie         bool   `koanf:"secure-cookie"`
	EnrichJobConclusions bool   `koanf:"enrich-job-conclusions"`
	WebhookGitHubSecret  string `koanf:"webhook-github-secret"`
	WebhookGiteaSecret   string `koanf:"webhook-gitea-secret"`
	WebhookForgejoSecret string `koanf:"webhook-forgejo-secret"`

	// Embedded Tailscale integration (RUN-155, docs/26 §4). TailscaleFunnel
	// and TailscaleUI hold the raw string values because a malformed boolean
	// must only fail boots that actually enabled the feature (an unset
	// auth key keeps the integration completely off, docs/26 §4); the
	// typed accessors below parse them after Validate.
	TailscaleAuthKey  string `koanf:"tailscale-auth-key"`
	TailscaleHostname string `koanf:"tailscale-hostname"`
	TailscaleFunnel   string `koanf:"tailscale-funnel"`
	TailscaleUI       string `koanf:"tailscale-ui"`
	TailscaleStateDir string `koanf:"tailscale-state-dir"`
}

// Options parameterizes Load. The zero value loads defaults plus the
// process environment only.
type Options struct {
	// Flags is the parsed persistent flag set of the root command (the
	// highest-precedence layer). It is expected to define the flags
	// registered by cmd/supervisor (--config, --log-level, --data-dir,
	// --db-path, --port, --docker-host); only flags the user explicitly
	// passed take effect. May be nil.
	Flags *pflag.FlagSet
}

// Load merges every configuration layer and validates the result.
//
// Precedence, lowest to highest:
//
//  1. built-in defaults (the documented environment contract),
//  2. an optional YAML/TOML file (--config flag or SUPERVISOR_CONFIG),
//  3. environment variables (SUPERVISOR_* prefix),
//  4. CLI flags.
//
// Only flags the user actually passed override lower layers; untouched flag
// defaults never shadow values coming from the file or the environment.
func Load(opts Options) (*Config, error) {
	ko := koanf.New(".")

	if err := ko.Load(confmap.Provider(defaults(), "."), nil); err != nil {
		return nil, fmt.Errorf("loading default configuration: %w", err)
	}

	configPath := configFileFrom(opts.Flags)
	if configPath != "" {
		parser, err := fileParser(configPath)
		if err != nil {
			return nil, err
		}
		if err := ko.Load(file.Provider(configPath), parser); err != nil {
			return nil, fmt.Errorf("loading config file %q: %w", configPath, err)
		}
	}
	if err := ko.Load(env.Provider(EnvPrefix, ".", func(name string) string {
		// Map only the variables in the supervisor contract; unrelated
		// SUPERVISOR_* variables (e.g. pool tokens like
		// SUPERVISOR_GITEA_TOKEN) belong to the components that read them.
		return envKeys[name]
	}), nil); err != nil {
		return nil, fmt.Errorf("loading environment variables: %w", err)
	}

	if opts.Flags != nil {
		if err := ko.Load(posflag.Provider(opts.Flags, ".", ko), nil); err != nil {
			return nil, fmt.Errorf("loading CLI flags: %w", err)
		}
	}

	var cfg Config
	if err := ko.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{
		DecoderConfig: &mapstructure.DecoderConfig{
			Result:           &cfg,
			WeaklyTypedInput: true, // environment values arrive as strings
			ErrorUnused:      true, // surface misspelled keys instead of ignoring them
		},
	}); err != nil {
		return nil, fmt.Errorf("decoding configuration (check key names and value types): %w", err)
	}

	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// Debug-only and strictly non-secret fields: the encryption key must
	// never appear in any log record.
	logger.Debug("configuration loaded",
		"config_file", configPath,
		"data_dir", cfg.DataDir,
		"db_path", cfg.DBPath,
		"port", cfg.Port,
	)
	return &cfg, nil
}

// defaults returns the lowest layer: the documented default for every
// contract key. db-path and db-encryption-key stay empty on purpose so a
// missing db-path can be derived from data-dir and a missing encryption
// key is reported by validation rather than silently defaulted.
func defaults() map[string]any {
	return map[string]any{
		"port":                   DefaultPort,
		"db-path":                "",
		"log-level":              DefaultLogLevel,
		"docker-host":            DefaultDockerHost,
		"data-dir":               DefaultDataDir,
		"backup-interval-hours":  DefaultBackupIntervalHours,
		"backup-retention-count": DefaultBackupRetentionCount,
		"db-encryption-key":      "",
		"enrich-job-conclusions": true,
		"webhook-github-secret":  "",
		"webhook-gitea-secret":   "",
		"webhook-forgejo-secret": "",

		"tailscale-auth-key":  "",
		"tailscale-hostname":  DefaultTailscaleHostname,
		"tailscale-funnel":    DefaultTailscaleFunnel,
		"tailscale-ui":        DefaultTailscaleUI,
		"tailscale-state-dir": "",
	}
}

// configFileFrom resolves the configuration file path: an explicit
// --config flag wins, otherwise SUPERVISOR_CONFIG may point at the file
// (handy for containers that configure everything through the environment).
func configFileFrom(flags *pflag.FlagSet) string {
	if flags != nil {
		if f := flags.Lookup("config"); f != nil {
			if p := strings.TrimSpace(f.Value.String()); p != "" {
				return p
			}
		}
	}
	return strings.TrimSpace(os.Getenv(EnvConfigFile))
}

// fileParser picks the koanf parser for a config file from its extension.
func fileParser(path string) (koanf.Parser, error) {
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".yaml", ".yml":
		return yaml.Parser(), nil
	case ".toml":
		return toml.Parser(), nil
	default:
		return nil, fmt.Errorf("unsupported config file extension %q (%q): want .yaml, .yml, or .toml", ext, path)
	}
}

// normalize canonicalizes values after decoding: trims stray whitespace,
// lowercases the log level (accepting "warning" as "warn"), and derives the
// database path from the data directory when it was left unset.
func (c *Config) normalize() {
	c.DBEncryptionKey = strings.TrimSpace(c.DBEncryptionKey)
	c.LogLevel = strings.ToLower(strings.TrimSpace(c.LogLevel))
	if c.LogLevel == "warning" {
		c.LogLevel = "warn"
	}
	c.DataDir = strings.TrimSpace(c.DataDir)
	c.DBPath = strings.TrimSpace(c.DBPath)
	c.DockerHost = strings.TrimSpace(c.DockerHost)
	c.WebhookGitHubSecret = strings.TrimSpace(c.WebhookGitHubSecret)
	c.WebhookGiteaSecret = strings.TrimSpace(c.WebhookGiteaSecret)
	c.WebhookForgejoSecret = strings.TrimSpace(c.WebhookForgejoSecret)
	c.TailscaleAuthKey = strings.TrimSpace(c.TailscaleAuthKey)
	c.TailscaleHostname = strings.TrimSpace(c.TailscaleHostname)
	c.TailscaleFunnel = strings.TrimSpace(c.TailscaleFunnel)
	c.TailscaleUI = strings.TrimSpace(c.TailscaleUI)
	c.TailscaleStateDir = strings.TrimSpace(c.TailscaleStateDir)
	if c.TailscaleEnabled() && c.TailscaleStateDir == "" && c.DataDir != "" {
		c.TailscaleStateDir = filepath.Join(c.DataDir, DefaultTailscaleStateDirName)
	}
	if c.DBPath == "" && c.DataDir != "" {
		c.DBPath = filepath.Join(c.DataDir, DefaultDBFileName)
	}
}

// TailscaleEnabled reports whether the embedded Tailscale integration is
// activated (RUN-155, docs/26): a non-empty auth key opts the deployment in;
// anything unset means the feature is completely off and every other
// Tailscale setting is ignored.
func (c *Config) TailscaleEnabled() bool {
	return c.TailscaleAuthKey != ""
}

// TailscaleFunnelOn reports whether the public funnel webhook listener
// (`:443`) should open. Call only on a validated config (Validate rejects
// malformed values in enabled mode); an unset value means the default (true).
func (c *Config) TailscaleFunnelOn() bool {
	v, _ := parseTailscaleBool(c.TailscaleFunnel)
	return v
}

// TailscaleUIOn reports whether the tailnet-only management listener
// (`:8443`) should open. Call only on a validated config; an unset value
// means the default (true).
func (c *Config) TailscaleUIOn() bool {
	v, _ := parseTailscaleBool(c.TailscaleUI)
	return v
}

// parseTailscaleBool interprets the raw funnel/ui settings: empty means the
// documented default (true), anything else must be a strconv boolean.
func parseTailscaleBool(raw string) (bool, error) {
	if raw == "" {
		return true, nil
	}
	return strconv.ParseBool(raw)
}
