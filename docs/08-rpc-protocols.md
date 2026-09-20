# ConnectRPC Protocols

This document defines the Protobuf schemas used for binary communication between the Vite/React frontend and the Go backend using **ConnectRPC**.

## `api.proto`

```protobuf
syntax = "proto3";

package supervisor.v1;

import "google/protobuf/timestamp.proto";

option go_package = "github.com/noosxe/runnero/internal/pb/supervisor/v1";

// ----------------------------------------
// Authentication Service
// ----------------------------------------

service AuthService {
  // Setup the initial local administrator account
  rpc SetupAdmin (SetupAdminRequest) returns (SetupAdminResponse);
  
  // Login with local credentials to receive a session token
  rpc Login (LoginRequest) returns (LoginResponse);
  
  // Verify current session
  rpc GetSession (GetSessionRequest) returns (GetSessionResponse);
  
  // Delete the caller's current session row and clear the session cookie
  rpc Logout (LogoutRequest) returns (LogoutResponse);
  
  // List the caller's active sessions (device label, clocks, current marker)
  rpc ListSessions (ListSessionsRequest) returns (ListSessionsResponse);
  
  // Revoke one of the caller's sessions by row id; revoking the current one
  // behaves like Logout
  rpc RevokeSession (RevokeSessionRequest) returns (RevokeSessionResponse);
  
  // Revoke every caller session except the current one
  rpc RevokeOtherSessions (RevokeOtherSessionsRequest) returns (RevokeOtherSessionsResponse);

  // Change the signed-in user's own password (docs/32 §4.4): verifies the
  // current password, applies the 12-character class-C floor to the new
  // one, and revokes every other session; the current session survives.
  rpc ChangePassword (ChangePasswordRequest) returns (ChangePasswordResponse);

  // Passkey (WebAuthn) authentication (RUN-247, docs/34). Enrollment is
  // session-authenticated and gated by a current-password re-check
  // (docs/34 §4.2); login is fully anonymous and passwordless — the
  // discoverable credential identifies the user (docs/34 §3.2). Options
  // and responses cross the wire as bytes holding the library's canonical
  // JSON shapes; ceremony state stays in server memory (docs/34 §3.4).
  rpc BeginPasskeyEnrollment (BeginPasskeyEnrollmentRequest) returns (BeginPasskeyEnrollmentResponse);
  rpc FinishPasskeyEnrollment (FinishPasskeyEnrollmentRequest) returns (FinishPasskeyEnrollmentResponse);
  rpc ListPasskeys (ListPasskeysRequest) returns (ListPasskeysResponse);
  rpc RenamePasskey (RenamePasskeyRequest) returns (RenamePasskeyResponse);
  rpc DeletePasskey (DeletePasskeyRequest) returns (DeletePasskeyResponse);
  rpc BeginPasskeyLogin (BeginPasskeyLoginRequest) returns (BeginPasskeyLoginResponse);
  rpc FinishPasskeyLogin (FinishPasskeyLoginRequest) returns (LoginResponse);
}

message BeginPasskeyEnrollmentRequest {
  string current_password = 1;  // min_len 1
}

message BeginPasskeyEnrollmentResponse {
  bytes public_key_options_json = 1;  // protocol.CredentialCreation JSON
}

message FinishPasskeyEnrollmentRequest {
  bytes attestation_response_json = 1;  // registration PublicKeyCredentialJSON
  string name = 2;                      // 1..64 chars; empty -> "Passkey"
}

message FinishPasskeyEnrollmentResponse {
  PasskeyInfo passkey = 1;
}

message PasskeyInfo {
  int64 id = 1;             // credential row id (Rename/Delete), never material
  string name = 2;
  google.protobuf.Timestamp created_at = 3;
  google.protobuf.Timestamp last_used_at = 4;
  bool backup_eligible = 5; // "synced" vs "device-bound" chip
  bool backup_state = 6;
  bool clone_warning = 7;   // red banner; credential refuses further assertions
}

message ListPasskeysRequest {}
message ListPasskeysResponse { repeated PasskeyInfo passkeys = 1; }

message RenamePasskeyRequest {
  int64 id = 1;
  string name = 2;  // 1..64 chars; empty -> "Passkey"
}
message RenamePasskeyResponse {}

message DeletePasskeyRequest { int64 id = 1; }
message DeletePasskeyResponse {}

message BeginPasskeyLoginRequest {}
message BeginPasskeyLoginResponse {
  bytes public_key_options_json = 1;  // CredentialAssertion JSON, empty allow-list
}

message FinishPasskeyLoginRequest {
  bytes assertion_response_json = 1;  // authentication PublicKeyCredentialJSON
}
// FinishPasskeyLogin returns LoginResponse - the standard session shape
// through the shared issuance path (docs/34 §3.3).

message SetupAdminRequest {
  string username = 1;
  string password = 2;  // min 12 chars (class-C floor, docs/32 §4.4; RUN-242)
}

message SetupAdminResponse {
  bool success = 1;
}

message LoginRequest {
  string username = 1;
  string password = 2;
}

// Note: The opaque session token (docs/32) is set via Set-Cookie header (HttpOnly, Secure,
// SameSite=Strict), not returned in the response body. Failed logins answer
// Unauthenticated with a generic "invalid username or password"; after 5
// failures inside 15 minutes for the same username+client-IP, logins answer
// ResourceExhausted with a retry hint until the exponential backoff (cap
// 15m) elapses (docs/32 section 4.2).
message LoginResponse {
  bool success = 1;
  string username = 2;
}

message GetSessionRequest {}

message GetSessionResponse {
  string username = 1;
  bool is_admin = 2;  // Deprecated: computed as role == "admin"; kept for older consumers
  string role = 5;    // Live role, "admin" | "viewer" (RUN-236, docs/35 section 2.1):
                      // re-read from admin_users on every request, so role
                      // changes apply on the caller's next request
  string version = 6; // Product version of the running binary (ldflags-stamped
                      // main.version, RUN-249/250; e.g. "v0.3.0-379-g2eb9bc2").
                      // Surfaced for the sidebar footer and settings Instance
                      // card (RUN-251); authenticated-only, so the version is
                      // never a pre-auth fingerprint
}

// User management (RUN-236, docs/35 section 2.3): every procedure is
// admin-bucket. Guard rails are structural: the last admin can never be
// demoted or deleted (the guard and the write are serialized), and nobody
// can delete their own account. SetUserPassword revokes all of the
// target's sessions except the caller's own; passkeys are untouched.
service UserService {
  rpc ListUsers(ListUsersRequest) returns (ListUsersResponse);
  rpc CreateUser(CreateUserRequest) returns (CreateUserResponse);
  rpc SetUserRole(SetUserRoleRequest) returns (SetUserRoleResponse);
  rpc SetUserPassword(SetUserPasswordRequest) returns (SetUserPasswordResponse);
  rpc DeleteUser(DeleteUserRequest) returns (DeleteUserResponse);
}

// Session control (docs/32 section 3.5): all four procedures operate
// exclusively on the calling user's rows - the ownership guard lives in the
// SQL (WHERE user_id = caller), so a multi-user future cannot silently
// regress it. Responses never carry token material; SessionInfo exposes
// only the row id and display metadata.
message LogoutRequest {}

message LogoutResponse {
  bool success = 1;  // Idempotent: deleting an already-gone row still succeeds
}

message ListSessionsRequest {}

message ListSessionsResponse {
  repeated SessionInfo sessions = 1;
}

message RevokeSessionRequest {
  int64 session_id = 1;  // Row id, never the token
}

message RevokeSessionResponse {
  bool success = 1;
}

// Unknown or foreign session ids answer NotFound - the query-level
// ownership guard makes them indistinguishable.
message RevokeOtherSessionsRequest {}

message RevokeOtherSessionsResponse {
  int64 revoked = 1;  // Number of other sessions removed
}

// A wrong current password answers InvalidArgument with a typed violation
// (rule id auth.password.current_mismatch) on the current_password field;
// new_password carries a min_len=12 protovalidate annotation (docs/32 §4.4).
message ChangePasswordRequest {
  string current_password = 1;
  string new_password = 2;
}

message ChangePasswordResponse {
  bool success = 1;
  int64 revoked_sessions = 2;  // Other sessions invalidated by the change
}

// One of the caller's live sessions. device_label is parsed from the login
// user agent ("Firefox 130 on Linux"); is_current marks the row answering
// the request.
message SessionInfo {
  int64 id = 1;
  string device_label = 2;
  google.protobuf.Timestamp created_at = 3;
  google.protobuf.Timestamp last_seen_at = 4;
  google.protobuf.Timestamp expires_at = 5;             // Sliding idle deadline
  google.protobuf.Timestamp absolute_expires_at = 6;    // Fixed lifetime cap
  bool is_current = 7;
}

// ----------------------------------------
// Pool Management Service
// ----------------------------------------

service PoolService {
  rpc ListPools (ListPoolsRequest) returns (ListPoolsResponse);
  rpc CreatePool (CreatePoolRequest) returns (CreatePoolResponse);
  rpc UpdatePool (UpdatePoolRequest) returns (UpdatePoolResponse);
  rpc DeletePool (DeletePoolRequest) returns (DeletePoolResponse);
  rpc DiscoverTargets (DiscoverTargetsRequest) returns (DiscoverTargetsResponse);
}

enum PoolHealthStatus {
  POOL_HEALTH_STATUS_UNSPECIFIED = 0;
  POOL_HEALTH_STATUS_HEALTHY = 1;
  POOL_HEALTH_STATUS_PROVISIONING = 2;
  POOL_HEALTH_STATUS_DEGRADED = 3;
  POOL_HEALTH_STATUS_PAUSED = 4;
}

message Pool {
  int64 id = 1;
  string name = 2;
  string provider = 3;
  string repository_url = 4; // Primary / legacy single target
  int32 min_idle_runners = 5;
  int32 max_concurrency = 6;
  repeated string labels = 7;
  string runner_image = 8;
  bool allow_docker = 9;
  RenovateConfig renovate = 10;

  // Runtime stats (read-only, populated by server)
  int32 active_runners = 11;
  int32 idle_runners = 12;

  // Resource configuration
  int64 auth_profile_id = 13;
  string scope = 14;              // "repo" or "org"
  string cpu_limit = 15;
  string memory_limit = 16;
  string memory_swap_limit = 32;  // RUN-147: Docker MemorySwap semantics — "" = 2x daemon default, "-1" = unlimited, else >= memory_limit
  int32 pids_limit = 33;  // RUN-148: Docker PidsLimit semantics — 0 = unlimited (opt-out), else the process ceiling
  int32 max_runner_lifetime_seconds = 17; // max busy wall-clock per job, anchored at first pickup (docs/23)
  repeated string target_urls = 18; // Multi-target URLs (homogeneously repos or orgs)

  // Operational state & diagnostics (read-only, populated by server)
  PoolHealthStatus health_status = 21;
  string current_intent = 22;
  string last_error = 23;
  string last_error_code = 24;
  string last_error_timestamp = 25;
  string last_reconciled_at = 26;
}

message RenovateConfig {
  bool enabled = 1;
  string cron_schedule = 2;
  string image = 3;
}

message ListPoolsRequest {}

message ListPoolsResponse {
  repeated Pool pools = 1;
}

message CreatePoolRequest {
  Pool pool = 1;
}

message CreatePoolResponse {
  Pool pool = 1;
}

message UpdatePoolRequest {
  Pool pool = 1;
}

message UpdatePoolResponse {
  Pool pool = 1;
}
```

