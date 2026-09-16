package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/google/uuid"
)

// instanceIDKey is the app_settings key holding this supervisor's persistent
// instance identity (RUN-240, docs/33 §3.4).
const instanceIDKey = "instance_id"

// EnsureInstanceID returns the supervisor's persistent instance id, creating
// a UUID v4 on first boot and persisting it in app_settings. The id stamps
// spawned containers with the com.runnero.owner label for forensics
// (docs/33 §3.4) and is deliberately persistent across restarts: restart
// adoption (docs/23) must keep recognizing this instance's own runners.
// It is metadata only - never an authorization check (docs/33 §5).
func (d *DB) EnsureInstanceID(ctx context.Context) (string, error) {
	var existing string
	err := d.sqlDB.QueryRowContext(ctx,
		`SELECT value FROM app_settings WHERE key = ?`, instanceIDKey,
	).Scan(&existing)
	if err == nil && existing != "" {
		return existing, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("reading instance id: %w", err)
	}

	id := uuid.NewString()
	if _, err := d.sqlDB.ExecContext(ctx,
		`INSERT INTO app_settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO NOTHING`, instanceIDKey, id,
	); err != nil {
		return "", fmt.Errorf("persisting instance id: %w", err)
	}

	// Re-read: a concurrent boot could have won the insert; whoever's value
	// is stored is the instance identity everyone uses.
	stored, err := d.EnsureInstanceID(ctx)
	if err != nil {
		return "", err
	}
	return stored, nil
}
