package server

import (
	"context"
	"errors"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
)

// NewValidationInterceptor enforces the protovalidate annotations declared on
// request messages (class A rules, docs/30 §5.1) on every unary RPC
// (docs/30 §5.3). Rules live in the proto descriptors — annotating a message
// turns enforcement on for every procedure that takes it, and messages without
// annotations validate trivially. Streaming RPCs are out of scope: all
// mutating RPCs are unary (docs/02) and streamed request messages carry no
// rule-bearing free-form input.
//
// The interceptor runs innermost (after authentication), so unauthenticated
// callers get the auth code on protected RPCs rather than rule feedback.
func NewValidationInterceptor() connect.Interceptor {
	return &validationInterceptor{}
}

type validationInterceptor struct{}

// WrapUnary validates the request message before the handler runs:
//
//   - annotated-rule failures become CodeInvalidArgument with the
//     buf.validate.Violations detail (same channel as class B, docs/08);
//   - anything that prevents evaluation (CEL compilation, envelope failures)
//     is fail-closed (docs/30 §5.3): the request never passes unvalidated, the
//     cause is logged server-side, and the wire message stays generic so rule
//     internals never leak.
func (v *validationInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		message, ok := req.Any().(proto.Message)
		if !ok {
			return nil, connect.NewError(connect.CodeInternal, errors.New("request validation failed"))
		}
		err := protovalidate.Validate(message)
		if err == nil {
			return next(ctx, req)
		}
		var violationsErr *protovalidate.ValidationError
		if errors.As(err, &violationsErr) {
			violations := violationsErr.ToProto().GetViolations()
			cerr := connect.NewError(connect.CodeInvalidArgument, errors.New(violationSummary(violations)))
			if detail, detailErr := connect.NewErrorDetail(violationsErr.ToProto()); detailErr == nil {
				cerr.AddDetail(detail)
			}
			return nil, cerr
		}
		logger.Error("protovalidate evaluation failed",
			"procedure", req.Spec().Procedure,
			"error", err)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("request validation failed"))
	}
}

func (v *validationInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next // server-side interceptor; no client use today
}

func (v *validationInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next // unary-only by design; see NewValidationInterceptor
}
