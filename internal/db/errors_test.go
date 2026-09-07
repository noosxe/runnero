package db

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// TestIsUniqueConstraintError pins the detection of SQLite UNIQUE constraint
// violations against the real driver (used by callers to map duplicates to
// transport-level "already exists" errors, e.g. duplicate auth_profiles.name).
func TestIsUniqueConstraintError(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(Options{Path: filepath.Join(dir, "test.db")})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()
	if _, err := database.SQL().ExecContext(ctx, "CREATE TABLE uniq_probe (name TEXT UNIQUE)"); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	if _, err := database.SQL().ExecContext(ctx, "INSERT INTO uniq_probe VALUES ('a')"); err != nil {
		t.Fatalf("seed probe table: %v", err)
	}
	_, dupErr := database.SQL().ExecContext(ctx, "INSERT INTO uniq_probe VALUES ('a')")
	if dupErr == nil {
		t.Fatal("expected duplicate insert to fail")
	}
	if !IsUniqueConstraintError(dupErr) {
		t.Errorf("IsUniqueConstraintError(duplicate insert) = false, want true (err: %v)", dupErr)
	}

	if _, err := database.SQL().ExecContext(ctx, "INSERT INTO uniq_probe VALUES (NULL)"); err != nil {
		t.Fatalf("non-unique insert failed unexpectedly: %v", err)
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated driver error", errors.New("constraint failed: NOT NULL constraint failed: t.col (1299)"), false},
		{"wrapped unique error", fmt.Errorf("updating row: %w", dupErr), true},
		{"plain unique message", errors.New("constraint failed: UNIQUE constraint failed: t.name (2067)"), true},
	}
	for _, tc := range cases {
		if got := IsUniqueConstraintError(tc.err); got != tc.want {
			t.Errorf("%s: IsUniqueConstraintError = %v, want %v", tc.name, got, tc.want)
		}
	}
}
