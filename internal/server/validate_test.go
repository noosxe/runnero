package server_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	validatev1 "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/server"
)

// validationEnv wires a real Connect handler stack (binary codec, auth,
// validation interceptor — the runtime order) over a test DB, authenticates a
// session, and seeds one auth profile for pool foreign keys.
type validationEnv struct {
	ctx           context.Context
	ts            *httptest.Server
	cookie        string
	authProfileID int64
	pools         supervisorv1connect.PoolServiceClient
	auth          supervisorv1connect.AuthServiceClient
}

func setupValidationEnv(t *testing.T) *validationEnv {
	t.Helper()
	ctx := context.Background()
	database := setupTestDB(t)
	stats := newMockStatsProvider()

	srv := server.New(server.Options{
		Port:      8080,
		AuthDB:    database,
		PoolDB:    database,
		PoolStats: stats,
		Session:   testSessionConfig(),
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	authClient := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
	if _, err := authClient.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "password123456",
	})); err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}
	loginRes, err := authClient.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "password123456",
	}))
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	cookie := strings.Split(strings.Split(loginRes.Header().Get("Set-Cookie"), ";")[0], "=")[1]

	authProfile, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:           "validation-auth-profile",
		AuthMethod:     "pat",
		TokenEncrypted: sql.NullString{String: "encrypted-token", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}

	return &validationEnv{
		ctx:           ctx,
		ts:            ts,
		cookie:        cookie,
		authProfileID: authProfile.ID,
		pools:         supervisorv1connect.NewPoolServiceClient(ts.Client(), ts.URL),
		auth:          authClient,
	}
}

// validPool returns a pool payload that passes every rule; table tests mutate
// one field at a time.
func validPool(profileID int64) *supervisorv1.Pool {
	return &supervisorv1.Pool{
		Name:                     "validation-pool",
		Provider:                 "github",
		RepositoryUrl:            "https://github.com/org/repo",
		Scope:                    "repo",
		AuthProfileId:            profileID,
		MinIdleRunners:           1,
		MaxConcurrency:           10,
		RunnerImage:              "ghcr.io/noosxe/runnero:latest",
		AllowDocker:              true,
		CpuLimit:                 "2.0",
		MemoryLimit:              "4G",
		MaxRunnerLifetimeSeconds: 3600,
	}
}

func (e *validationEnv) createPool(t *testing.T, pool *supervisorv1.Pool) error {
	t.Helper()
	req := connect.NewRequest(&supervisorv1.CreatePoolRequest{Pool: pool})
	req.Header().Set("Cookie", "session_token="+e.cookie)
	_, err := e.pools.CreatePool(e.ctx, req)
	return err
}

// violationsFromError extracts the typed buf.validate.Violations detail — the
// structured channel the web will read (docs/30 §5.3, docs/08).
func violationsFromError(t *testing.T, err error) *validatev1.Violations {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var cerr *connect.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("expected connect error, got %T: %v", err, err)
	}
	for _, detail := range cerr.Details() {
		value, derr := detail.Value()
		if derr != nil {
			continue
		}
		if violations, ok := value.(*validatev1.Violations); ok {
			return violations
		}
	}
	t.Fatalf("no buf.validate.Violations detail in error: %v", err)
	return nil
}

func hasRuleID(t *testing.T, err error, ruleID string) {
	t.Helper()
	violations := violationsFromError(t, err)
	for _, violation := range violations.GetViolations() {
		if violation.GetRuleId() == ruleID {
			return
		}
	}
	t.Fatalf("rule_id %q not found in violations: %v", ruleID, violations)
}

func lastFieldName(v *validatev1.Violation) string {
	elements := v.GetField().GetElements()
	if len(elements) == 0 {
		return ""
	}
	return elements[len(elements)-1].GetFieldName()
}

// TestValidationInterceptor_AnnotatedPoolRules walks the class A rules declared
// on supervisorv1.Pool / CreatePoolRequest: each violation must come back as
// CodeInvalidArgument with the expected stable rule id and a structured field
// path (docs/30 §7 phase 0).
func TestValidationInterceptor_AnnotatedPoolRules(t *testing.T) {
	env := setupValidationEnv(t)

	for _, tc := range []struct {
		name          string
		mutate        func(*supervisorv1.Pool)
		wantRuleID    string
		wantFieldName string
	}{
		{"empty name", func(p *supervisorv1.Pool) { p.Name = "" }, "string.min_len", "name"},
		{"non-slug name", func(p *supervisorv1.Pool) { p.Name = "Bad Pool!" }, "string.pattern", "name"},
		{"negative min_idle", func(p *supervisorv1.Pool) { p.MinIdleRunners = -1 }, "int32.gte", "min_idle_runners"},
		{"negative max_concurrency", func(p *supervisorv1.Pool) { p.MaxConcurrency = -1 }, "int32.gte", "max_concurrency"},
		{"negative pids_limit", func(p *supervisorv1.Pool) { p.PidsLimit = -5 }, "int32.gte", "pids_limit"},
		{"negative lifetime", func(p *supervisorv1.Pool) { p.MaxRunnerLifetimeSeconds = -1 }, "int32.gte", "max_runner_lifetime_seconds"},
		{"zero auth_profile_id", func(p *supervisorv1.Pool) { p.AuthProfileId = 0 }, "int64.gt", "auth_profile_id"},
		{"poll interval below range", func(p *supervisorv1.Pool) { p.PollIntervalSeconds = 10 }, "pool.poll_interval.range", "poll_interval_seconds"},
		{"poll interval above range", func(p *supervisorv1.Pool) { p.PollIntervalSeconds = 4000 }, "pool.poll_interval.range", "poll_interval_seconds"},
		{"min_idle above max_concurrency", func(p *supervisorv1.Pool) { p.MinIdleRunners = 20 }, "pool.min_idle.max_concurrency", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := validPool(env.authProfileID)
			tc.mutate(pool)
			err := env.createPool(t, pool)
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("want CodeInvalidArgument, got: %v", err)
			}
			violations := violationsFromError(t, err)
			var matched *validatev1.Violation
			for _, violation := range violations.GetViolations() {
				if violation.GetRuleId() == tc.wantRuleID {
					matched = violation
					break
				}
			}
			if matched == nil {
				t.Fatalf("rule_id %q not found in violations: %v", tc.wantRuleID, violations)
			}
			// CEL message-level rules have no field path; field rules must name
			// their field as the last path element (web inline mapping, docs/30
			// §5.4).
			if tc.wantFieldName != "" && lastFieldName(matched) != tc.wantFieldName {
				t.Fatalf("field path %v does not end in %q", matched.GetField(), tc.wantFieldName)
			}
			if matched.GetMessage() == "" {
				t.Fatal("violation carries no user-facing message")
			}
		})
	}
}

