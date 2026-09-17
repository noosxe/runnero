package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
)

// User management (RUN-236, docs/35): real viewer accounts plus the
// admin-only management surface. Roles are enforced live by the auth
// interceptor (it re-reads the user row on every request), so a role
// change applies to the target's next request with no re-login and no
// staleness window; the handlers here only maintain admin_users.

// Audit actions for the user lifecycle (docs/35 §3). Target identity is
// carried by username in details so events survive target deletion
// (audit_logs.user_id is ON DELETE SET NULL). Details never carry
// passwords or tokens (leakage tests).
const (
	ActionAuthUserCreated   = "auth.user_created"
	ActionAuthUserDeleted   = "auth.user_deleted"
	ActionAuthRoleChanged   = "auth.role_changed"
	ActionAuthPasswordReset = "auth.password_reset"
)

// RoleAdmin and RoleViewer are the only valid admin_users.role values
// (docs/35 §2.1). Enforced at every write path below; the role column
// itself carries no CHECK constraint (SQLite cannot add one by ALTER), so
// these checks are the enforcement point.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// UserDatabase defines the database subset required by the UserService.
// *db.DB satisfies this interface directly.
type UserDatabase interface {
	ListAdminUsers(ctx context.Context) ([]db.AdminUser, error)
	GetAdminUserByUsername(ctx context.Context, username string) (db.AdminUser, error)
	CreateAdminUser(ctx context.Context, arg db.CreateAdminUserParams) (db.AdminUser, error)
	UpdateAdminRole(ctx context.Context, arg db.UpdateAdminRoleParams) (db.AdminUser, error)
	UpdateAdminPassword(ctx context.Context, arg db.UpdateAdminPasswordParams) (db.AdminUser, error)
	DeleteAdminUser(ctx context.Context, id int64) error
	CountAdminRoleUsers(ctx context.Context) (int64, error)
	DeleteOtherSessionsByUserId(ctx context.Context, arg db.DeleteOtherSessionsByUserIdParams) (int64, error)
	CreateAuditLog(ctx context.Context, arg db.CreateAuditLogParams) (db.AuditLog, error)
}

// UserService implements the admin-bucket user-management RPCs
// (docs/35 §2.3).
type UserService struct {
	supervisorv1connect.UnimplementedUserServiceHandler
	db  UserDatabase
	cfg SessionConfig

	// guardMu serializes last-admin guard sequences (count-then-write).
	// SQLite is embedded and single-process: every writer of admin_users
	// runs in this process, and these handlers are the only role writers,
	// so holding the mutex across the pair makes concurrent demotions or
	// deletes of the last admin impossible — the invariant docs/35 §2.3
	// demands, without depending on SQLite transaction-upgrade semantics.
	guardMu sync.Mutex
}

// NewUserService constructs the user-management service. Registered only
// when an auth database exists (same condition as AuthService — users and
// sessions share the account store).
func NewUserService(authDB UserDatabase, cfg SessionConfig) *UserService {
	return &UserService{db: authDB, cfg: cfg}
}

// userInfoFromRow maps an admin_users row to its wire shape. Only the
// username, role, and creation time are exposed — no session or login
// metadata (docs/35 OQ-5).
func userInfoFromRow(u db.AdminUser) *supervisorv1.UserInfo {
	return &supervisorv1.UserInfo{
		Username:  u.Username,
		Role:      u.Role,
		CreatedAt: timestamppb.New(u.CreatedAt),
	}
}

// ListUsers returns all users in id order.
func (s *UserService) ListUsers(ctx context.Context, _ *connect.Request[supervisorv1.ListUsersRequest]) (*connect.Response[supervisorv1.ListUsersResponse], error) {
	rows, err := s.db.ListAdminUsers(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list users"))
	}
	users := make([]*supervisorv1.UserInfo, 0, len(rows))
	for _, row := range rows {
		users = append(users, userInfoFromRow(row))
	}
	return connect.NewResponse(&supervisorv1.ListUsersResponse{Users: users}), nil
}

// validRole reports whether the wire-supplied role string is one of the
// two enforced values.
func validRole(role string) bool {
	return role == RoleAdmin || role == RoleViewer
}

