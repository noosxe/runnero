package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/noosxe/runnero/internal/config"
	"github.com/noosxe/runnero/internal/logging"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

// errNotImplemented is returned by the stub handlers below so callers and
// scripts never mistake a stub for a successful run. Later milestones
// replace the stubs and drop this sentinel.
var errNotImplemented = errors.New("not implemented yet")

// cfg holds the fully merged and validated configuration once the root
// command's PersistentPreRunE has run. Every subcommand reads its
// settings from here instead of touching flags or the environment.
var cfg *config.Config

// logger is the CLI module logger (docs/06 §1): command-level startup
// and stub messages flow through it; daemon subsystems derive their own
// via logging.For as they land in later milestones.
var logger = logging.For("cli")

// NewRootCommand builds the `supervisor` root command with all subcommands
// and persistent flags attached.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:     "runnero-supervisor",
		Aliases: []string{"supervisor"},
		Short:   "Runnero Supervisor for ephemeral GitHub/Gitea/Forgejo runner containers",
		Long: `runnero-supervisor manages dynamic pools of ephemeral GitHub, Gitea, and Forgejo
Actions runner containers and serves the embedded web control interface.

Run it with no subcommand to start the daemon.`,
		Version:           version,
		SilenceUsage:      true, // runtime errors should not repeat the usage text
		PersistentPreRunE: bindFlagsToConfig,
		// The daemon is the default: a bare `supervisor` (optionally with
		// persistent flags) starts it.
		RunE: runDaemon,
	}

	// Flag values are bound to throwaway locals: everything flows through
	// the typed config.Config produced by internal/config, which loads
	// these flags as the highest-precedence layer (RUN-7).
	var flagConfig, flagLogLevel, flagDataDir, flagDBPath, flagDockerHost string
	var flagPort int
	var flagSecureCookie bool
	var flagSecureCookies string
	var flagSessionIdleTimeout time.Duration
	var flagSessionAbsoluteTimeout time.Duration
	var flagBcryptCost int
	var flagAuditRetention time.Duration
	var flagTrustedProxy bool
	var flagWebAuthnRPID string
	var flagWebAuthnOrigins string
	f := root.PersistentFlags()
	f.StringVarP(&flagConfig, "config", "c", "", "path to the configuration file (YAML or TOML)")
	f.StringVar(&flagLogLevel, "log-level", "info", "log level (debug, info, warn, error)")
	f.StringVar(&flagDataDir, "data-dir", "/data", "data directory holding the database, backups, and runner logs")
	f.StringVar(&flagDBPath, "db-path", "", "path to the SQLite database file (defaults to <data-dir>/supervisor.db)")
	f.IntVar(&flagPort, "port", config.DefaultPort, "HTTP port for the API and web control interface")
	f.StringVar(&flagDockerHost, "docker-host", "", "Docker daemon endpoint (defaults to the local Docker socket)")
	f.BoolVar(&flagSecureCookie, "secure-cookie", false, "deprecated: legacy boolean for session-cookie Secure; superseded by --secure-cookies (maps true to always)")
	f.StringVar(&flagSecureCookies, "secure-cookies", config.DefaultSecureCookieMode, "session cookie Secure attribute mode: auto (HTTPS requests only), always, never (plain-HTTP LAN)")
	f.DurationVar(&flagSessionIdleTimeout, "session-idle-timeout", mustDuration(config.DefaultSessionIdleTimeout), "sliding idle timeout: sessions unused this long are deleted")
	f.DurationVar(&flagSessionAbsoluteTimeout, "session-absolute-timeout", mustDuration(config.DefaultSessionAbsoluteTimeout), "absolute session lifetime cap (fixed at login, never extended)")
	f.IntVar(&flagBcryptCost, "bcrypt-cost", config.DefaultBcryptCost, "bcrypt cost for new or changed passwords (4-31)")
	f.DurationVar(&flagAuditRetention, "audit-retention", config.DefaultAuditRetention, "audit log purge horizon (default 2160h = 90 days, docs/32)")
	f.BoolVar(&flagTrustedProxy, "trusted-proxy", false, "trust X-Forwarded-For / X-Forwarded-Proto from the reverse proxy (client IP and HTTPS detection)")
	f.StringVar(&flagWebAuthnRPID, "webauthn-rp-id", "", "Relying Party ID for passkey login (effective domain, e.g. runnero.tailnet-xyz.ts.net); empty keeps passkeys off (docs/34)")
	f.StringVar(&flagWebAuthnOrigins, "webauthn-origins", "", "CSV of allowed WebAuthn ceremony origins; defaults to https://<webauthn-rp-id> (docs/34)")

	root.AddCommand(
		newDaemonCommand(),
		newImportCommand(),
		newExportCommand(),
		newResetPasswordCommand(),
		newBackupCommand(),
	)
	return root
}

// Execute runs the root command. Cobra prints errors itself; main only
// needs the error value to decide the exit code.
func Execute() error {
	return NewRootCommand().Execute()
}

// bindFlagsToConfig loads the full configuration stack (defaults, config
// file, SUPERVISOR_* environment, CLI flags) via internal/config, which
// also validates the result, and installs the process-wide logger.
func bindFlagsToConfig(cmd *cobra.Command, _ []string) error {
	loaded, err := config.Load(config.Options{Flags: cmd.Root().PersistentFlags()})
	if err != nil {
		return err
	}
	cfg = loaded
	return logging.Setup(logging.Options{Level: cfg.LogLevel})
}

// mustDuration parses a compile-time default duration string; it panics on
// a typo because a broken built-in default is a programming error.
func mustDuration(raw string) time.Duration {
	d, err := time.ParseDuration(raw)
	if err != nil {
		panic(fmt.Sprintf("invalid built-in duration default %q: %v", raw, err))
	}
	return d
}
