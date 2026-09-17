package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	supervisorv1connect "github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
)

// Tests in this file live in the internal package: they drive the
// interceptor's authenticate path directly, which the fail-closed role
// matrix (docs/32 §2.3) needs — unclassified procedures and non-admin users
// cannot be produced through the public surface.

// mockAuthDB is a scripted AuthDatabase for interceptor-level tests.
type mockAuthDB struct {
	session db.Session
	user    db.AdminUser
	audit   []db.CreateAuditLogParams
}

func (m *mockAuthDB) CountAdminUsers(context.Context) (int64, error) { return 1, nil }
func (m *mockAuthDB) CreateAdminUser(_ context.Context, arg db.CreateAdminUserParams) (db.AdminUser, error) {
	return db.AdminUser{ID: 1, Username: arg.Username, PasswordHash: arg.PasswordHash, Role: "admin"}, nil
}
func (m *mockAuthDB) GetAdminUserByUsername(context.Context, string) (db.AdminUser, error) {
	return m.user, nil
}
func (m *mockAuthDB) ListAdminUsers(context.Context) ([]db.AdminUser, error) {
	return []db.AdminUser{m.user}, nil
}
func (m *mockAuthDB) CountAdminRoleUsers(context.Context) (int64, error) { return 1, nil }
func (m *mockAuthDB) UpdateAdminRole(_ context.Context, arg db.UpdateAdminRoleParams) (db.AdminUser, error) {
	m.user.Role = arg.Role
	return m.user, nil
}
func (m *mockAuthDB) DeleteAdminUser(context.Context, int64) error { return nil }
func (m *mockAuthDB) GetAdminUserById(_ context.Context, id int64) (db.AdminUser, error) {
	return m.user, nil
}

func (m *mockAuthDB) UpdateAdminPassword(_ context.Context, arg db.UpdateAdminPasswordParams) (db.AdminUser, error) {
	m.user.PasswordHash = arg.PasswordHash
	return m.user, nil
}
func (m *mockAuthDB) CreateSession(_ context.Context, arg db.CreateSessionParams) (db.Session, error) {
	return db.Session{ID: 1, UserID: arg.UserID, TokenHash: arg.TokenHash, ExpiresAt: arg.ExpiresAt, AbsoluteExpiresAt: arg.AbsoluteExpiresAt}, nil
}
func (m *mockAuthDB) GetSessionByTokenHash(_ context.Context, tokenHash string) (db.Session, error) {
	if tokenHash != m.session.TokenHash {
		return db.Session{}, errors.New("no such session")
	}
	return m.session, nil
}
func (m *mockAuthDB) TouchSession(context.Context, db.TouchSessionParams) error { return nil }
func (m *mockAuthDB) DeleteSessionByTokenHash(context.Context, string) error    { return nil }
func (m *mockAuthDB) ListSessionsByUserId(context.Context, int64) ([]db.Session, error) {
	return []db.Session{m.session}, nil
}
func (m *mockAuthDB) DeleteSessionByIdAndUserId(context.Context, db.DeleteSessionByIdAndUserIdParams) (int64, error) {
	return 1, nil
}
func (m *mockAuthDB) DeleteOtherSessionsByUserId(context.Context, db.DeleteOtherSessionsByUserIdParams) (int64, error) {
	return 0, nil
}
func (m *mockAuthDB) CreateAuditLog(_ context.Context, arg db.CreateAuditLogParams) (db.AuditLog, error) {
	m.audit = append(m.audit, arg)
	return db.AuditLog{ID: int64(len(m.audit))}, nil
}

// TestAuthenticateFailsClosedOnUnclassifiedProcedure proves an unclassified
// procedure is rejected with CodeInternal before any cookie handling
// (docs/32 §2.3): shipping a new RPC without a matrix entry cannot open it
// (it is blocked) or silently close it (the coverage test forces entry).
func TestAuthenticateFailsClosedOnUnclassifiedProcedure(t *testing.T) {
	interceptor := &AuthInterceptor{authDB: &mockAuthDB{}, cfg: SessionConfig{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour, SecureMode: "auto"}}

	_, _, err := interceptor.authenticate(context.Background(), http.Header{}, "/supervisor.v1.FutureService/NotYetShipped")
	if err == nil {
		t.Fatal("unclassified procedure accepted")
	}
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Fatalf("code = %v, want CodeInternal", got)
	}
}