// TestValidationInterceptor_UpdatePoolIDRequired covers the request-level CEL
// rule: UpdatePool without a pool id is rejected by the interceptor before the
// handler's stateful checks run.
func TestValidationInterceptor_UpdatePoolIDRequired(t *testing.T) {
	env := setupValidationEnv(t)

	req := connect.NewRequest(&supervisorv1.UpdatePoolRequest{
		Pool: &supervisorv1.Pool{Id: 0, Name: "updated-name"},
	})
	req.Header().Set("Cookie", "session_token="+env.cookie)
	_, err := env.pools.UpdatePool(env.ctx, req)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("want CodeInvalidArgument, got: %v", err)
	}
	hasRuleID(t, err, "pool.update.id_required")
}

// TestValidationInterceptor_ClassBRules proves parser/state rules travel the
// same violations channel with their registry ids (docs/30 §5.1): quantity
// comparison, cron syntax, and pool-name uniqueness.
func TestValidationInterceptor_ClassBRules(t *testing.T) {
	env := setupValidationEnv(t)

	// memory_swap below memory_limit (quantity parser, RUN-147 lineage).
	swapPool := validPool(env.authProfileID)
	swapPool.Name = "swap-below-memory"
	swapPool.MemorySwapLimit = "1M"
	err := env.createPool(t, swapPool)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("memory swap violation want CodeInvalidArgument, got: %v", err)
	}
	hasRuleID(t, err, "pool.memory_swap.gte_memory")

	// Unparseable cron (parser rule).
	cronPool := validPool(env.authProfileID)
	cronPool.Name = "bad-cron"
	cronPool.Renovate = &supervisorv1.RenovateConfig{Enabled: true, CronSchedule: "not-a-cron"}
	err = env.createPool(t, cronPool)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("cron violation want CodeInvalidArgument, got: %v", err)
	}
	hasRuleID(t, err, "pool.renovate.cron_invalid")

	// Duplicate name (stateful uniqueness; code semantics preserved).
	first := validPool(env.authProfileID)
	first.Name = "duplicated-name"
	if err := env.createPool(t, first); err != nil {
		t.Fatalf("first create failed: %v", err)
	}
	second := validPool(env.authProfileID)
	second.Name = "duplicated-name"
	err = env.createPool(t, second)
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("duplicate want CodeAlreadyExists, got: %v", err)
	}
	hasRuleID(t, err, "pool.name.duplicate")
}

// TestValidationInterceptor_AuthAnnotations proves the auth-surface annotations
// fire BEFORE handler logic: an empty password gets the annotation rejection
// even when the handler would have answered differently (admin already exists).
func TestValidationInterceptor_AuthAnnotations(t *testing.T) {
	env := setupValidationEnv(t) // admin already set up

	_, err := env.auth.SetupAdmin(env.ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin2",
		Password: "",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("SetupAdmin empty password want CodeInvalidArgument, got: %v", err)
	}
	hasRuleID(t, err, "string.min_len")

	_, err = env.auth.Login(env.ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "",
		Password: "whatever",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Login empty username want CodeInvalidArgument, got: %v", err)
	}
	hasRuleID(t, err, "string.min_len")
}

// TestValidationInterceptor_ValidMessagesPass proves enforcement does not
// disturb valid traffic on both annotated and non-annotated (read) RPCs.
func TestValidationInterceptor_ValidMessagesPass(t *testing.T) {
	env := setupValidationEnv(t)

	pool := validPool(env.authProfileID)
	pool.Name = "passes-all-rules"
	if err := env.createPool(t, pool); err != nil {
		t.Fatalf("valid pool rejected: %v", err)
	}

	sessionReq := connect.NewRequest(&supervisorv1.GetSessionRequest{})
	sessionReq.Header().Set("Cookie", "session_token="+env.cookie)
	if _, err := env.auth.GetSession(env.ctx, sessionReq); err != nil {
		t.Fatalf("GetSession (no annotations) must pass untouched: %v", err)
	}
}

// The interceptor's fail-closed branch (evaluation errors → CodeUnavailable,
// generic wire message) is deliberately not unit-tested: connect.AnyRequest
// carries an unexported marker method, so a broken envelope cannot be faked
// outside the connect package, and triggering a CEL evaluation failure would
// require shipping a broken test proto. The branch is three lines and mirrors
// the upstream connectrpc.com/validate behavior.
