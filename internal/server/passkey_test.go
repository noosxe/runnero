package server

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/descope/virtualwebauthn"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"

	"github.com/noosxe/runnero/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// ---
// Test doubles: an in-memory PasskeyStore plus an AuthDatabase stand-in.
// The WebAuthn crypto itself is exercised for real through the
// descope/virtualwebauthn software authenticator (docs/34 section 9).
// ---

type mockPasskeyStore struct {
	nextID int64 // next row id
	rows   map[int64]db.WebauthnCredential
}

func newMockPasskeyStore() *mockPasskeyStore {
	return &mockPasskeyStore{rows: make(map[int64]db.WebauthnCredential)}
}

func (m *mockPasskeyStore) CreateWebauthnCredential(_ context.Context, arg db.CreateWebauthnCredentialParams) (db.WebauthnCredential, error) {
	m.nextID++
	row := db.WebauthnCredential{
		ID:              m.nextID,
		UserID:          arg.UserID,
		Name:            arg.Name,
		CredentialID:    arg.CredentialID,
		PublicKey:       arg.PublicKey,
		Aaguid:          arg.Aaguid,
		AttestationType: arg.AttestationType,
		Transports:      arg.Transports,
		SignCount:       arg.SignCount,
		BackupEligible:  arg.BackupEligible,
		BackupState:     arg.BackupState,
		CloneWarning:    arg.CloneWarning,
		CreatedAt:       time.Now(),
		LastUsedAt:      time.Unix(0, 0),
	}
	m.rows[row.ID] = row
	return row, nil
}

func (m *mockPasskeyStore) GetWebauthnCredentialById(_ context.Context, credentialID []byte) (db.WebauthnCredential, error) {
	for _, row := range m.rows {
		if string(row.CredentialID) == string(credentialID) {
			return row, nil
		}
	}
	return db.WebauthnCredential{}, errors.New("not found")
}

func (m *mockPasskeyStore) GetWebauthnCredentialByIdAndUserId(_ context.Context, arg db.GetWebauthnCredentialByIdAndUserIdParams) (db.WebauthnCredential, error) {
	for _, row := range m.rows {
		if row.ID == arg.ID && row.UserID == arg.UserID {
			return row, nil
		}
	}
	return db.WebauthnCredential{}, errors.New("not found")
}