// TestAuthenticateAdminRoleEnforcement drives the denial path with a valid
// session owned by a non-admin user (docs/32 §2.3): PermissionDenied plus an
// audit entry. Unreachable through the public surface today (the only user
// is the bootstrap admin); the structure must already hold.
func TestAuthenticateAdminRoleEnforcement(t *testing.T) {
	mock := &mockAuthDB{
		session: db.Session{ID: 7, UserID: 1, TokenHash: HashToken("tok"), ExpiresAt: time.Now().Add(time.Hour), AbsoluteExpiresAt: time.Now().Add(24 * time.Hour)},
		user:    db.AdminUser{ID: 1, Username: "observer", Role: "viewer"},
	}
	interceptor := &AuthInterceptor{authDB: mock, cfg: SessionConfig{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour, SecureMode: "auto"}}

	header := http.Header{}
	header.Set("Cookie", "session_token=tok")
	_, _, err := interceptor.authenticate(context.Background(), header, "/supervisor.v1.PoolService/CreatePool")
	if err == nil {
		t.Fatal("non-admin user passed an admin-bucket procedure")
	}
	if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
		t.Fatalf("code = %v, want CodePermissionDenied", got)
	}
	if len(mock.audit) != 1 || mock.audit[0].Action != "auth.access_denied" {
		t.Fatalf("expected one auth.access_denied audit row, got %+v", mock.audit)
	}
}

// TestViewerBucketClassification pins the RUN-236 reclassification
// (docs/35 §2.2): the self-service surface (rows SQL-scoped to the calling
// user) and the read-only observability surface are viewer-bucket; writes,
// secrets, supervisor internals, and user management stay admin. If this
// test fails after a deliberate reclassification, update both it and
// docs/35 §2.2 in the same change - the matrix and the design must never
// drift apart.
func TestViewerBucketClassification(t *testing.T) {
	viewerProcedures := []string{
		// Self-service: every one of these is scoped to the calling user in
		// SQL (docs/32 §3.5), so a viewer can touch only their own account.
		"/supervisor.v1.AuthService/GetSession",
		"/supervisor.v1.AuthService/Logout",
		"/supervisor.v1.AuthService/ListSessions",
		"/supervisor.v1.AuthService/RevokeSession",
		"/supervisor.v1.AuthService/RevokeOtherSessions",
		"/supervisor.v1.AuthService/ChangePassword",
		"/supervisor.v1.AuthService/BeginPasskeyEnrollment",
		"/supervisor.v1.AuthService/FinishPasskeyEnrollment",
		"/supervisor.v1.AuthService/ListPasskeys",
		"/supervisor.v1.AuthService/RenamePasskey",
		"/supervisor.v1.AuthService/DeletePasskey",
		// Read-only observability.
		"/supervisor.v1.PoolService/ListPools",
		"/supervisor.v1.PoolService/WatchPools",
		"/supervisor.v1.PoolService/ListRunners",
		"/supervisor.v1.PoolService/WatchRunners",
		"/supervisor.v1.AnalyticsService/GetJobHistory",
		"/supervisor.v1.AnalyticsService/GetJobRecord",
		"/supervisor.v1.AnalyticsService/GetSystemStats",
		"/supervisor.v1.AnalyticsService/WatchDashboard",
		"/supervisor.v1.LogService/GetRunnerLogs",
		"/supervisor.v1.LogService/StreamRunnerLogs",
		"/supervisor.v1.RenovateService/GetRenovateStatus",
		"/supervisor.v1.RenovateService/ListRenovateHistory",
		"/supervisor.v1.ImageUpdateService/ListImageUpdates",
	}
	for _, procedure := range viewerProcedures {
		if got := procedureRoles[procedure]; got != bucketViewer {
			t.Errorf("procedure %q: got bucket %v, want viewer", procedure, got)
		}
	}

	adminProcedures := []string{
		// Writes, secrets, supervisor internals, user management.
		"/supervisor.v1.PoolService/CreatePool",
		"/supervisor.v1.PoolService/UpdatePool",
		"/supervisor.v1.PoolService/DeletePool",
		"/supervisor.v1.PoolService/TerminateRunner",
		"/supervisor.v1.PoolService/DiscoverTargets",
		"/supervisor.v1.AuthProfileService/ListAuthProfiles",
		"/supervisor.v1.AuthProfileService/CreateAuthProfile",
		"/supervisor.v1.AuthProfileService/UpdateAuthProfile",
		"/supervisor.v1.AuthProfileService/DeleteAuthProfile",
		"/supervisor.v1.OnboardingService/GetAppSettings",
		"/supervisor.v1.OnboardingService/SetAppSetting",
		"/supervisor.v1.OnboardingService/CompleteOnboarding",
		"/supervisor.v1.LogService/ListSupervisorLogs",
		"/supervisor.v1.LogService/StreamSupervisorLog",
		"/supervisor.v1.LogService/ListRemovalRecords",
		"/supervisor.v1.RenovateService/TriggerRenovateRun",
		"/supervisor.v1.ImageUpdateService/CheckImageUpdate",
		"/supervisor.v1.ImageUpdateService/PullImage",
		"/supervisor.v1.ImageUpdateService/DismissImageUpdate",
		"/supervisor.v1.UserService/ListUsers",
		"/supervisor.v1.UserService/CreateUser",
		"/supervisor.v1.UserService/SetUserRole",
		"/supervisor.v1.UserService/SetUserPassword",
		"/supervisor.v1.UserService/DeleteUser",
	}
	for _, procedure := range adminProcedures {
		if got := procedureRoles[procedure]; got != bucketAdmin {
			t.Errorf("procedure %q: got bucket %v, want admin", procedure, got)
		}
	}
}

