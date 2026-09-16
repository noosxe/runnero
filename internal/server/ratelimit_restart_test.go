package server_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/server"
)

// RUN-238 (docs/32 §4.2): the brute-force lockout must survive a supervisor
// restart. Failed logins are written through to login_rate_failures and a
// fresh AuthService over the same database restores the in-window rows on
// boot - previously a restart reset every attacker's backoff.

func TestLoginLockoutSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)

	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)

	login := func(c supervisorv1connect.AuthServiceClient, password string) error {
		_, err := c.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
			Username: "admin",
			Password: password,
		}))
		return err
	}

	// Five wrong passwords lock the key with the first backoff step.
	for i := 0; i < 5; i++ {
		if err := login(client, "totally-wrong-password"); err == nil {
			t.Fatalf("login %d succeeded with a wrong password", i+1)
		}
	}
	if err := login(client, "totally-wrong-password"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("6th attempt code = %v, want ResourceExhausted", connect.CodeOf(err))
	}

	// Restart: a brand-new server over the same database. The in-memory
	// limiter is gone; the lockout must come back from the table.
	srv := server.New(server.Options{
		Port:    8080,
		AuthDB:  database,
		Session: testSessionConfig(),
	})
	ts2 := httptest.NewServer(srv.Handler())
	t.Cleanup(ts2.Close)
	reborn := supervisorv1connect.NewAuthServiceClient(ts2.Client(), ts2.URL)

	if err := login(reborn, "totally-wrong-password"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("after restart code = %v, want ResourceExhausted (lockout lost)", connect.CodeOf(err))
	}

	// Even the correct password is refused while the lockout holds - the
	// backoff gate sits in front of credential verification (docs/32 §4.2).
	if err := login(reborn, "super-secret-password-123"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("correct password after restart code = %v, want ResourceExhausted", connect.CodeOf(err))
	}
}

// The success path must clear the durable rows too, or the next restart
// would resurrect a lockout for a key that legitimately logged in.
func TestLoginSuccessClearsPersistedFailures(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)

	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)

	// One failed login writes a row; a successful login from the same
	// key must delete it again.
	if _, err := client.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "wrong-password",
	})); err == nil {
		t.Fatal("login with wrong password succeeded")
	}
	if _, err := client.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "super-secret-password-123",
	})); err != nil {
		t.Fatalf("login with correct password failed: %v", err)
	}

	rows, err := database.ListLoginRateFailuresSince(ctx, time.Now().Add(-15*time.Minute))
	if err != nil {
		t.Fatalf("ListLoginRateFailuresSince failed: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("login_rate_failures rows = %d, want 0 after successful login", len(rows))
	}
}