func (m *mockPasskeyStore) ListWebauthnCredentialsByUserId(_ context.Context, userID int64) ([]db.WebauthnCredential, error) {
	var out []db.WebauthnCredential
	for _, row := range m.rows {
		if row.UserID == userID {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m *mockPasskeyStore) RenameWebauthnCredential(_ context.Context, arg db.RenameWebauthnCredentialParams) (int64, error) {
	for id, row := range m.rows {
		if row.ID == arg.ID && row.UserID == arg.UserID {
			row.Name = arg.Name
			m.rows[id] = row
			return 1, nil
		}
	}
	return 0, nil
}

func (m *mockPasskeyStore) DeleteWebauthnCredentialByIdAndUserId(_ context.Context, arg db.DeleteWebauthnCredentialByIdAndUserIdParams) (int64, error) {
	for id, row := range m.rows {
		if row.ID == arg.ID && row.UserID == arg.UserID {
			delete(m.rows, id)
			return 1, nil
		}
	}
	return 0, nil
}

func (m *mockPasskeyStore) UpdateWebauthnCredentialAssertionState(_ context.Context, arg db.UpdateWebauthnCredentialAssertionStateParams) error {
	for id, row := range m.rows {
		if string(row.CredentialID) == string(arg.CredentialID) {
			row.SignCount = arg.SignCount
			row.BackupEligible = arg.BackupEligible
			row.BackupState = arg.BackupState
			row.LastUsedAt = arg.LastUsedAt
			m.rows[id] = row
			return nil
		}
	}
	return errors.New("not found")
}

func (m *mockPasskeyStore) SetWebauthnCredentialCloneWarning(_ context.Context, credentialID []byte) error {
	for id, row := range m.rows {
		if string(row.CredentialID) == string(credentialID) {
			row.CloneWarning = 1
			m.rows[id] = row
			return nil
		}
	}
	return errors.New("not found")
}

// passkeyFixture bundles a service with a working WebAuthn engine, a
// seeded admin user with a known password, and a virtual authenticator
// holding one enrolled credential.
type passkeyFixture struct {
	t        *testing.T
	svc      *AuthService
	passDB   *mockPasskeyStore
	authDB   *mockAuthDB
	rp       virtualwebauthn.RelyingParty
	auth     virtualwebauthn.Authenticator
	cred     virtualwebauthn.Credential
	userID   int64
	password string
	ctx      context.Context
}

func newPasskeyFixture(t *testing.T, withCredential bool) *passkeyFixture {
	t.Helper()
	password := "super-secret-password-123"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hashing password: %v", err)
	}
	adminUser := db.AdminUser{ID: 7, Username: "admin", PasswordHash: string(hash), Role: "admin"}

	passDB := newMockPasskeyStore()
	// mockAuthDB.GetAdminUserById returns its stored user, so the
	// discoverable-credential handler resolves the admin row through it.
	authDB := &mockAuthDB{user: adminUser}

	waCfg := &WebAuthnConfig{RPID: "localhost", RPDisplayName: DefaultWebAuthnRPDisplayName, Origins: []string{"http://localhost:8090"}}
	svc := NewAuthService(authDB, SessionConfig{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour, BcryptCost: bcrypt.MinCost}, waCfg)
	// Inject the in-memory passkey store in place of the real database.
	svc.passkeyStore = passDB

	f := &passkeyFixture{
		t:        t,
		svc:      svc,
		passDB:   passDB,
		authDB:   authDB,
		rp:       virtualwebauthn.RelyingParty{ID: "localhost", Name: DefaultWebAuthnRPDisplayName, Origin: "http://localhost:8090"},
		userID:   adminUser.ID,
		password: password,
		ctx:      withTestUser(context.Background(), adminUser.ID, adminUser.Username),
	}

	if withCredential {
		f.auth = virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{
			UserHandle: adminUserWebAuthnID(adminUser.ID),
		})
		f.cred = virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
		f.auth.AddCredential(f.cred)
	}
	return f
}

// withTestUser builds a request context carrying an authenticated user,
// mirroring what the auth interceptor injects on session-authenticated
// procedures.
func withTestUser(ctx context.Context, id int64, username string) context.Context {
	return WithUserContext(ctx, &UserContext{UserID: id, Username: username, Role: "admin"})
}

// adminUserWebAuthnID mirrors passkeyUser.WebAuthnID for the virtual
// authenticator's UserHandle.
func adminUserWebAuthnID(id int64) []byte {
	out := make([]byte, 8)
	for i := 0; i < 8; i++ {
		out[8-1-i] = byte(id >> (8 * i))
	}
	return out
}

// enroll performs the full enrollment ceremony for the fixture's
// authenticator and returns the stored row.
func (f *passkeyFixture) enroll() db.WebauthnCredential {
	f.t.Helper()
	begin, err := f.svc.BeginPasskeyEnrollment(f.ctx, connect.NewRequest(&supervisorv1.BeginPasskeyEnrollmentRequest{
		CurrentPassword: f.password,
	}))
	if err != nil {
		f.t.Fatalf("BeginPasskeyEnrollment: %v", err)
	}
	opts, err := virtualwebauthn.ParseAttestationOptions(string(begin.Msg.PublicKeyOptionsJson))
	if err != nil {
		f.t.Fatalf("parsing attestation options: %v", err)
	}
	attestation := virtualwebauthn.CreateAttestationResponse(f.rp, f.auth, f.cred, *opts)
	finish, err := f.svc.FinishPasskeyEnrollment(f.ctx, connect.NewRequest(&supervisorv1.FinishPasskeyEnrollmentRequest{
		AttestationResponseJson: []byte(attestation),
		Name:                    "test-key",
	}))
	if err != nil {
		f.t.Fatalf("FinishPasskeyEnrollment: %v", err)
	}
	row, ok := f.passDB.rows[finish.Msg.Passkey.Id]
	if !ok {
		f.t.Fatalf("credential row %d not stored", finish.Msg.Passkey.Id)
	}
	return row
}

