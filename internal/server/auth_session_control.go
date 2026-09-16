package server

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Session control surface (docs/32 §3.5): Logout, ListSessions, RevokeSession
// and RevokeOtherSessions. All four operate exclusively on the calling
// user's rows - the ownership guard lives in the SQL (WHERE user_id = ?), so
// a multi-user future cannot silently regress it.

// clearSessionCookieValue returns the Set-Cookie header value that expires
// the session cookie in the browser (docs/32 §3.4).
func (s *AuthService) clearSessionCookieValue(ctx context.Context) string {
	return s.cfg.sessionCookie("", -1, s.secureFor(ctx)).String()
}

// userContextFromAuth resolves the authenticated user context or answers the
// canonical Unauthenticated error. The interceptor guarantees the context for
// every classified procedure, so the miss branch is unreachable defense.
func userContextFromAuth(ctx context.Context) (*UserContext, error) {
	user, ok := GetUserContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("unauthorized: missing session"))
	}
	return user, nil
}

// Logout deletes the caller's current session row and expires the session
// cookie (docs/32 §3.5). Deleting an already-gone row still succeeds: logout
// is idempotent, the client only cares that the cookie dies.
func (s *AuthService) Logout(ctx context.Context, req *connect.Request[supervisorv1.LogoutRequest]) (*connect.Response[supervisorv1.LogoutResponse], error) {
	user, err := userContextFromAuth(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := s.db.DeleteSessionByIdAndUserId(ctx, db.DeleteSessionByIdAndUserIdParams{
		ID:     user.SessionID,
		UserID: user.UserID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete session"))
	}

	recordAuthAudit(ctx, s.db, &user.UserID, ActionAuthLogout, authClientIP(ctx), map[string]any{
		"session_id": user.SessionID,
	})

	res := connect.NewResponse(&supervisorv1.LogoutResponse{Success: true})
	res.Header().Set("Set-Cookie", s.clearSessionCookieValue(ctx))
	return res, nil
}

// ListSessions returns the caller's live sessions with device labels parsed
// from the login user agents and the current-session marker (docs/32 §3.5).
// The response never carries token material.
func (s *AuthService) ListSessions(ctx context.Context, req *connect.Request[supervisorv1.ListSessionsRequest]) (*connect.Response[supervisorv1.ListSessionsResponse], error) {
	user, err := userContextFromAuth(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.ListSessionsByUserId(ctx, user.UserID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list sessions"))
	}

	infos := make([]*supervisorv1.SessionInfo, 0, len(rows))
	for i := range rows {
		sess := rows[i]
		infos = append(infos, &supervisorv1.SessionInfo{
			Id:                sess.ID,
			DeviceLabel:       DeviceLabel(sess.UserAgent),
			CreatedAt:         timestamppb.New(sess.CreatedAt),
			LastSeenAt:        timestamppb.New(sess.LastSeenAt),
			ExpiresAt:         timestamppb.New(sess.ExpiresAt),
			AbsoluteExpiresAt: timestamppb.New(sess.AbsoluteExpiresAt),
			IsCurrent:         sess.ID == user.SessionID,
		})
	}

	return connect.NewResponse(&supervisorv1.ListSessionsResponse{Sessions: infos}), nil
}

// RevokeSession deletes one of the caller's sessions by row id (docs/32
// §3.5). Unknown or foreign ids answer NotFound - the query-level ownership
// guard makes them indistinguishable. Revoking the current session also
// expires the cookie: it is logout by another name.
func (s *AuthService) RevokeSession(ctx context.Context, req *connect.Request[supervisorv1.RevokeSessionRequest]) (*connect.Response[supervisorv1.RevokeSessionResponse], error) {
	user, err := userContextFromAuth(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.DeleteSessionByIdAndUserId(ctx, db.DeleteSessionByIdAndUserIdParams{
		ID:     req.Msg.SessionId,
		UserID: user.UserID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to revoke session"))
	}
	if rows == 0 {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}

	isCurrent := req.Msg.SessionId == user.SessionID
	recordAuthAudit(ctx, s.db, &user.UserID, ActionAuthSessionRevoked, authClientIP(ctx), map[string]any{
		"session_id": req.Msg.SessionId,
		"current":    isCurrent,
	})

	res := connect.NewResponse(&supervisorv1.RevokeSessionResponse{Success: true})
	if isCurrent {
		res.Header().Set("Set-Cookie", s.clearSessionCookieValue(ctx))
	}
	return res, nil
}

// RevokeOtherSessions deletes every caller session except the current one
// (docs/32 §3.5) and reports how many rows it removed.
func (s *AuthService) RevokeOtherSessions(ctx context.Context, req *connect.Request[supervisorv1.RevokeOtherSessionsRequest]) (*connect.Response[supervisorv1.RevokeOtherSessionsResponse], error) {
	user, err := userContextFromAuth(ctx)
	if err != nil {
		return nil, err
	}

	revoked, err := s.db.DeleteOtherSessionsByUserId(ctx, db.DeleteOtherSessionsByUserIdParams{
		UserID: user.UserID,
		ID:     user.SessionID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to revoke other sessions"))
	}

	recordAuthAudit(ctx, s.db, &user.UserID, ActionAuthSessionRevoked, authClientIP(ctx), map[string]any{
		"scope":      "others",
		"revoked":    revoked,
		"session_id": user.SessionID,
	})

	return connect.NewResponse(&supervisorv1.RevokeOtherSessionsResponse{Revoked: revoked}), nil
}
