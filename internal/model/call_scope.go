package model

import "context"

type CallPurpose string

const (
	CallPurposeAgentTurn  CallPurpose = "agent_turn"
	CallPurposeCompaction CallPurpose = "compaction"
	CallPurposeUnknown    CallPurpose = "unknown"
)

// CallScope identifies the logical model call that a provider attempt belongs to.
type CallScope struct {
	Purpose            CallPurpose
	ModelCallContextID string
	Attempt            int
}

type callScopeKey struct{}

func WithCallScope(ctx context.Context, scope CallScope) context.Context {
	return context.WithValue(ctx, callScopeKey{}, scope)
}

func CallScopeFromContext(ctx context.Context) CallScope {
	scope, _ := ctx.Value(callScopeKey{}).(CallScope)
	if scope.Purpose == "" {
		scope.Purpose = CallPurposeUnknown
	}
	return scope
}
