package model_test

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/stretchr/testify/require"
)

type unsharedContentNormalizer struct{}

func (unsharedContentNormalizer) Normalize(bundle modelcontext.Bundle) error {
	return (modelcontext.ProjectionNormalizer{}).Normalize(bundle)
}

func TestBuiltContentPreparationPreservesBodyFingerprintAndClamp(t *testing.T) {
	clients := adapterWireClients(wireClientConfig{capabilities: model.Capabilities{
		ContextWindowTokens: 50000, InputModalities: []string{"text", "image", "file"},
	}})
	for _, adapter := range clients {
		t.Run(adapter.name, func(t *testing.T) {
			store, input, revision := workerCPUFixture(t, 30)
			input.MediaProjector = model.MediaProjectorForClient(adapter.client)
			shared, err := (modelcontext.Builder{Store: store}).Build(t.Context(), input)
			require.NoError(t, err)
			unsharedBuilder := modelcontext.Builder{Store: store, Normalizer: unsharedContentNormalizer{}}
			unshared, err := unsharedBuilder.Build(t.Context(), input)
			require.NoError(t, err)
			shared.ConfiguredModelRevisionID = revision
			unshared.ConfiguredModelRevisionID = revision
			for _, output := range []int{1024, 49000} {
				policy := model.RequestPolicy{MaxOutputTokens: output}
				expected, err := model.PrepareForSend(t.Context(), adapter.client, model.PrepareForSendInput{
					Context: unshared, Policy: policy,
				})
				require.NoError(t, err)
				actual, err := model.PrepareForSend(t.Context(), adapter.client, model.PrepareForSendInput{
					Context: shared, Policy: policy,
				})
				require.NoError(t, err)
				require.Equal(t, expected, actual)
				if output == 49000 {
					require.Less(t, actual.MaxOutputTokens, output)
				}
			}
		})
	}
}
