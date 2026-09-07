package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/keys"
)

// newBackupCommand creates the `supervisor backup` subcommand: an on-demand
// SQLite snapshot alongside the automated periodic ones (open questions #21).
func newBackupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "backup",
		Short: "Take an on-demand snapshot of the SQLite database",
		Long: `Writes a consistent SQLite backup to
<data-dir>/backups/supervisor-<timestamp>.db, the same location and format
used by the automated periodic snapshots.`,
		RunE: runBackup,
	}
}

func runBackup(cmd *cobra.Command, _ []string) error {
	derivedKeys, err := keys.Derive(cfg.DBEncryptionKey)
	if err != nil {
		return fmt.Errorf("backup: deriving encryption keys: %w", err)
	}

	database, err := db.Open(db.Options{
		Path:          cfg.DBPath,
		EncryptionKey: derivedKeys.DBEncryptionKey,
	})
	if err != nil {
		return fmt.Errorf("backup: opening database: %w", err)
	}
	defer func() { _ = database.Close() }()

	bm := db.NewBackupManager(database, cfg.DataDir, cfg.BackupIntervalHours, cfg.BackupRetentionCount)
	snapshotPath, err := bm.Snapshot(cmd.Context())
	if err != nil {
		return fmt.Errorf("backup: creating snapshot: %w", err)
	}

	logger.Info("database snapshot created", "path", snapshotPath)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Successfully created backup snapshot at %s\n", snapshotPath)
	return nil
}
