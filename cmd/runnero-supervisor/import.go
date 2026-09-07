package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/keys"
	"github.com/noosxe/runnero/internal/server"
)

// newImportCommand creates the `supervisor import` subcommand: an explicit
// and destructive re-import of pool configuration from a YAML file into
// the database (docs/02-architecture-design.md §4).
func newImportCommand() *cobra.Command {
	var mode string
	var yes bool
	var configPath string

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import pool configuration from a YAML file into the database",
		Long: `Import merges or overwrites database state from the YAML file given via
--config. This is a destructive operation requiring confirmation unless --yes is supplied.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if configPath == "" {
				configPath = cfg.ConfigFile
			}
			if configPath == "" {
				return errors.New("import: the --config flag is required (path to a YAML configuration file)")
			}

			parsedMode := db.ImportMode(strings.ToLower(mode))
			if parsedMode != db.ImportModeOverwrite && parsedMode != db.ImportModeMerge {
				return fmt.Errorf("import: invalid --mode %q (must be 'overwrite' or 'merge')", mode)
			}

			// Interactive confirmation unless --yes is provided
			if !yes {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "WARNING: This will %s existing database configuration from %s. Are you sure? [y/N]: ", parsedMode, configPath)
				reader := bufio.NewReader(cmd.InOrStdin())
				answer, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("reading confirmation: %w", err)
				}
				answer = strings.TrimSpace(strings.ToLower(answer))
				if answer != "y" && answer != "yes" {
					return errors.New("import cancelled by user")
				}
			}

			content, err := os.ReadFile(configPath)
			if err != nil {
				return fmt.Errorf("reading config file %q: %w", configPath, err)
			}

			seedCfg, err := db.ParseSeedConfig(content)
			if err != nil {
				return fmt.Errorf("parsing configuration: %w", err)
			}

			derivedKeys, err := keys.Derive(cfg.DBEncryptionKey)
			if err != nil {
				return fmt.Errorf("deriving database encryption key: %w", err)
			}

			database, err := db.Open(db.Options{
				Path:          cfg.DBPath,
				EncryptionKey: derivedKeys.DBEncryptionKey,
			})
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer func() { _ = database.Close() }()

			if err := database.ImportSeedConfig(cmd.Context(), seedCfg, parsedMode); err != nil {
				return fmt.Errorf("importing seed config: %w", err)
			}

			server.RecordAuditLog(cmd.Context(), database, "config.import", "configuration", nil, map[string]any{
				"config_path": configPath,
				"mode":        parsedMode,
				"source":      "cli",
			})

			logger.Info("configuration imported successfully", "config", configPath, "mode", parsedMode)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Successfully imported configuration from %s (mode: %s)\n", configPath, parsedMode)
			return nil
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "path to the YAML configuration file to import")
	cmd.Flags().StringVarP(&mode, "mode", "m", string(db.ImportModeOverwrite), "import mode: 'overwrite' or 'merge'")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip interactive confirmation prompt")
	return cmd
}