> **UpdatePool semantics (docs/22 §5):** full-replace of every writable pool
> field; read-only runtime fields in the request are ignored. Provider is
> immutable (`CodeInvalidArgument`). Renaming is metadata-only — tracking is
> keyed by pool id, so live runners are unaffected and keep their spawn-time
> name label until they recycle naturally (RUN-126). Duplicate names return
> `CodeAlreadyExists`; unknown ids `CodeNotFound`.
> Spawn-identity edits (targets, labels, image, `allow_docker`, resource
> limits, auth profile) recycle the pool's idle runners before the write —
> busy runners are never terminated by an edit. `pool_targets` rows are
> rewritten only when the normalized target set changed, and the `pool.update`
> audit entry records before/after values restricted to changed fields.

```protobuf
message DeletePoolRequest {
  int64 id = 1;
}

message DeletePoolResponse {
  bool success = 1;
}

message DiscoverTargetsRequest {
  int64 auth_profile_id = 1;
  string scope = 2; // "repo" or "org"
}

message DiscoveredTarget {
  string name = 1;
  string full_name = 2;
  string html_url = 3;
  string description = 4;
  bool is_private = 5;
  string avatar_url = 6;
}

message DiscoverTargetsResponse {
  repeated DiscoveredTarget targets = 1;
}

// ----------------------------------------
// Auth Profile Management Service
// ----------------------------------------

service AuthProfileService {
  rpc ListAuthProfiles (ListAuthProfilesRequest) returns (ListAuthProfilesResponse);
  rpc CreateAuthProfile (CreateAuthProfileRequest) returns (CreateAuthProfileResponse);
  rpc UpdateAuthProfile (UpdateAuthProfileRequest) returns (UpdateAuthProfileResponse);
  rpc DeleteAuthProfile (DeleteAuthProfileRequest) returns (DeleteAuthProfileResponse);
}

message AuthProfile {
  int64 id = 1;
  string name = 2;
  string auth_method = 3;         // "github_app", "gitea_token", "forgejo_token", "pat"
  int64 app_id = 4;               // GitHub App only
  bool has_private_key = 5;       // Read-only indicator (never exposes raw key)
  bool has_token = 6;             // Read-only indicator (never exposes raw token)
}

message ListAuthProfilesRequest {}

message ListAuthProfilesResponse {
  repeated AuthProfile profiles = 1;
}

message CreateAuthProfileRequest {
  string name = 1;
  string auth_method = 2;
  int64 app_id = 3;               // GitHub App only
  bytes private_key = 4;          // GitHub App private key PEM (write-only)
  string token = 5;               // PAT or Gitea/Forgejo token (write-only)
}

message CreateAuthProfileResponse {
  AuthProfile profile = 1;
}

message UpdateAuthProfileRequest {
  int64 id = 1;                   // Target profile (required, > 0)
  string name = 2;                // New display name (required, unique)
  string auth_method = 3;         // "github_app", "gitea_token", "forgejo_token", "pat"
  int64 app_id = 4;               // GitHub App only (required > 0 for github_app)
  bytes private_key = 5;          // Write-only; empty = keep existing key
  string token = 6;               // Write-only; empty = keep existing token
}

message UpdateAuthProfileResponse {
  AuthProfile profile = 1;
}

message DeleteAuthProfileRequest {
  int64 id = 1;
}

message DeleteAuthProfileResponse {
  bool success = 1;
}

// ----------------------------------------
// Onboarding Service
// ----------------------------------------

service OnboardingService {
  rpc GetOnboardingStatus (GetOnboardingStatusRequest) returns (GetOnboardingStatusResponse);
  rpc GetAppSettings (GetAppSettingsRequest) returns (GetAppSettingsResponse);
  rpc SetAppSetting (SetAppSettingRequest) returns (SetAppSettingResponse);
  rpc CompleteOnboarding (CompleteOnboardingRequest) returns (CompleteOnboardingResponse);
}

message GetOnboardingStatusRequest {}

message GetOnboardingStatusResponse {
  bool admin_created = 1;
  bool auth_profile_exists = 2;
  bool pool_exists = 3;
  bool setup_complete = 4;        // true when admin_created AND (onboarding_completed OR pool_exists)
  bool onboarding_completed = 5;  // true if CompleteOnboarding was invoked
  string host_arch = 6;
  string host_os = 7;
  bool passkey_available = 8;     // WebAuthn configured (docs/34 §3.5); gates the login passkey button
}

message CompleteOnboardingRequest {}

message CompleteOnboardingResponse {
  bool success = 1;
}

message GetAppSettingsRequest {}

message AppSettingEntry {
  string key = 1;
  string value = 2;
  string updated_at = 3;
}

message GetAppSettingsResponse {
  repeated AppSettingEntry settings = 1;
}

message SetAppSettingRequest {
  string key = 1;
  string value = 2;
}

message SetAppSettingResponse {
  string key = 1;
  string value = 2;
}

// ----------------------------------------
// Analytics & History Service
// ----------------------------------------

service AnalyticsService {
  rpc GetJobHistory (GetJobHistoryRequest) returns (GetJobHistoryResponse);
  rpc GetSystemStats (GetSystemStatsRequest) returns (GetSystemStatsResponse);
}

message JobRecord {
  int64 id = 1;
  int64 pool_id = 2;
  string runner_name = 3;
  string status = 4;
  string queued_at = 5;
  string started_at = 6;
  string completed_at = 7;
  double duration_seconds = 8;
  double queue_time_seconds = 9;
  string pool_name = 10;
}

`duration_seconds`, `queue_time_seconds`, and `pool_name` are derived, never
stored: every producer (GetJobHistory, GetJobRecord, WatchDashboard) converts
rows through one shared converter that computes `queue_time_seconds =
started_at − queued_at` and `duration_seconds = completed_at − started_at`
(missing side or clock-skew-negative → 0, rendered as "—" by the UI; RUN-246).

message GetJobHistoryRequest {
  int64 pool_id = 1; // 0 for all
  int32 limit = 2;
  int32 offset = 3;
}

message GetJobHistoryResponse {
  repeated JobRecord jobs = 1;
  int32 total_count = 2;
}

message GetSystemStatsRequest {}

message GetSystemStatsResponse {
  int32 total_active_runners = 1;
  int32 total_idle_runners = 2;
  double average_queue_time_seconds = 3;
  int32 total_jobs_24h = 4;
  int32 successful_jobs_24h = 5;
  int32 failed_jobs_24h = 6;
  double average_runtime_seconds = 7;
  double success_rate_percent = 8;
```

