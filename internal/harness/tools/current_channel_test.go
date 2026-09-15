package tools

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestSetCurrentChannelInput(t *testing.T) {
	id := integrationToolTestID("current-channel")
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, id)
	require.NoError(t, err)
	for _, value := range []struct {
		input string
		id    uuid.UUID
		valid bool
	}{
		{`{"channel_id":"` + channelID + `"}`, id, true},
		{`{"channel_id":null}`, uuid.Nil, true},
		{`{}`, uuid.Nil, false},
		{`{"channel_id":""}`, uuid.Nil, false},
		{`{"channel_id":false}`, uuid.Nil, false},
		{`{"channel_id":null,"extra":true}`, uuid.Nil, false},
	} {
		t.Run(value.input, func(t *testing.T) {
			got, err := parseSetCurrentChannelRequest(json.RawMessage(value.input))
			if value.valid {
				require.NoError(t, err)
				require.Equal(t, value.id, got)
			} else {
				require.Error(t, err)
			}
		})
	}
	catalog, err := toolcatalog.Default()
	require.NoError(t, err)
	entry, found := catalog.Lookup(toolcatalog.ToolNameSetCurrentChannel)
	require.True(t, found)
	require.True(t, toolcatalog.IsBindingManagedTool(entry.Name))
	require.Len(t, entry.PermissionModes, 1, "changing the approval destination must not itself wait for approval")
}
