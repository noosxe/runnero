package server_test

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/suite"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/keys"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/server"
)

type AuthEngineTestSuite struct {
	suite.Suite
	ctx        context.Context
	database   *db.DB
	jwtSecret  []byte
	server     *server.Server
	testServer *httptest.Server
	authClient supervisorv1connect.AuthServiceClient
}

func (s *AuthEngineTestSuite) SetupTest() {
	s.ctx = context.Background()
	derived, err := keys.Derive("11112222333344445555666677778888")
	s.Require().NoError(err)

	s.database, err = db.Open(db.Options{
		Path:          ":memory:",
		EncryptionKey: derived.DBEncryptionKey,
	})
	s.Require().NoError(err)
	s.jwtSecret = derived.JWTSigningSecret

	s.server = server.New(server.Options{
		Port:             8080,
		AuthDB:           s.database,
		PoolDB:           s.database,
		JWTSigningSecret: s.jwtSecret,
	})

	s.testServer = httptest.NewServer(s.server.Handler())
	s.authClient = supervisorv1connect.NewAuthServiceClient(s.testServer.Client(), s.testServer.URL)
}

func (s *AuthEngineTestSuite) TearDownTest() {
	if s.testServer != nil {
		s.testServer.Close()
	}
	if s.database != nil {
		_ = s.database.Close()
	}
}

func (s *AuthEngineTestSuite) TestAdminSetupAndIdempotency() {
	// 1. Invalid input: missing credentials
	_, err := s.authClient.SetupAdmin(s.ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "",
		Password: "password",
	}))
	s.Assert().Equal(connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = s.authClient.SetupAdmin(s.ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "",
	}))
	s.Assert().Equal(connect.CodeInvalidArgument, connect.CodeOf(err))

	// 2. Successful creation
	res, err := s.authClient.SetupAdmin(s.ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "ValidAdminPassword123!",
	}))
	s.Require().NoError(err)
	s.Assert().True(res.Msg.Success)

	// 3. Duplicate creation fails with FailedPrecondition
	_, err = s.authClient.SetupAdmin(s.ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "second-admin",
		Password: "ValidAdminPassword123!",
	}))
	s.Assert().Equal(connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func (s *AuthEngineTestSuite) TestLoginAuthenticationFlow() {
	// First setup admin
	_, err := s.authClient.SetupAdmin(s.ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "ValidAdminPassword123!",
	}))
	s.Require().NoError(err)

	// 1. Non-existent user
	_, err = s.authClient.Login(s.ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "ghost",
		Password: "any-password",
	}))
	s.Assert().Equal(connect.CodeUnauthenticated, connect.CodeOf(err))

	// 2. Wrong password
	_, err = s.authClient.Login(s.ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "WrongPassword999!",
	}))
	s.Assert().Equal(connect.CodeUnauthenticated, connect.CodeOf(err))

	// 3. Valid login returns cookie
	loginRes, err := s.authClient.Login(s.ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "ValidAdminPassword123!",
	}))
	s.Require().NoError(err)
	s.Assert().True(loginRes.Msg.Success)
	s.Assert().Equal("admin", loginRes.Msg.Username)

	setCookie := loginRes.Header().Get("Set-Cookie")
	s.Assert().Contains(setCookie, "session_token=")
	s.Assert().Contains(setCookie, "HttpOnly")
	s.Assert().Contains(setCookie, "SameSite=Strict")

	// 4. Extract cookie and verify authenticated GetSession
	rawCookie := strings.Split(strings.Split(setCookie, ";")[0], "=")[1]

	getReq := connect.NewRequest(&supervisorv1.GetSessionRequest{})
	getReq.Header().Set("Cookie", "session_token="+rawCookie)
	sessRes, err := s.authClient.GetSession(s.ctx, getReq)
	s.Require().NoError(err)
	s.Assert().Equal("admin", sessRes.Msg.Username)
	s.Assert().True(sessRes.Msg.IsAdmin)

	// 5. Revoked session: delete session from DB, subsequent GetSession fails
	tokenHash := server.HashToken(rawCookie)
	err = s.database.DeleteSessionByTokenHash(s.ctx, tokenHash)
	s.Require().NoError(err)

	_, err = s.authClient.GetSession(s.ctx, getReq)
	s.Assert().Equal(connect.CodeUnauthenticated, connect.CodeOf(err))
}

