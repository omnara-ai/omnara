package toolcatalog

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestInteractionDestinationToolSchemasAndPolicies(t *testing.T) {
	t.Parallel()
	catalog, err := Default()
	require.NoError(t, err)
	target, err := publicid.Encode(publicid.KindIntegrationTarget, uuid.New())
	require.NoError(t, err)
	for _, name := range InteractionDestinationToolNames() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			entry, ok := catalog.Lookup(name)
			require.True(t, ok)
			require.True(t, entry.Implicit)
			require.NotEmpty(t, entry.Description)
			for _, mode := range []string{
				toolpermission.ModeAlwaysAllow, toolpermission.ModeAlwaysAsk, toolpermission.ModeAlwaysDeny,
			} {
				_, err := toolpermission.ValidateSelection(toolpermission.DefaultSelection(mode), entry.PermissionModes)
				require.NoError(t, err, "migrated explicit policies must retain their mode")
			}
			validator, err := jsonschema.Compile(entry.InputSchema)
			require.NoError(t, err)
			valid, invalid := []string{`{}`}, []string{`{"resource":"chat"}`, `null`}
			if name == ToolNameSetInteractionDestination {
				valid = []string{`{"destination":null}`,
					`{"destination":{"target_id":"` + target + `","resource":"chat"}}`}
				invalid = []string{
					`{}`, `{"destination":{}}`, `{"destination":"dashboard"}`,
					`{"destination":{"target_id":"` + target + `"}}`,
					`{"destination":{"target_id":"` + target + `","resource":""}}`,
					`{"destination":{"target_id":"` + uuid.NewString() + `","resource":"chat"}}`,
					`{"destination":{"target_id":"` + target + `","resource":"chat","connection_id":"other"}}`,
					`{"destination":null,"scope":{"kind":"channel","ref":"C123"}}`,
				}
			}
			for _, raw := range valid {
				require.NoError(t, validator.Validate([]byte(raw)), raw)
			}
			for _, raw := range invalid {
				require.Error(t, validator.Validate([]byte(raw)), raw)
			}
		})
	}
}
