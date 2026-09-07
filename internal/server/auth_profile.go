package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/provider"
)

// AuthProfileDatabase defines the database queries required by AuthProfileService.
// *db.DB satisfies this interface.
type AuthProfileDatabase interface {
	ListAuthProfiles(ctx context.Context) ([]db.AuthProfile, error)
	GetAuthProfileById(ctx context.Context, id int64) (db.AuthProfile, error)
	CreateAuthProfile(ctx context.Context, arg db.CreateAuthProfileParams) (db.AuthProfile, error)
	DeleteAuthProfile(ctx context.Context, id int64) error
	ListRunnerPools(ctx context.Context) ([]db.RunnerPool, error)
	CreateAuditLog(ctx context.Context, arg db.CreateAuditLogParams) (db.AuditLog, error)
}

// CredentialValidator optionally validates credentials against the upstream git provider during profile creation.
type CredentialValidator interface {
	ValidateCredentials(ctx context.Context, req *supervisorv1.CreateAuthProfileRequest) error
}

// AuthProfileService implements supervisorv1connect.AuthProfileServiceHandler.
type AuthProfileService struct {
	supervisorv1connect.UnimplementedAuthProfileServiceHandler
	db            AuthProfileDatabase
	encryptionKey []byte
	validator     CredentialValidator
}

// NewAuthProfileService constructs an AuthProfileService instance.
func NewAuthProfileService(database AuthProfileDatabase, encryptionKey []byte, validator CredentialValidator) *AuthProfileService {
	return &AuthProfileService{
		db:            database,
		encryptionKey: encryptionKey,
		validator:     validator,
	}
}

func toAuthProfileProto(p db.AuthProfile) *supervisorv1.AuthProfile {
	return &supervisorv1.AuthProfile{
		Id:            p.ID,
		Name:          p.Name,
		AuthMethod:    p.AuthMethod,
		AppId:         p.AppID.Int64,
		HasPrivateKey: p.PrivateKeyEncrypted.Valid && p.PrivateKeyEncrypted.String != "",
		HasToken:      p.TokenEncrypted.Valid && p.TokenEncrypted.String != "",
	}
}

func (s *AuthProfileService) populateAppMetadata(ctx context.Context, proto *supervisorv1.AuthProfile, p db.AuthProfile) {
	if p.AuthMethod != "github_app" || len(s.encryptionKey) == 0 || !p.PrivateKeyEncrypted.Valid || !p.AppID.Valid {
		return
	}
	privKey, err := db.Decrypt(s.encryptionKey, p.PrivateKeyEncrypted.String)
	if err != nil {
		return
	}
	decProfile := db.DecryptedAuthProfile{
		AuthProfile: p,
		PrivateKey:  privKey,
	}
	prov, err := provider.DefaultRegistry.Build(ctx, decProfile)
	if err != nil {
		return
	}
	if metaProv, ok := prov.(provider.AppMetadataProvider); ok {
		installURL, insts, err := metaProv.GetAppMetadata(ctx)
		if err == nil {
			proto.InstallUrl = installURL
			proto.InstallationsCount = int32(len(insts))
		}
	}
}

func validateCreateAuthProfileRequest(req *supervisorv1.CreateAuthProfileRequest) error {
	if req == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("request payload is required"))
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("auth profile name must not be empty"))
	}

	method := strings.ToLower(strings.TrimSpace(req.AuthMethod))
	switch method {
	case "github_app":
		if req.AppId <= 0 {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("github_app authentication requires a valid positive app_id"))
		}
		if len(req.PrivateKey) == 0 {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("github_app authentication requires private_key"))
		}
	case "gitea_token", "forgejo_token", "pat":
		if strings.TrimSpace(req.Token) == "" {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s authentication requires token", method))
		}
	default:
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported auth_method %q; must be 'github_app', 'gitea_token', 'forgejo_token', or 'pat'", req.AuthMethod))
	}

	return nil
}

