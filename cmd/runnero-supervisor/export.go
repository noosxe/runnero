package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/keys"
)

// newExportCommand creates the `supervisor export` subcommand: exports current
// database state as sanitized YAML for backup or GitOps versioning (docs/02-architecture-design.md §4).
func newExportCommand() *cobra.Command {
	var outputPath string

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export current database state as sanitized YAML",
		Long: `Export outputs the database state as sanitized YAML for GitOps versioning
or backup. All credentials and private keys are replaced with placeholders or reference keys;
zero plaintext secrets are ever exported.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
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

			sanitized, err := database.ExportSanitizedConfig(cmd.Context())
			if err != nil {
				return fmt.Errorf("exporting configuration: %w", err)
			}

			yamlBytes, err := sanitized.ToYAML()
			if err != nil {
				return fmt.Errorf("marshaling YAML: %w", err)
			}

			if outputPath != "" {
				if err := os.WriteFile(outputPath, yamlBytes, 0o600); err != nil {
					return fmt.Errorf("writing export file %q: %w", outputPath, err)
				}
				logger.Info("sanitized configuration exported", "output", outputPath)
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Successfully exported sanitized configuration to %s\n", outputPath)
			} else {
				_, _ = cmd.OutOrStdout().Write(yamlBytes)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "output file path (defaults to stdout)")
	return cmd
}