## Validation contract (protovalidate, RUN-216 / docs/30)

Validation rules live in `proto/api.proto` as
[protovalidate](https://protovalidate.com) annotations and are enforced on
every unary RPC by a Connect interceptor (`internal/server/validate.go`,
innermost — after authentication). Rules are classified (docs/30 §5.1):

- **Class A — schema rules** (format, range, required, cross-field CEL):
  declared as annotations on the request messages. Evaluated by protovalidate-go
  server-side and protovalidate-es client-side; drift between the two is
  impossible by construction. Violations use protovalidate's canonical rule ids
  (`string.min_len`, `int64.gt`, …) plus explicit ids for CEL rules
  (`pool.min_idle.max_concurrency`, `pool.poll_interval.range`,
  `pool.update.id_required`, `auth_profile.app_id.required`,
  `auth_profile.private_key.required`, `auth_profile.token.required`).
- **Class B — parser/stateful rules** (quantity-string parsing, cron syntax,
  URL parsing, uniqueness, immutability): cannot be expressed in the schema;
  enforced in Go handlers and delivered through the identical channel with
  stable rule ids from the registry in `internal/server/violations.go`
  (`pool.memory_swap.gte_memory`, `pool.renovate.cron_invalid`,
  `pool.name.duplicate`, …). The registry is API contract — never rename ids
  without updating this doc and the web's registry mirror together.
- **Class C — UI-state rules** (wizard step gating, custom-mode requires a
  value) have no wire representation and live only in TanStack Form
  (docs/30 §5.1).

### Wire shape

Any rule violation rejects the RPC with `CodeInvalidArgument` (uniqueness
keeps `CodeAlreadyExists`) and attaches **one** Connect error detail of type
`buf.validate.Violations`:

```proto
message Violations {
  repeated Violation violations = 1;
}
message Violation {
  optional FieldPath field = 5;   // structured path, e.g. [pool, memory_limit]
  optional string rule_id = 2;    // stable id from the registries above
  optional string message = 3;    // user-facing text
}
```

Clients that ignore details see today's behavior unchanged: the error message
is the violations' user-facing texts joined with `"; "`. The web maps each
violation to its form field by the last `field` path element and keys
behavior on `rule_id`, never on message text (docs/30 §5.4).

Enforcement is fail-closed: rule-evaluation failures (CEL compilation,
uninspectable envelopes) reject the request with a generic message and log
the cause server-side — a request never passes unvalidated.

## LogService (RUN-218, docs/29 §5.1)

Read side of the persisted log store (RUN-186): live runner streaming plus
bounded reads over the on-disk artifacts — supervisor boot files, runner
captures, and removal records. All names used in paths are validated
(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,128}$`), and every read is capped server-side.

```proto
service LogService {
  // Live streaming logs of an active runner container
  rpc StreamRunnerLogs (StreamRunnerLogsRequest) returns (stream LogChunk);

  // Historical logs for completed/exited runners (last tail_lines entries,
  // default 500, cap 5000). runner_id accepts the Docker container ID
  // (the capture-file key) or the container name (RUN-252): names resolve
  // through job_history.log_retention_path, always re-validated to a file
  // inside DATA_DIR/logs
  rpc GetRunnerLogs (GetRunnerLogsRequest) returns (GetRunnerLogsResponse);

  // List retained supervisor boot files (stat-only + 1-line header reads)
  rpc ListSupervisorLogs (ListSupervisorLogsRequest) returns (ListSupervisorLogsResponse);

  // Server-stream one boot file; follow is rejected (FailedPrecondition)
  // for previous boots — only the current boot can be followed
  rpc StreamSupervisorLog (StreamSupervisorLogRequest) returns (stream LogChunk);

  // Paginated, filtered removal records (reverse-chronological; scan cap
  // 10k lines/request; page cap 200)
  rpc ListRemovalRecords (ListRemovalRecordsRequest) returns (ListRemovalRecordsResponse);
}

message SupervisorBootLog {
  string file = 1;        // base name only
  string boot_id = 2;     // from the file header, filename fallback
  string started_at = 3;  // RFC3339
  int64 size_bytes = 4;
  int32 rotation_seq = 5; // 0 = unrotated base file
  bool is_current = 6;
}

message ListSupervisorLogsResponse { repeated SupervisorBootLog boots = 1; }

message StreamSupervisorLogRequest {
  string file = 1;
  bool follow = 2;
  int32 tail_lines = 3;   // default 500, cap 5000
}

message ListRemovalRecordsRequest {
  int64 pool_id = 1;      // optional filters; zero/empty = unset
  string runner_id = 2;
  string reason = 3;
  string since = 4;       // RFC3339
  string until = 5;       // RFC3339
  int32 page_size = 6;    // default 50, cap 200
  string cursor = 7;      // ts (RFC3339Nano) of the previous page's last record
}

message RemovalRecordSummary {
  string ts = 1;          // RFC3339
  string boot_id = 2;
  string runner_id = 3;
  string runner_name = 4;
  int64 pool_id = 5;
  string pool_name = 6;
  string reason = 7;
  bool provider_busy = 8;
  string dereg_error = 9;
  int32 exit_code = 10;   // -1 = unset
  bool capture_ok = 11;
  int64 capture_bytes = 12;
}

message ListRemovalRecordsResponse {
  repeated RemovalRecordSummary records = 1;
  string next_cursor = 2; // empty = no further records
}
```

Removal records reuse the writer's schema (orchestrator.RemovalRecord,
docs/28 §5.4); a round-trip test guards the projection against drift.

## Reverse Proxy & Streaming Considerations

ConnectRPC server-streaming RPCs (`LogService.StreamRunnerLogs`, `LogService.StreamSupervisorLog`, `DashboardService.WatchDashboard`, `PoolService.WatchPools`, `PoolService.WatchRunners`) push chunks over long-lived HTTP/2 or chunked HTTP/1.1 connections.

When operating behind a reverse proxy:
- **Disable Buffering**: Proxies must not buffer responses (e.g. Caddy `flush_interval -1`, Traefik `flushInterval: -1`, Nginx `proxy_buffering off;`).
- **HTTP/2 Transport**: Terminates TLS at the proxy and allows multiplexing streaming RPCs without running into browser per-host connection limits.
- **Secure Cookie**: Set `SUPERVISOR_SECURE_COOKIES=always`, or keep the default `auto` plus `SUPERVISOR_TRUSTED_PROXY=true` (enabling `X-Forwarded-Proto` trust) when TLS is terminated at the proxy.

For detailed configuration examples and deployment manifests, see [docs/10-reverse-proxy-tls.md](10-reverse-proxy-tls.md).