// ListAuthProfiles returns all auth profiles with read-only boolean indicators (never raw secret material).
func (s *AuthProfileService) ListAuthProfiles(ctx context.Context, _ *connect.Request[supervisorv1.ListAuthProfilesRequest]) (*connect.Response[supervisorv1.ListAuthProfilesResponse], error) {
	profiles, err := s.db.ListAuthProfiles(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing auth profiles: %w", err))
	}

	resp := &supervisorv1.ListAuthProfilesResponse{
		Profiles: make([]*supervisorv1.AuthProfile, 0, len(profiles)),
	}
	for _, p := range profiles {
		proto := toAuthProfileProto(p)
		s.populateAppMetadata(ctx, proto, p)
		resp.Profiles = append(resp.Profiles, proto)
	}

	return connect.NewResponse(resp), nil
}

// CreateAuthProfile encrypts write-only secrets (AES-256) and stores the profile.
func (s *AuthProfileService) CreateAuthProfile(ctx context.Context, req *connect.Request[supervisorv1.CreateAuthProfileRequest]) (*connect.Response[supervisorv1.CreateAuthProfileResponse], error) {
	if err := validateCreateAuthProfileRequest(req.Msg); err != nil {
		return nil, err
	}

	if s.validator != nil {
		if err := s.validator.ValidateCredentials(ctx, req.Msg); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("credential validation failed: %w", err))
		}
	}

	var encPriv sql.NullString
	if len(req.Msg.PrivateKey) > 0 {
		if len(s.encryptionKey) == 0 {
			return nil, connect.NewError(connect.CodeInternal, errors.New("database encryption key not configured"))
		}
		enc, err := db.Encrypt(s.encryptionKey, string(req.Msg.PrivateKey))
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encrypting private key: %w", err))
		}
		encPriv = sql.NullString{String: enc, Valid: true}
	}

	var encTok sql.NullString
	if strings.TrimSpace(req.Msg.Token) != "" {
		if len(s.encryptionKey) == 0 {
			return nil, connect.NewError(connect.CodeInternal, errors.New("database encryption key not configured"))
		}
		enc, err := db.Encrypt(s.encryptionKey, strings.TrimSpace(req.Msg.Token))
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encrypting token: %w", err))
		}
		encTok = sql.NullString{String: enc, Valid: true}
	}

	var appID sql.NullInt64
	if req.Msg.AppId > 0 {
		appID = sql.NullInt64{Int64: req.Msg.AppId, Valid: true}
	}

	created, err := s.db.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:                strings.TrimSpace(req.Msg.Name),
		AuthMethod:          strings.ToLower(strings.TrimSpace(req.Msg.AuthMethod)),
		AppID:               appID,
		PrivateKeyEncrypted: encPriv,
		TokenEncrypted:      encTok,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("creating auth profile: %w", err))
	}

	recordAuditLog(ctx, s.db, "auth_profile.create", "auth_profile", &created.ID, map[string]any{
		"name":        created.Name,
		"auth_method": created.AuthMethod,
	})

	resProto := toAuthProfileProto(created)
	s.populateAppMetadata(ctx, resProto, created)
	return connect.NewResponse(&supervisorv1.CreateAuthProfileResponse{
		Profile: resProto,
	}), nil
}

// DeleteAuthProfile removes an auth profile from the database if not referenced by runner pools.
func (s *AuthProfileService) DeleteAuthProfile(ctx context.Context, req *connect.Request[supervisorv1.DeleteAuthProfileRequest]) (*connect.Response[supervisorv1.DeleteAuthProfileResponse], error) {
	if req.Msg.Id <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid auth profile id"))
	}

	existing, err := s.db.GetAuthProfileById(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("auth profile id %d not found: %w", req.Msg.Id, err))
	}

	// Check if any runner pool currently references this profile
	pools, err := s.db.ListRunnerPools(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("checking runner pool references: %w", err))
	}
	for _, p := range pools {
		if p.AuthProfileID == req.Msg.Id {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot delete auth profile %q: referenced by runner pool %q", existing.Name, p.Name))
		}
	}

	if err := s.db.DeleteAuthProfile(ctx, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("deleting auth profile: %w", err))
	}

	recordAuditLog(ctx, s.db, "auth_profile.delete", "auth_profile", &existing.ID, map[string]any{
		"name":        existing.Name,
		"auth_method": existing.AuthMethod,
	})

	return connect.NewResponse(&supervisorv1.DeleteAuthProfileResponse{
		Success: true,
	}), nil
}
