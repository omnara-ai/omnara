package modelretry

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type providerReplayPolicyStoreStub struct {
	rejections executionstore.ProviderReplayRejections
	err        error
}

func (s *providerReplayPolicyStoreStub) ListProviderReplayRejections(
	context.Context,
	uuid.UUID,
	uuid.UUID,
	uuid.UUID,
) (executionstore.ProviderReplayRejections, error) {
	return s.rejections, s.err
}

func sameModelRejection(cutoff int64) executionstore.ProviderReplayRejections {
	return executionstore.ProviderReplayRejections{
		ProviderModelSlug: "gpt-5.5",
		APIFormat:         modelprotocol.APIFormatOpenAIResponses,
		CutoffBySlug:      map[string]int64{"gpt-5.5": cutoff},
	}
}

func TestRequestPolicyForModelCall(t *testing.T) {
	t.Parallel()

	base := model.RequestPolicy{
		MaxOutputTokens: 4_096,
		ReasoningEffort: "model-specific-option",
	}
	store := &providerReplayPolicyStoreStub{rejections: sameModelRejection(73)}
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
		&providerReplayPolicyStoreStub{rejections: sameModelRejection(73)},
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

func TestRequestPolicyForModelCallFollowsRejectionsAcrossReplayCompatibleModels(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		rejections executionstore.ProviderReplayRejections
		want       int64
	}{
		{
			name: "claude model switch keeps the earlier rejection",
			rejections: executionstore.ProviderReplayRejections{
				ProviderModelSlug: "claude-opus-5-5",
				APIFormat:         modelprotocol.APIFormatAnthropicMessages,
				CutoffBySlug:      map[string]int64{"claude-sonnet-5-5": 40, "claude-opus-5-5": 20},
			},
			want: 40,
		},
		{
			name: "non-claude rejection on the same route does not apply",
			rejections: executionstore.ProviderReplayRejections{
				ProviderModelSlug: "claude-opus-5-5",
				APIFormat:         modelprotocol.APIFormatAnthropicMessages,
				CutoffBySlug:      map[string]int64{"minimax-m3": 40, "claude-opus-5-5": 20},
			},
			want: 20,
		},
		{
			name: "other formats only follow the same slug",
			rejections: executionstore.ProviderReplayRejections{
				ProviderModelSlug: "gpt-5.5",
				APIFormat:         modelprotocol.APIFormatOpenAIResponses,
				CutoffBySlug:      map[string]int64{"gpt-5.4": 40},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := RequestPolicyForModelCall(
				context.Background(),
				&providerReplayPolicyStoreStub{rejections: tc.rejections},
				uuid.UUID{1},
				uuid.UUID{2},
				uuid.UUID{3},
				model.RequestPolicy{},
			)
			if err != nil {
				t.Fatalf("resolve request policy: %v", err)
			}
			if got.ProviderReplayCutoffEventSequence != tc.want {
				t.Fatalf("replay cutoff = %d, want %d", got.ProviderReplayCutoffEventSequence, tc.want)
			}
		})
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
