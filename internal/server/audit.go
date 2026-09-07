package server

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/noosxe/runnero/internal/db"
)

// AuditLogDatabase abstracts the database method to insert audit log rows.
type AuditLogDatabase interface {
	CreateAuditLog(ctx context.Context, arg db.CreateAuditLogParams) (db.AuditLog, error)
}

// RecordAuditLog records an audit log row for an authenticated or context-tracked admin action.
// Action names follow uniform dot-notation (e.g. 'pool.create', 'runner.terminate', 'setting.update').
// details is serialized as a JSON string.
func RecordAuditLog(ctx context.Context, database AuditLogDatabase, action, resourceType string, resourceID *int64, details any) {
	var explicitUserID *int64
	if user, ok := GetUserContext(ctx); ok && user.UserID > 0 {
		explicitUserID = &user.UserID
	}
	RecordAuditLogWithUser(ctx, database, explicitUserID, action, resourceType, resourceID, details)
}

// recordAuditLog is an internal alias for RecordAuditLog.
func recordAuditLog(ctx context.Context, database AuditLogDatabase, action, resourceType string, resourceID *int64, details any) {
	RecordAuditLog(ctx, database, action, resourceType, resourceID, details)
}

// recordAuditLogWithUser is an internal alias for RecordAuditLogWithUser.
func recordAuditLogWithUser(ctx context.Context, database AuditLogDatabase, userID *int64, action, resourceType string, resourceID *int64, details any) {
	RecordAuditLogWithUser(ctx, database, userID, action, resourceType, resourceID, details)
}

// RecordAuditLogWithUser records an audit log row with an explicit user ID (e.g. during login or setup).
func RecordAuditLogWithUser(ctx context.Context, database AuditLogDatabase, userID *int64, action, resourceType string, resourceID *int64, details any) {
	if database == nil {
		return
	}

	var uid sql.NullInt64
	if userID != nil && *userID > 0 {
		uid = sql.NullInt64{Int64: *userID, Valid: true}
	}

	var resID sql.NullInt64
	if resourceID != nil && *resourceID > 0 {
		resID = sql.NullInt64{Int64: *resourceID, Valid: true}
	}

	var detailsJSON sql.NullString
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			detailsJSON = sql.NullString{String: string(b), Valid: true}
		}
	}

	_, _ = database.CreateAuditLog(ctx, db.CreateAuditLogParams{
		UserID:       uid,
		Action:       action,
		ResourceType: sql.NullString{String: resourceType, Valid: resourceType != ""},
		ResourceID:   resID,
		Details:      detailsJSON,
	})
}
