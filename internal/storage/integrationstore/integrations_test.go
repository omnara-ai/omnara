package integrationstore

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/testutil/integrationtest"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAcceptsReferencesToResolveAtRuntime(t *testing.T) {
	input := SaveIntegrationInput{
		OrgID: uuid.New(), ProjectID: uuid.New(), Name: "support", IntegrationKind: integrationdefinition.SlackThread,
		Settings: integrationtest.ChatSettings(uuid.New()),
	}
	normalized, err := normalizeIntegration(input)
	require.NoError(t, err)
	require.JSONEq(t, string(input.Settings), string(normalized.Settings))
}
