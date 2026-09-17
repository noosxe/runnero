package server

import (
	"errors"
	"fmt"
	"strings"

	validatev1 "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
)

// Stable rule_id registry for class B validation (docs/30 §5.1, §5.3): rules
// whose semantics live in Go (parsers, cross-resource state, uniqueness) and
// that the proto schema cannot express as annotations. They fire in handlers
// and travel to clients through the SAME buf.validate.Violations error-detail
// channel the validation interceptor uses for class A annotations, so the web
// maps every rejection inline by (rule_id, field path) without message parsing.
//
// These ids are API contract (docs/08): the web mirrors them when it maps
// violations onto form fields — never rename without updating docs/08 and the
// web registry together.
const (
	// Runner pools (internal/server/pool.go).
	RulePoolPayloadRequired          = "pool.payload.required"
	RulePoolNameDuplicate            = "pool.name.duplicate"
	RulePoolProviderUnsupported      = "pool.provider.unsupported"
	RulePoolProviderImmutable        = "pool.provider.immutable"
	RulePoolTargetRequired           = "pool.target.required"
	RulePoolScopeInvalid             = "pool.scope.invalid"
	RulePoolTargetURLInvalid         = "pool.target.url_invalid"
	RulePoolTargetScopeMismatch      = "pool.target.scope_mismatch"
	RulePoolAllowDockerRequired      = "pool.allow_docker.required"
	RulePoolMemorySwapRequiresMemory = "pool.memory_swap.requires_memory"
	RulePoolMemoryLimitParse         = "pool.memory_limit.parse"
	RulePoolMemorySwapParse          = "pool.memory_swap.parse"
	RulePoolMemorySwapGTEMemory      = "pool.memory_swap.gte_memory"
	RulePoolPollFallbackUnsupported  = "pool.poll_fallback.unsupported"
	RulePoolRenovateCronInvalid      = "pool.renovate.cron_invalid"
	RulePoolAuthProfileNotFound      = "pool.auth_profile.not_found"

	// Auth profiles (internal/server/auth_profile.go).
	RuleAuthProfilePayloadRequired          = "auth_profile.payload.required"
	RuleAuthProfileNameRequired             = "auth_profile.name.required"
	RuleAuthProfileNameDuplicate            = "auth_profile.name.duplicate"
	RuleAuthProfileCredentialInvalid        = "auth_profile.credential.invalid"
	RuleAuthProfilePrivateKeyChangeRequired = "auth_profile.private_key.change_required"
	RuleAuthProfileTokenChangeRequired      = "auth_profile.token.change_required"

	// Local auth + app settings (internal/server/auth.go, onboarding.go).
	// These are trim-aware presence checks: min_len sees the raw wire string,
	// but handlers store trimmed values, so whitespace-only input would
	// otherwise become an empty stored value.
	RuleAuthUsernameRequired = "auth.username.required"
	// ChangePassword (internal/server/auth.go): the current-password check
	// runs a bcrypt comparison whose failure is surfaced as a field-attached
	// violation so form clients can mark the field (docs/30 §5.3 channel).
	RuleAuthPasswordCurrentMismatch = "auth.password.current_mismatch"
	RuleAppSettingKeyRequired       = "app_setting.key.required"
	// UserService role writes (internal/server/users.go): the role is an
	// application vocabulary (admin|viewer), not a free string, so a bad
	// value is a typed field violation on the role field.
	RuleAuthRoleInvalid = "auth.role.invalid"
)

// newViolation builds one buf.validate.Violation: a stable rule id from the
// registry above, a user-facing message, and a dotted field path
// ("pool.memory_limit" becomes two structured path elements) so clients can
// attach the rejection to a form field.
func newViolation(ruleID, fieldPath, format string, args ...any) *validatev1.Violation {
	segments := strings.Split(fieldPath, ".")
	elements := make([]*validatev1.FieldPathElement, 0, len(segments))
	for _, segment := range segments {
		elements = append(elements, &validatev1.FieldPathElement{
			FieldName: proto.String(segment),
		})
	}
	return &validatev1.Violation{
		RuleId:  proto.String(ruleID),
		Field:   &validatev1.FieldPath{Elements: elements},
		Message: proto.String(fmt.Sprintf(format, args...)),
	}
}

// invalidArgument returns a CodeInvalidArgument connect error carrying the
// violations as a typed buf.validate.Violations detail. The human-readable
// message joins the violation texts: clients that ignore details keep today's
// banner behavior (docs/30 §5.3 wire compatibility).
func invalidArgument(violations ...*validatev1.Violation) error {
	return violationsWithCode(connect.CodeInvalidArgument, violations)
}

// alreadyExists is invalidArgument's sibling for uniqueness violations: same
// detail channel, preserved CodeAlreadyExists semantics.
func alreadyExists(violations ...*validatev1.Violation) error {
	return violationsWithCode(connect.CodeAlreadyExists, violations)
}

func violationsWithCode(code connect.Code, violations []*validatev1.Violation) error {
	if len(violations) == 0 {
		return connect.NewError(code, errors.New("request validation failed"))
	}
	cerr := connect.NewError(code, errors.New(violationSummary(violations)))
	if detail, err := connect.NewErrorDetail(&validatev1.Violations{Violations: violations}); err == nil {
		cerr.AddDetail(detail)
	}
	return cerr
}

// violationSummary renders the wire message from a violation set: user-facing
// texts joined with "; ", falling back to the rule id when a violation carries
// no message.
func violationSummary(violations []*validatev1.Violation) string {
	texts := make([]string, 0, len(violations))
	for _, violation := range violations {
		if message := violation.GetMessage(); message != "" {
			texts = append(texts, message)
			continue
		}
		texts = append(texts, violation.GetRuleId())
	}
	return strings.Join(texts, "; ")
}