// CreateUser adds an account with an initial password and role. The
// password floor (12) is enforced wire-side by the protovalidate
// annotation; the role is validated here because it is an application
// vocabulary, not a string constraint.
func (s *UserService) CreateUser(ctx context.Context, req *connect.Request[supervisorv1.CreateUserRequest]) (*connect.Response[supervisorv1.CreateUserResponse], error) {
	caller, err := userContextFromAuth(ctx)
	if err != nil {
		return nil, err
	}
	username := strings.TrimSpace(req.Msg.Username)
	if username == "" {
		return nil, invalidArgument(newViolation(RuleAuthUsernameRequired, "username", "username must not be empty"))
	}
	role := req.Msg.Role
	if !validRole(role) {
		return nil, invalidArgument(newViolation(RuleAuthRoleInvalid, "role", "role must be admin or viewer"))
	}

	// Duplicate check up front for a typed AlreadyExists; the UNIQUE index
	// is the real gate — a concurrent same-name create loses at insert and
	// maps to the same error below.
	if _, err := s.db.GetAdminUserByUsername(ctx, username); err == nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("username %q is already taken", username))
	}

	hashBytes, err := bcrypt.GenerateFromPassword([]byte(req.Msg.Password), s.cfg.BcryptCost)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hashing password: %w", err))
	}

	user, err := s.db.CreateAdminUser(ctx, db.CreateAdminUserParams{
		Username:     username,
		PasswordHash: string(hashBytes),
		Role:         role,
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("username %q is already taken", username))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create user"))
	}

	recordAuthAudit(ctx, s.db, &caller.UserID, ActionAuthUserCreated, authClientIP(ctx), map[string]any{
		"target_username": username,
		"role":            role,
	})

	return connect.NewResponse(&supervisorv1.CreateUserResponse{User: userInfoFromRow(user)}), nil
}

// SetUserRole flips a user's role with the last-admin guard: demoting the
// final admin is structurally impossible (docs/35 §2.3). Self-demote is
// allowed when another admin exists — the caller's live session downgrades
// on their next request (docs/35 §2.1).
func (s *UserService) SetUserRole(ctx context.Context, req *connect.Request[supervisorv1.SetUserRoleRequest]) (*connect.Response[supervisorv1.SetUserRoleResponse], error) {
	caller, err := userContextFromAuth(ctx)
	if err != nil {
		return nil, err
	}
	role := req.Msg.Role
	if !validRole(role) {
		return nil, invalidArgument(newViolation(RuleAuthRoleInvalid, "role", "role must be admin or viewer"))
	}

	s.guardMu.Lock()
	target, err := s.db.GetAdminUserByUsername(ctx, req.Msg.Username)
	if err != nil {
		s.guardMu.Unlock()
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user %q not found", req.Msg.Username))
	}
	if target.Role == RoleAdmin && role == RoleViewer {
		if err := s.guardLastAdmin(ctx, caller, target.Username); err != nil {
			s.guardMu.Unlock()
			return nil, err
		}
	}
	updated, err := s.db.UpdateAdminRole(ctx, db.UpdateAdminRoleParams{Role: role, ID: target.ID})
	s.guardMu.Unlock()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to update role"))
	}

	recordAuthAudit(ctx, s.db, &caller.UserID, ActionAuthRoleChanged, authClientIP(ctx), map[string]any{
		"target_username": target.Username,
		"old_role":        target.Role,
		"new_role":        role,
	})

	return connect.NewResponse(&supervisorv1.SetUserRoleResponse{User: userInfoFromRow(updated)}), nil
}

// guardLastAdmin refuses the pending demotion/deletion when the target is
// the final admin. Callers hold guardMu; the count and the subsequent
// write therefore observe the same state (see guardMu's doc).
func (s *UserService) guardLastAdmin(ctx context.Context, caller *UserContext, targetUsername string) error {
	if targetUsername == caller.Username && caller.Role != RoleAdmin {
		// Unreachable via the RPC surface (the caller is admin-bucket),
		// but a guard must never trust its caller.
		return connect.NewError(connect.CodePermissionDenied, errors.New("admin role required"))
	}
	count, err := s.db.CountAdminRoleUsers(ctx)
	if err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("failed to count admins"))
	}
	if count <= 1 {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot remove the last admin: promote another admin first"))
	}
	return nil
}