func (s *AuthEngineTestSuite) TestExpiredSessionRejected() {
	// Setup admin
	_, err := s.authClient.SetupAdmin(s.ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "ValidAdminPassword123!",
	}))
	s.Require().NoError(err)

	user, err := s.database.GetAdminUserByUsername(s.ctx, "admin")
	s.Require().NoError(err)

	// Manually insert an expired session
	fakeToken := "expired-test-token-12345"
	tokenHash := server.HashToken(fakeToken)
	now := time.Now().UTC()
	_, err = s.database.CreateSession(s.ctx, db.CreateSessionParams{
		UserID:    user.ID,
		TokenHash: tokenHash,
		ExpiresAt: now.Add(-1 * time.Hour), // Expired 1 hour ago
	})
	s.Require().NoError(err)

	// Attempt GetSession with expired token
	getReq := connect.NewRequest(&supervisorv1.GetSessionRequest{})
	getReq.Header().Set("Cookie", "session_token="+fakeToken)
	_, err = s.authClient.GetSession(s.ctx, getReq)
	s.Assert().Equal(connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestAuthEngineTestSuite(t *testing.T) {
	suite.Run(t, new(AuthEngineTestSuite))
}

type PoolQuotaTestSuite struct {
	suite.Suite
	ctx        context.Context
	database   *db.DB
	jwtSecret  []byte
	server     *server.Server
	testServer *httptest.Server
	poolClient supervisorv1connect.PoolServiceClient
	cookie     string
}

func (s *PoolQuotaTestSuite) SetupTest() {
	s.ctx = context.Background()
	derived, err := keys.Derive("22223333444455556666777788889999")
	s.Require().NoError(err)

	s.database, err = db.Open(db.Options{
		Path:          ":memory:",
		EncryptionKey: derived.DBEncryptionKey,
	})
	s.Require().NoError(err)
	s.jwtSecret = derived.JWTSigningSecret

	s.server = server.New(server.Options{
		Port:             8080,
		AuthDB:           s.database,
		PoolDB:           s.database,
		JWTSigningSecret: s.jwtSecret,
	})

	s.testServer = httptest.NewServer(s.server.Handler())
	authClient := supervisorv1connect.NewAuthServiceClient(s.testServer.Client(), s.testServer.URL)
	s.poolClient = supervisorv1connect.NewPoolServiceClient(s.testServer.Client(), s.testServer.URL)

	// Setup and login admin to obtain auth cookie
	_, err = authClient.SetupAdmin(s.ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "ValidAdminPassword123!",
	}))
	s.Require().NoError(err)

	loginRes, err := authClient.Login(s.ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "ValidAdminPassword123!",
	}))
	s.Require().NoError(err)
	setCookie := loginRes.Header().Get("Set-Cookie")
	s.cookie = strings.Split(strings.Split(setCookie, ";")[0], "=")[1]
}

func (s *PoolQuotaTestSuite) TearDownTest() {
	if s.testServer != nil {
		s.testServer.Close()
	}
	if s.database != nil {
		_ = s.database.Close()
	}
}

func (s *PoolQuotaTestSuite) TestPoolQuotaValidation() {
	// Create valid auth profile for foreign key
	authProfile, err := s.database.CreateAuthProfile(s.ctx, db.CreateAuthProfileParams{
		Name:           "valid-profile",
		AuthMethod:     "pat",
		TokenEncrypted: sql.NullString{String: "enc-token", Valid: true},
	})
	s.Require().NoError(err)

	// 1. Negative min_idle_runners is rejected
	reqNegativeIdle := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:           "negative-idle-pool",
			Provider:       "github",
			RepositoryUrl:  "https://github.com/org/repo",
			AuthProfileId:  authProfile.ID,
			MinIdleRunners: -1,
			MaxConcurrency: 5,
		},
	})
	reqNegativeIdle.Header().Set("Cookie", "session_token="+s.cookie)
	_, err = s.poolClient.CreatePool(s.ctx, reqNegativeIdle)
	s.Assert().Equal(connect.CodeInvalidArgument, connect.CodeOf(err))

	// 2. Negative max_concurrency is rejected
	reqNegativeMax := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:           "negative-max-pool",
			Provider:       "github",
			RepositoryUrl:  "https://github.com/org/repo",
			AuthProfileId:  authProfile.ID,
			MinIdleRunners: 0,
			MaxConcurrency: -1,
		},
	})
	reqNegativeMax.Header().Set("Cookie", "session_token="+s.cookie)
	_, err = s.poolClient.CreatePool(s.ctx, reqNegativeMax)
	s.Assert().Equal(connect.CodeInvalidArgument, connect.CodeOf(err))

	// 3. Unsupported provider is rejected
	reqBadProv := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:           "bad-prov-pool",
			Provider:       "bitbucket",
			RepositoryUrl:  "https://bitbucket.org/org/repo",
			AuthProfileId:  authProfile.ID,
			MinIdleRunners: 1,
			MaxConcurrency: 5,
		},
	})
	reqBadProv.Header().Set("Cookie", "session_token="+s.cookie)
	_, err = s.poolClient.CreatePool(s.ctx, reqBadProv)
	s.Assert().Equal(connect.CodeInvalidArgument, connect.CodeOf(err))

	// 4. Invalid auth profile ID is rejected
	reqBadAuth := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:           "bad-auth-pool",
			Provider:       "github",
			RepositoryUrl:  "https://github.com/org/repo",
			AuthProfileId:  0, // Invalid!
			MinIdleRunners: 1,
			MaxConcurrency: 5,
		},
	})
	reqBadAuth.Header().Set("Cookie", "session_token="+s.cookie)
	_, err = s.poolClient.CreatePool(s.ctx, reqBadAuth)
	s.Assert().Equal(connect.CodeInvalidArgument, connect.CodeOf(err))

	// 3. Valid limits succeed
	reqValid := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:           "valid-pool",
			Provider:       "github",
			RepositoryUrl:  "https://github.com/org/repo",
			AuthProfileId:  authProfile.ID,
			MinIdleRunners: 2,
			MaxConcurrency: 5,
		},
	})
	reqValid.Header().Set("Cookie", "session_token="+s.cookie)
	res, err := s.poolClient.CreatePool(s.ctx, reqValid)
	s.Require().NoError(err)
	s.Assert().Equal("valid-pool", res.Msg.Pool.Name)
}

func TestPoolQuotaTestSuite(t *testing.T) {
	suite.Run(t, new(PoolQuotaTestSuite))
}