// assert performs a full passwordless login with the fixture's
// authenticator, using the stored row's counter semantics via the
// authenticator credential counter.
func (f *passkeyFixture) assertLogin() (*connect.Response[supervisorv1.LoginResponse], error) {
	f.t.Helper()
	begin, err := f.svc.BeginPasskeyLogin(context.Background(), connect.NewRequest(&supervisorv1.BeginPasskeyLoginRequest{}))
	if err != nil {
		return nil, err
	}
	opts, perr := virtualwebauthn.ParseAssertionOptions(string(begin.Msg.PublicKeyOptionsJson))
	if perr != nil {
		f.t.Fatalf("parsing assertion options: %v", perr)
	}
	assertion := virtualwebauthn.CreateAssertionResponse(f.rp, f.auth, f.cred, *opts)
	return f.svc.FinishPasskeyLogin(context.Background(), connect.NewRequest(&supervisorv1.FinishPasskeyLoginRequest{
		AssertionResponseJson: []byte(assertion),
	}))
}

// ---
// Tests
// ---

func TestPasskeyUnconfiguredFailsClosed(t *testing.T) {
	svc := NewAuthService(&mockAuthDB{user: db.AdminUser{ID: 1, Username: "admin", Role: "admin"}}, SessionConfig{}, nil)
	if svc.passkeyReady() {
		t.Fatal("service must not be passkey-ready without configuration")
	}
	ctx := withTestUser(context.Background(), 1, "admin")

	if _, err := svc.BeginPasskeyEnrollment(ctx, connect.NewRequest(&supervisorv1.BeginPasskeyEnrollmentRequest{CurrentPassword: "x"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("BeginPasskeyEnrollment code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
	if _, err := svc.FinishPasskeyEnrollment(ctx, connect.NewRequest(&supervisorv1.FinishPasskeyEnrollmentRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("FinishPasskeyEnrollment code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
	if _, err := svc.ListPasskeys(ctx, connect.NewRequest(&supervisorv1.ListPasskeysRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("ListPasskeys code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
	if _, err := svc.RenamePasskey(ctx, connect.NewRequest(&supervisorv1.RenamePasskeyRequest{Id: 1, Name: "x"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("RenamePasskey code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
	if _, err := svc.DeletePasskey(ctx, connect.NewRequest(&supervisorv1.DeletePasskeyRequest{Id: 1})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("DeletePasskey code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
	if _, err := svc.BeginPasskeyLogin(context.Background(), connect.NewRequest(&supervisorv1.BeginPasskeyLoginRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("BeginPasskeyLogin code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
	if _, err := svc.FinishPasskeyLogin(context.Background(), connect.NewRequest(&supervisorv1.FinishPasskeyLoginRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("FinishPasskeyLogin code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
}

func TestPasskeyBeginEnrollmentRequiresCurrentPassword(t *testing.T) {
	f := newPasskeyFixture(t, true)
	_, err := f.svc.BeginPasskeyEnrollment(f.ctx, connect.NewRequest(&supervisorv1.BeginPasskeyEnrollmentRequest{CurrentPassword: "wrong-password"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument (violation)", connect.CodeOf(err))
	}
	// No ceremony may be minted on a failed re-check.
	if len(f.svc.passkeys.items) != 0 {
		t.Fatalf("ceremony store must stay empty after a failed re-check, has %d", len(f.svc.passkeys.items))
	}
}

func TestPasskeyEnrollmentStoresCredential(t *testing.T) {
	f := newPasskeyFixture(t, true)
	row := f.enroll()
	if row.UserID != f.userID {
		t.Fatalf("user_id = %d, want %d", row.UserID, f.userID)
	}
	if row.Name != "test-key" {
		t.Fatalf("name = %q, want test-key", row.Name)
	}
	if len(row.PublicKey) == 0 || len(row.CredentialID) == 0 {
		t.Fatal("public key and credential id must be stored")
	}
	if row.BackupEligible != 0 || row.CloneWarning != 0 {
		t.Fatalf("flags = BE %d CW %d, want 0/0", row.BackupEligible, row.CloneWarning)
	}
	// Discovery-ready: the row must resolve via the credential-id lookup.
	if _, err := f.passDB.GetWebauthnCredentialById(context.Background(), row.CredentialID); err != nil {
		t.Fatalf("credential-id lookup failed: %v", err)
	}
}

func TestPasskeyPasswordlessLoginIssuesSession(t *testing.T) {
	f := newPasskeyFixture(t, true)
	f.enroll()

	resp, err := f.assertLogin()
	if err != nil {
		t.Fatalf("passkey login: %v", err)
	}
	if !resp.Msg.Success || resp.Msg.Username != "admin" {
		t.Fatalf("login response = %+v", resp.Msg)
	}
	if len(resp.Header().Get("Set-Cookie")) == 0 {
		t.Fatal("session cookie must be issued through the shared issuance path")
	}
	// Audit carries the method marker and no credential material.
	found := false
	for _, a := range f.authDB.audit {
		if a.Action == ActionAuthLoginSuccess {
			found = true
			if !strings.Contains(a.Details.String, `"method":"passkey"`) {
				t.Fatalf("login_success details = %s, want method passkey", a.Details.String)
			}
			if strings.Contains(a.Details.String, "challenge") {
				t.Fatal("login_success details must not carry challenge material")
			}
		}
	}
	if !found {
		t.Fatal("login_success audit row missing")
	}
}

func TestPasskeySingleUseChallengeAndUnknownCredential(t *testing.T) {
	f := newPasskeyFixture(t, true)
	f.enroll()

	begin, err := f.svc.BeginPasskeyLogin(context.Background(), connect.NewRequest(&supervisorv1.BeginPasskeyLoginRequest{}))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	opts, _ := virtualwebauthn.ParseAssertionOptions(string(begin.Msg.PublicKeyOptionsJson))
	assertion := virtualwebauthn.CreateAssertionResponse(f.rp, f.auth, f.cred, *opts)

	req := connect.NewRequest(&supervisorv1.FinishPasskeyLoginRequest{AssertionResponseJson: []byte(assertion)})
	if _, err := f.svc.FinishPasskeyLogin(context.Background(), req); err != nil {
		t.Fatalf("first assertion must succeed: %v", err)
	}
	// Replay: same challenge again — the ceremony was consumed.
	if _, err := f.svc.FinishPasskeyLogin(context.Background(), req); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("replayed assertion code = %v, want Unauthenticated", connect.CodeOf(err))
	}
	// Unknown credential: a foreign authenticator's assertion.
	other := virtualwebauthn.NewAuthenticator()
	otherCred := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	other.AddCredential(otherCred)
	begin2, err := f.svc.BeginPasskeyLogin(context.Background(), connect.NewRequest(&supervisorv1.BeginPasskeyLoginRequest{}))
	if err != nil {
		t.Fatalf("begin 2: %v", err)
	}
	opts2, _ := virtualwebauthn.ParseAssertionOptions(string(begin2.Msg.PublicKeyOptionsJson))
	bad := virtualwebauthn.CreateAssertionResponse(f.rp, other, otherCred, *opts2)
	if _, err := f.svc.FinishPasskeyLogin(context.Background(), connect.NewRequest(&supervisorv1.FinishPasskeyLoginRequest{AssertionResponseJson: []byte(bad)})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unknown credential code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

func TestPasskeyAssertionWithoutUserVerificationRejected(t *testing.T) {
	f := newPasskeyFixture(t, true)
	f.enroll()

	// Flip the authenticator to UV-less for the assertion only: with
	// UV=required at registration, an UV-less ATTESTATION is already
	// rejected (docs/34 section 3.7), so the bypass attempt must target
	// the assertion path.
	f.auth.Options.UserNotVerified = true

	if _, err := f.assertLogin(); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("UV-less assertion code = %v, want Unauthenticated", connect.CodeOf(err))
	}
	// The audit reason must be uv_missing.
	last := f.authDB.audit[len(f.authDB.audit)-1]
	if last.Action != ActionAuthPasskeyAssertionFailed || !strings.Contains(last.Details.String, `"reason":"uv_missing"`) {
		t.Fatalf("audit = %s %s, want assertion_failed/uv_missing", last.Action, last.Details.String)
	}
}

func TestPasskeyCounterRegressionFlagsAndRejects(t *testing.T) {
	f := newPasskeyFixture(t, true)
	row := f.enroll()

	// Drive the counter up: the first assertion stores it.
	f.cred.Counter = 100
	if _, err := f.assertLogin(); err != nil {
		t.Fatalf("counter-100 login: %v", err)
	}
	stored, _ := f.passDB.GetWebauthnCredentialById(context.Background(), row.CredentialID)
	if stored.SignCount != 100 {
		t.Fatalf("stored sign_count = %d, want 100", stored.SignCount)
	}

	// Roll the virtual authenticator's counter below the stored value:
	// the next assertion is a probable clone (docs/34 section 3.8).
	f.cred.Counter = 50
	if _, err := f.assertLogin(); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("regressed assertion code = %v, want Unauthenticated", connect.CodeOf(err))
	}
	flagged, _ := f.passDB.GetWebauthnCredentialById(context.Background(), row.CredentialID)
	if flagged.CloneWarning != 1 {
		t.Fatal("clone_warning flag must be set")
	}
	// Even a fresh assertion with a higher counter is refused: flagged
	// credentials are dead until removed (docs/34 section 3.8).
	f.cred.Counter = 500
	if _, err := f.assertLogin(); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("flagged credential assertion code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

func TestPasskeyCounterlessAuthenticatorAllowed(t *testing.T) {
	f := newPasskeyFixture(t, true)
	f.enroll()
	// Counter stays 0 on both sides: 0<->0 is the counterless exemption.
	for i := 0; i < 3; i++ {
		if _, err := f.assertLogin(); err != nil {
			t.Fatalf("counterless assertion %d: %v", i+1, err)
		}
	}
}

func TestPasskeyLoginPoolBounded(t *testing.T) {
	f := newPasskeyFixture(t, false)
	for i := 0; i < passkeyLoginPoolCap; i++ {
		if _, err := f.svc.BeginPasskeyLogin(context.Background(), connect.NewRequest(&supervisorv1.BeginPasskeyLoginRequest{})); err != nil {
			t.Fatalf("begin %d: %v", i+1, err)
		}
	}
	if _, err := f.svc.BeginPasskeyLogin(context.Background(), connect.NewRequest(&supervisorv1.BeginPasskeyLoginRequest{})); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("pool overflow code = %v, want ResourceExhausted", connect.CodeOf(err))
	}
	if len(f.svc.passkeys.items) > passkeyLoginPoolCap {
		t.Fatalf("ceremony store grew past the cap: %d", len(f.svc.passkeys.items))
	}
}

func TestPasskeyCeremonyExpiry(t *testing.T) {
	f := newPasskeyFixture(t, true)
	f.enroll()

	// Begin a ceremony, then expire it before the assertion consumes it.
	begin, err := f.svc.BeginPasskeyLogin(context.Background(), connect.NewRequest(&supervisorv1.BeginPasskeyLoginRequest{}))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	f.svc.passkeys.mu.Lock()
	for _, c := range f.svc.passkeys.items {
		c.expiresAt = time.Now().Add(-time.Second)
	}
	f.svc.passkeys.mu.Unlock()

	opts, perr := virtualwebauthn.ParseAssertionOptions(string(begin.Msg.PublicKeyOptionsJson))
	if perr != nil {
		t.Fatalf("parsing assertion options: %v", perr)
	}
	assertion := virtualwebauthn.CreateAssertionResponse(f.rp, f.auth, f.cred, *opts)
	_, err = f.svc.FinishPasskeyLogin(context.Background(), connect.NewRequest(&supervisorv1.FinishPasskeyLoginRequest{
		AssertionResponseJson: []byte(assertion),
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expired ceremony code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

func TestPasskeyEnrollmentCeremonyReplacedPerUser(t *testing.T) {
	f := newPasskeyFixture(t, true)
	if _, err := f.svc.BeginPasskeyEnrollment(f.ctx, connect.NewRequest(&supervisorv1.BeginPasskeyEnrollmentRequest{CurrentPassword: f.password})); err != nil {
		t.Fatalf("begin 1: %v", err)
	}
	if _, err := f.svc.BeginPasskeyEnrollment(f.ctx, connect.NewRequest(&supervisorv1.BeginPasskeyEnrollmentRequest{CurrentPassword: f.password})); err != nil {
		t.Fatalf("begin 2: %v", err)
	}
	if len(f.svc.passkeys.items) != 1 {
		t.Fatalf("one live ceremony per user, have %d", len(f.svc.passkeys.items))
	}
}

func TestPasskeyAnonymousFailuresRateLimitBegin(t *testing.T) {
	f := newPasskeyFixture(t, true)
	f.enroll()

	// Five anonymous Finish failures lock the anonymous key.
	for i := 0; i < rateFailureLimit; i++ {
		if _, err := f.svc.FinishPasskeyLogin(context.Background(), connect.NewRequest(&supervisorv1.FinishPasskeyLoginRequest{AssertionResponseJson: []byte("not-json")})); err == nil {
			t.Fatal("garbage assertion must fail")
		}
	}
	if _, err := f.svc.BeginPasskeyLogin(context.Background(), connect.NewRequest(&supervisorv1.BeginPasskeyLoginRequest{})); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("Begin under lockout code = %v, want ResourceExhausted", connect.CodeOf(err))
	}
}

func TestPasskeyPasswordFallbackUnaffected(t *testing.T) {
	f := newPasskeyFixture(t, true)
	f.enroll()

	// The password path keeps working with passkeys enrolled — the two
	// paths are independent complete logins (docs/34 section 3.3).
	req := connect.NewRequest(&supervisorv1.LoginRequest{Username: "admin", Password: f.password})
	if _, err := f.svc.Login(context.Background(), req); err != nil {
		t.Fatalf("password login with passkeys enrolled: %v", err)
	}
}

func TestPasskeyListRenameDeleteOwnership(t *testing.T) {
	f := newPasskeyFixture(t, true)
	row := f.enroll()

	list, err := f.svc.ListPasskeys(f.ctx, connect.NewRequest(&supervisorv1.ListPasskeysRequest{}))
	if err != nil {
		t.Fatalf("ListPasskeys: %v", err)
	}
	if len(list.Msg.Passkeys) != 1 || list.Msg.Passkeys[0].Name != "test-key" {
		t.Fatalf("list = %+v", list.Msg.Passkeys)
	}

	if _, err := f.svc.RenamePasskey(f.ctx, connect.NewRequest(&supervisorv1.RenamePasskeyRequest{Id: row.ID, Name: "yubikey"})); err != nil {
		t.Fatalf("RenamePasskey: %v", err)
	}
	if got, _ := f.passDB.GetWebauthnCredentialByIdAndUserId(context.Background(), db.GetWebauthnCredentialByIdAndUserIdParams{ID: row.ID, UserID: f.userID}); got.Name != "yubikey" {
		t.Fatalf("name = %q, want yubikey", got.Name)
	}

	if _, err := f.svc.DeletePasskey(f.ctx, connect.NewRequest(&supervisorv1.DeletePasskeyRequest{Id: row.ID})); err != nil {
		t.Fatalf("DeletePasskey: %v", err)
	}
	if len(f.passDB.rows) != 0 {
		t.Fatal("row must be deleted")
	}
	// Foreign/unknown id answers NotFound.
	if _, err := f.svc.RenamePasskey(f.ctx, connect.NewRequest(&supervisorv1.RenamePasskeyRequest{Id: row.ID, Name: "x"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("rename missing code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := f.svc.DeletePasskey(f.ctx, connect.NewRequest(&supervisorv1.DeletePasskeyRequest{Id: row.ID})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("delete missing code = %v, want NotFound", connect.CodeOf(err))
	}
}

func TestPasskeyAuditLeakage(t *testing.T) {
	f := newPasskeyFixture(t, true)
	row := f.enroll()
	// A failed attempt for good measure.
	_, _ = f.svc.FinishPasskeyLogin(context.Background(), connect.NewRequest(&supervisorv1.FinishPasskeyLoginRequest{AssertionResponseJson: []byte("garbage")}))

	secretFragments := []string{
		base64.RawURLEncoding.EncodeToString(row.CredentialID),
		string(row.CredentialID),
		base64.StdEncoding.EncodeToString(row.PublicKey),
	}
	for _, a := range f.authDB.audit {
		for _, frag := range secretFragments {
			if len(frag) > 8 && strings.Contains(a.Details.String, frag) {
				t.Fatalf("audit %s leaks credential material: %s", a.Action, a.Details.String)
			}
		}
		if a.Action == ActionAuthLoginSuccess || strings.HasPrefix(a.Action, "auth.passkey.") {
			if strings.Contains(strings.ToLower(a.Details.String), "challenge") {
				t.Fatalf("audit %s mentions challenge material", a.Action)
			}
		}
	}
}
