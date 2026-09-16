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

// TestAuthenticateViewerBucketReserved guards the reserved bucket: nothing
// classifies into it today, and the constant's position in the bucket order
// is what the coverage test's "exactly once" reasoning relies on.
func TestAuthenticateViewerBucketReserved(t *testing.T) {
	for procedure, bucket := range procedureRoles {
		if bucket == bucketViewer {
			t.Errorf("procedure %q maps to the reserved viewer bucket: assigning viewer roles is out of scope until the observer-users design (docs/32 §2.3)", procedure)
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