// TestRoleMatrixCoverage is the fail-closed proof for the procedure-role
// matrix (docs/32 section 2.3, guide section 6.1): every RPC declared in
// proto/api.proto is classified, and the matrix holds no stale entries. The
// enumeration walks the generated protobuf file descriptor, so a newly
// added RPC fails this test until it is classified — a new endpoint cannot
// ship silently public or silently blocked.
func TestRoleMatrixCoverage(t *testing.T) {
	declared := map[string]bool{}
	file := supervisorv1.File_api_proto
	for i := 0; i < file.Services().Len(); i++ {
		svc := file.Services().Get(i)
		for j := 0; j < svc.Methods().Len(); j++ {
			procedure := fmt.Sprintf("/%s/%s", svc.FullName(), svc.Methods().Get(j).Name())
			declared[procedure] = true

			if _, ok := procedureRoles[procedure]; !ok {
				t.Errorf("procedure %q is declared in the proto but missing from the role matrix: classify it in procedureRoles", procedure)
			}
		}
	}

	for procedure := range procedureRoles {
		if !declared[procedure] {
			t.Errorf("role matrix classifies %q which is no longer declared in the proto: remove the stale entry", procedure)
		}
	}
}

// TestAuthenticateStaleCookieDegradesToAnonymousOnPublicProcedures pins the
// RUN-244 recovery rule (docs/32 §3.3): a presented-but-invalid cookie on a
// bucketPublic procedure degrades to anonymous access instead of failing the
// call. A dead session cookie must not lock the browser out of Login and the
// onboarding status probe — the only self-service recovery path.
func TestAuthenticateStaleCookieDegradesToAnonymousOnPublicProcedures(t *testing.T) {
	mock := &mockAuthDB{
		session: db.Session{ID: 7, UserID: 1, TokenHash: HashToken("valid-tok"), ExpiresAt: time.Now().Add(time.Hour), AbsoluteExpiresAt: time.Now().Add(24 * time.Hour)},
		user:    db.AdminUser{ID: 1, Username: "admin", Role: "admin"},
	}
	interceptor := &AuthInterceptor{authDB: mock, cfg: SessionConfig{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour, SecureMode: "auto"}}

	header := http.Header{}
	header.Set("Cookie", "session_token=stale-dead-token")

	publicProcedures := []string{
		supervisorv1connect.AuthServiceSetupAdminProcedure,
		supervisorv1connect.AuthServiceLoginProcedure,
		supervisorv1connect.OnboardingServiceGetOnboardingStatusProcedure,
	}
	for _, procedure := range publicProcedures {
		ctx, renewal, err := interceptor.authenticate(context.Background(), header, procedure)
		if err != nil {
			t.Fatalf("%s: stale cookie rejected: %v", procedure, err)
		}
		if renewal != "" {
			t.Fatalf("%s: renewal cookie set for a degraded anonymous call", procedure)
		}
		if _, ok := GetUserContext(ctx); ok {
			t.Fatalf("%s: stale cookie produced an authenticated context", procedure)
		}
	}
}

