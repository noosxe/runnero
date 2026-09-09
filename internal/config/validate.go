package config

import "fmt"

// Validate enforces the supervisor environment contract. Every message is
// actionable: it names the offending value and every knob (file key,
// environment variable, CLI flag) that could have set it. The encryption
// key is validated last so purely structural problems surface first.
func (c *Config) Validate() error {
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid log level %q: want debug, info, warn, or error (key 'log-level', env %s, flag --log-level)", c.LogLevel, EnvLogLevel)
	}

	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid HTTP port %d: must be between 1 and 65535 (key 'port', env %s, flag --port)", c.Port, EnvPort)
	}

	if c.DataDir == "" {
		return fmt.Errorf("data directory must not be empty (key 'data-dir', env %s, flag --data-dir)", EnvDataDir)
	}

	if c.DBPath == "" {
		return fmt.Errorf("database path must not be empty (key 'db-path', env %s, flag --db-path)", EnvDBPath)
	}

	if c.DockerHost == "" {
		return fmt.Errorf("docker endpoint must not be empty (key 'docker-host', env %s, flag --docker-host)", EnvDockerHost)
	}

	if c.BackupIntervalHours < 1 {
		return fmt.Errorf("backup interval must be at least 1 hour (key 'backup-interval-hours', env %s)", EnvBackupIntervalHours)
	}

	if c.BackupRetentionCount < 1 {
		return fmt.Errorf("backup retention must keep at least 1 snapshot (key 'backup-retention-count', env %s)", EnvBackupRetention)
	}

	if c.TailscaleEnabled() {
		funnel, funnelErr := parseTailscaleBool(c.TailscaleFunnel)
		ui, uiErr := parseTailscaleBool(c.TailscaleUI)
		if funnelErr != nil {
			return fmt.Errorf("invalid tailscale funnel setting %q: want a boolean (key 'tailscale-funnel', env %s)", c.TailscaleFunnel, EnvTailscaleFunnel)
		}
		if uiErr != nil {
			return fmt.Errorf("invalid tailscale ui setting %q: want a boolean (key 'tailscale-ui', env %s)", c.TailscaleUI, EnvTailscaleUI)
		}
		if !funnel && !ui {
			return fmt.Errorf("tailscale is enabled (env %s set) but both listeners are disabled: set %s=true and/or %s=true, or unset the auth key to turn the integration off", EnvTailscaleAuthKey, EnvTailscaleFunnel, EnvTailscaleUI)
		}
	}

	return c.validateDBEncryptionKey()

}

// validateDBEncryptionKey refuses to run with a missing or weak database
// encryption key: it protects credentials stored in SQLite (AES-256, docs/05)
// and seeds the JWT signing secret (RUN-9), so there is no safe default.
func (c *Config) validateDBEncryptionKey() error {
	if c.DBEncryptionKey == "" {
		return fmt.Errorf("missing database encryption key: set the %s environment variable (generate one with: openssl rand -base64 32); the supervisor refuses to start without it", EnvDBEncryptionKey)
	}
	if n := len([]byte(c.DBEncryptionKey)); n < MinEncryptionKeyBytes {
		return fmt.Errorf("database encryption key is too weak: %d bytes, want at least %d (generate one with: openssl rand -base64 32; set it via %s)", n, MinEncryptionKeyBytes, EnvDBEncryptionKey)
	}
	return nil
}
