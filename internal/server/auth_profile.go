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
	UpdateAuthProfile(ctx context.Context, arg db.UpdateAuthProfileParams) (db.AuthProfile, error)
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

// validateUpdateAuthProfileRequest performs the request-static checks shared by
// every update (docs/17 §4.3). State-dependent rules (blank = keep existing
// secret, purge on method switch) live in computeUpdateAuthProfilePlan, which
// sees the stored profile.
func validateUpdateAuthProfileRequest(req *supervisorv1.UpdateAuthProfileRequest) error {
	if req == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("request payload is required"))
	}
	if req.Id <= 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("invalid auth profile id"))
	}
	if strings.TrimSpace(req.Name) == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("auth profile name must not be empty"))
	}

	method := strings.ToLower(strings.TrimSpace(req.AuthMethod))
	switch method {
	case "github_app":
		if req.AppId <= 0 {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("github_app authentication requires a valid positive app_id"))
		}
	case "gitea_token", "forgejo_token", "pat":
		// Token presence is decided against the stored profile (blank = keep).
	default:
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported auth_method %q; must be 'github_app', 'gitea_token', 'forgejo_token', or 'pat'", req.AuthMethod))
	}

	return nil
}

// updatePlan is the result of merging an UpdateAuthProfileRequest onto the
// stored profile: the full-row DB params to persist plus the list of secret
// classes being rotated ("private_key" and/or "token"; never secret values).
type updatePlan struct {
	params  db.UpdateAuthProfileParams
	rotated []string
}