// TestAuthenticateValidCookieStillUpgradesPublicProcedures guards the other
// half of the docs/32 §3.3 contract: a VALID cookie on a public procedure
// still upgrades the context (login page personalization). The RUN-244
// degradation must not swallow it.
func TestAuthenticateValidCookieStillUpgradesPublicProcedures(t *testing.T) {
	mock := &mockAuthDB{
		session: db.Session{ID: 7, UserID: 1, TokenHash: HashToken("valid-tok"), ExpiresAt: time.Now().Add(time.Hour), AbsoluteExpiresAt: time.Now().Add(24 * time.Hour)},
		user:    db.AdminUser{ID: 1, Username: "admin", Role: "admin"},
	}
	interceptor := &AuthInterceptor{authDB: mock, cfg: SessionConfig{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour, SecureMode: "auto"}}

	header := http.Header{}
	header.Set("Cookie", "session_token=valid-tok")

	ctx, _, err := interceptor.authenticate(context.Background(), header, supervisorv1connect.OnboardingServiceGetOnboardingStatusProcedure)
	if err != nil {
		t.Fatalf("valid cookie rejected on public procedure: %v", err)
	}
	user, ok := GetUserContext(ctx)
	if !ok || user.Username != "admin" {
		t.Fatalf("valid cookie did not upgrade the public-procedure context: ok=%v user=%+v", ok, user)
	}
}

// TestAuthenticateStaleCookieStillFailsProtectedBuckets keeps the strict
// fail-closed behavior on protected buckets (docs/32 §2.3): the RUN-244
// public-bucket degradation must not weaken session/admin enforcement.
func TestAuthenticateStaleCookieStillFailsProtectedBuckets(t *testing.T) {
	mock := &mockAuthDB{
		session: db.Session{ID: 7, UserID: 1, TokenHash: HashToken("valid-tok"), ExpiresAt: time.Now().Add(time.Hour), AbsoluteExpiresAt: time.Now().Add(24 * time.Hour)},
		user:    db.AdminUser{ID: 1, Username: "admin", Role: "admin"},
	}
	interceptor := &AuthInterceptor{authDB: mock, cfg: SessionConfig{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour, SecureMode: "auto"}}

	header := http.Header{}
	header.Set("Cookie", "session_token=stale-dead-token")

	protectedProcedures := []string{
		supervisorv1connect.AuthServiceGetSessionProcedure,
		supervisorv1connect.PoolServiceListPoolsProcedure,
	}
	for _, procedure := range protectedProcedures {
		_, _, err := interceptor.authenticate(context.Background(), header, procedure)
		if err == nil {
			t.Fatalf("%s: stale cookie accepted on a protected bucket", procedure)
		}
		if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
			t.Fatalf("%s: code = %v, want CodeUnauthenticated", procedure, got)
		}
	}
}
