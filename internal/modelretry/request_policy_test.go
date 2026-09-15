package modelretry

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
)

type providerReplayPolicyStoreStub struct {
	cutoff int64
	err    error
}

func (s *providerReplayPolicyStoreStub) GetProviderReplaySuppressionCutoff(
	context.Context,
	uuid.UUID,
	uuid.UUID,
	uuid.UUID,
) (int64, error) {
	return s.cutoff, s.err
}

func TestRequestPolicyForModelCall(t *testing.T) {
	t.Parallel()

	base := model.RequestPolicy{
		MaxOutputTokens: 4_096,
		ReasoningEffort: "model-specific-option",
	}
	store := &providerReplayPolicyStoreStub{cutoff: 73}
	got, err := RequestPolicyForModelCall(
		context.Background(),
		store,
		uuid.UUID{1},
		uuid.UUID{2},
		uuid.UUID{3},
		base,
	)
	if err != nil {
		t.Fatalf("resolve request policy: %v", err)
	}
	if got.ProviderReplayCutoffEventSequence != 73 ||
		got.MaxOutputTokens != base.MaxOutputTokens ||
		got.ReasoningEffort != base.ReasoningEffort {
		t.Fatalf("resolved policy = %+v, want base policy with replay cutoff 73", got)
	}
}

func TestRequestPolicyForModelCallPreservesLaterCutoff(t *testing.T) {
	t.Parallel()

	base := model.RequestPolicy{ProviderReplayCutoffEventSequence: 91}
	got, err := RequestPolicyForModelCall(
		context.Background(),
		&providerReplayPolicyStoreStub{cutoff: 73},
		uuid.UUID{1},
		uuid.UUID{2},
		uuid.UUID{3},
		base,
	)
	if err != nil {
		t.Fatalf("resolve request policy: %v", err)
	}
	if got.ProviderReplayCutoffEventSequence != 91 {
		t.Fatalf("resolved policy = %+v, want later replay cutoff preserved", got)
	}
}

func TestRequestPolicyForModelCallPropagatesLookupFailure(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("database unavailable")
	_, err := RequestPolicyForModelCall(
		context.Background(),
		&providerReplayPolicyStoreStub{err: wantErr},
		uuid.UUID{1},
		uuid.UUID{2},
		uuid.UUID{3},
		model.RequestPolicy{},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("resolve request policy error = %v, want %v", err, wantErr)
	}
}
