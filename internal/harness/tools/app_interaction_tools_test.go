package tools

import (
	"encoding/json"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
	"testing"
)

func interactionImplementationForTest(t *testing.T, name string) toolImplementation {
	t.Helper()
	catalog, err := toolcatalog.Default()
	require.NoError(t, err)
	entry, found := catalog.Lookup(name)
	require.True(t, found)
	validator, err := jsonschema.Compile(entry.InputSchema)
	require.NoError(t, err)
	for _, registration := range interactionToolRegistrations() {
		if registration.name == name {
			require.NoError(
				t,
				validatePermissionModeHandlers(registration.permissionModes, entry.PermissionModes),
			)
			return toolImplementation{
				toolRegistration:     registration,
				inputSchemaValidator: validator,
			}
		}
	}
	t.Fatalf("missing interaction tool registration %s", name)
	return toolImplementation{}
}

func TestInteractionToolRegistrationValidation(t *testing.T) {
	t.Parallel()
	for _, name := range toolcatalog.InteractionHandlerToolNames() {
		registered := 0
		for _, registration := range builtInToolRegistrations() {
			if registration.name == name {
				registered++
			}
		}
		require.Equal(t, 1, registered)
		implementation := interactionImplementationForTest(t, name)
		require.NotNil(t, implementation.handler.Transactional)
		require.Nil(t, implementation.handler.Async)
		valid := json.RawMessage(`{}`)
		if name == toolcatalog.ToolNameSetInteractionHandler {
			valid = json.RawMessage(`{"handler":null,"args":{}}`)
		}
		require.NoError(t, implementation.validateInput(valid))
		require.Error(
			t,
			implementation.validateInput(json.RawMessage(`{"connection_id":"inject"}`)),
		)
	}
}