// computeUpdateAuthProfilePlan merges req onto existing per docs/17 §4.2:
//
//   - blank secret = keep the existing ciphertext, but only when the method is
//     unchanged (a method switch cannot keep a secret of the wrong kind);
//   - columns not applicable to the (new) auth method are purged (NULL), so
//     stale credential material never lingers after a switch;
//   - a degenerate row missing its method's stored secret self-heals by
//     requiring the operator to supply it (CodeFailedPrecondition with blank,
//     CodeInvalidArgument on method switch).
func computeUpdateAuthProfilePlan(req *supervisorv1.UpdateAuthProfileRequest, existing db.AuthProfile) (updatePlan, error) {
	method := strings.ToLower(strings.TrimSpace(req.AuthMethod))
	plan := updatePlan{params: db.UpdateAuthProfileParams{
		ID:         req.Id,
		Name:       strings.TrimSpace(req.Name),
		AuthMethod: method,
	}}

	// Columns not applicable to the target method are always purged.
	notApplicable := sql.NullString{}

	switch method {
	case "github_app":
		hasExistingKey := existing.PrivateKeyEncrypted.Valid && existing.PrivateKeyEncrypted.String != ""
		plan.params.AppID = sql.NullInt64{Int64: req.AppId, Valid: true}
		plan.params.TokenEncrypted = notApplicable
		switch {
		case len(req.PrivateKey) > 0:
			plan.rotated = append(plan.rotated, "private_key")
		case hasExistingKey && existing.AuthMethod == method:
			plan.params.PrivateKeyEncrypted = existing.PrivateKeyEncrypted
		case existing.AuthMethod != method:
			return updatePlan{}, connect.NewError(connect.CodeInvalidArgument, errors.New("changing auth_method to github_app requires private_key"))
		default:
			// Degenerate row: github_app without a stored key self-heals on update.
			return updatePlan{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("auth profile has no stored private key; supply private_key to set it"))
		}
	case "gitea_token", "forgejo_token", "pat":
		hasExistingToken := existing.TokenEncrypted.Valid && existing.TokenEncrypted.String != ""
		plan.params.AppID = sql.NullInt64{}
		plan.params.PrivateKeyEncrypted = notApplicable
		switch {
		case strings.TrimSpace(req.Token) != "":
			plan.rotated = append(plan.rotated, "token")
		case hasExistingToken && existing.AuthMethod == method:
			plan.params.TokenEncrypted = existing.TokenEncrypted
		case existing.AuthMethod != method:
			return updatePlan{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("changing auth_method to %s requires token", method))
		default:
			// Degenerate row: token method without a stored token self-heals on update.
			return updatePlan{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("auth profile has no stored token; supply token to set it"))
		}
	}

	return plan, nil
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

// UpdateAuthProfile renames an auth profile, rotates its write-only secrets,
// and/or changes its auth method (docs/17). It follows a read-merge-write flow
// over the existing sqlc UpdateAuthProfile query: blank secrets keep the
// stored ciphertext, method switches purge credential material of the old
// method, and upstream validation runs only when a new secret is supplied.
func (s *AuthProfileService) UpdateAuthProfile(ctx context.Context, req *connect.Request[supervisorv1.UpdateAuthProfileRequest]) (*connect.Response[supervisorv1.UpdateAuthProfileResponse], error) {
	if err := validateUpdateAuthProfileRequest(req.Msg); err != nil {
		return nil, err
	}

	existing, err := s.db.GetAuthProfileById(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("auth profile id %d not found: %w", req.Msg.Id, err))
	}

	plan, err := computeUpdateAuthProfilePlan(req.Msg, existing)
	if err != nil {
		return nil, err
	}

	// Upstream credential validation only when a new secret is supplied:
	// renames must not burn provider rate limits or fail on transient
	// provider outages, and kept secrets were already validated (docs/17 §5.2).
	if s.validator != nil && len(plan.rotated) > 0 {
		// Adapter: the validator speaks the Create request shape; the update
		// request mirrors it field-for-field, so the check is identical.
		checkReq := &supervisorv1.CreateAuthProfileRequest{
			Name:       plan.params.Name,
			AuthMethod: plan.params.AuthMethod,
			AppId:      req.Msg.AppId,
			PrivateKey: req.Msg.PrivateKey,
			Token:      req.Msg.Token,
		}
		if err := s.validator.ValidateCredentials(ctx, checkReq); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("credential validation failed: %w", err))
		}
	}

	// Encrypt rotated secrets with the current key; this also migrates
	// ciphertexts forward after an encryption key rotation (docs/17 §7).
	for _, secret := range plan.rotated {
		if len(s.encryptionKey) == 0 {
			return nil, connect.NewError(connect.CodeInternal, errors.New("database encryption key not configured"))
		}
		switch secret {
		case "private_key":
			enc, err := db.Encrypt(s.encryptionKey, string(req.Msg.PrivateKey))
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encrypting private key: %w", err))
			}
			plan.params.PrivateKeyEncrypted = sql.NullString{String: enc, Valid: true}
		case "token":
			enc, err := db.Encrypt(s.encryptionKey, strings.TrimSpace(req.Msg.Token))
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encrypting token: %w", err))
			}
			plan.params.TokenEncrypted = sql.NullString{String: enc, Valid: true}
		}
	}

	updated, err := s.db.UpdateAuthProfile(ctx, plan.params)
	if err != nil {
		if db.IsUniqueConstraintError(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("auth profile name %q already exists", plan.params.Name))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("updating auth profile: %w", err))
	}

	// Audit metadata only: before/after names and methods plus which secret
	// classes rotated — never any secret material (docs/17 §7).
	recordAuditLog(ctx, s.db, "auth_profile.update", "auth_profile", &updated.ID, map[string]any{
		"before": map[string]any{
			"name":        existing.Name,
			"auth_method": existing.AuthMethod,
		},
		"after": map[string]any{
			"name":        updated.Name,
			"auth_method": updated.AuthMethod,
		},
		"secrets_rotated": plan.rotated,
	})

	resProto := toAuthProfileProto(updated)
	s.populateAppMetadata(ctx, resProto, updated)
	return connect.NewResponse(&supervisorv1.UpdateAuthProfileResponse{
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
