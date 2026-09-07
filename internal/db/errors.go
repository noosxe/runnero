package db

import (
	"errors"
	"strings"

	"modernc.org/sqlite"
)

// sqliteUniqueConstraintCode is the SQLite extended result code
// SQLITE_CONSTRAINT_UNIQUE (2067) returned by the modernc.org/sqlite driver
// when an INSERT/UPDATE violates a UNIQUE constraint.
const sqliteUniqueConstraintCode = 2067

// IsUniqueConstraintError reports whether err is a SQLite UNIQUE constraint
// violation as surfaced by the modernc.org/sqlite driver (for example, a
// duplicate auth_profiles.name). Callers map it to transport-level
// "already exists" errors. A message fallback keeps the check robust across
// driver versions that may wrap or re-shape the typed error.
func IsUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqliteUniqueConstraintCode {
		return true
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