// SetUserPassword is the admin reset for a locked-out user (docs/35
// OQ-2): the caller does not supply the target's current password — that
// is the point. Every session of the target except the caller's own is
// revoked, so a stolen old password (or an abandoned browser) dies with
// the reset; the target's passkeys are untouched — they are the user's own
// recovery path (docs/35 §2.3).
func (s *UserService) SetUserPassword(ctx context.Context, req *connect.Request[supervisorv1.SetUserPasswordRequest]) (*connect.Response[supervisorv1.SetUserPasswordResponse], error) {
	caller, err := userContextFromAuth(ctx)
	if err != nil {
		return nil, err
	}
	s.guardMu.Lock()
	target, err := s.db.GetAdminUserByUsername(ctx, req.Msg.Username)
	if err != nil {
		s.guardMu.Unlock()
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user %q not found", req.Msg.Username))
	}
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(req.Msg.Password), s.cfg.BcryptCost)
	if err != nil {
		s.guardMu.Unlock()
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hashing password: %w", err))
	}
	if _, err := s.db.UpdateAdminPassword(ctx, db.UpdateAdminPasswordParams{PasswordHash: string(hashBytes), ID: target.ID}); err != nil {
		s.guardMu.Unlock()
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to update password"))
	}
	// Revoke the target's sessions; the caller's own row survives (it
	// carries a different user_id unless the target is the caller — then
	// the exclusion keeps the admin's session alive, same sweep
	// ChangePassword uses).
	revoked, revokeErr := s.db.DeleteOtherSessionsByUserId(ctx, db.DeleteOtherSessionsByUserIdParams{
		UserID: target.ID,
		ID:     caller.SessionID,
	})
	if revokeErr != nil {
		// Fail soft: the password IS changed; the audit record carries the
		// truth and idle maintenance expires stale rows anyway.
		revoked = 0
	}
	s.guardMu.Unlock()

	details := map[string]any{
		"target_username":  target.Username,
		"revoked_sessions": revoked,
	}
	if revokeErr != nil {
		details["revoked_error"] = revokeErr.Error()
	}
	recordAuthAudit(ctx, s.db, &caller.UserID, ActionAuthPasswordReset, authClientIP(ctx), details)

	return connect.NewResponse(&supervisorv1.SetUserPasswordResponse{Success: true, RevokedSessions: revoked}), nil
}

// DeleteUser removes an account. Sessions and passkeys cascade via the
// schema's FKs; audit rows keep with user_id set NULL and the username in
// event details. Self-delete and last-admin-delete are refused (docs/35
// §2.3).
func (s *UserService) DeleteUser(ctx context.Context, req *connect.Request[supervisorv1.DeleteUserRequest]) (*connect.Response[supervisorv1.DeleteUserResponse], error) {
	caller, err := userContextFromAuth(ctx)
	if err != nil {
		return nil, err
	}
	s.guardMu.Lock()
	target, err := s.db.GetAdminUserByUsername(ctx, req.Msg.Username)
	if err != nil {
		s.guardMu.Unlock()
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user %q not found", req.Msg.Username))
	}
	if target.ID == caller.UserID {
		s.guardMu.Unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot delete your own account: ask another admin"))
	}
	if target.Role == RoleAdmin {
		if err := s.guardLastAdmin(ctx, caller, target.Username); err != nil {
			s.guardMu.Unlock()
			return nil, err
		}
	}
	if err := s.db.DeleteAdminUser(ctx, target.ID); err != nil {
		s.guardMu.Unlock()
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete user"))
	}
	s.guardMu.Unlock()

	recordAuthAudit(ctx, s.db, &caller.UserID, ActionAuthUserDeleted, authClientIP(ctx), map[string]any{
		"target_username": target.Username,
	})

	return connect.NewResponse(&supervisorv1.DeleteUserResponse{Success: true}), nil
}
