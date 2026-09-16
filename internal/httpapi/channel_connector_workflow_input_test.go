package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestChannelWorkflowInputParent(t *testing.T) {
	definitionID, parentDefinitionID := uuid.New(), uuid.New()
	body := openapi.DeliverChannelConnectorWorkflowRequest{
		RouteId: testPublicID(t, publicid.KindIntegrationRoute, uuid.New()), InstanceKey: "conversation",
		InputKey: "message", Author: openapi.ChannelWorkflowAuthor{Ref: "author"},
		Receipt: openapi.ChannelEventLease{
			ReceiptId:  testPublicID(t, publicid.KindIntegrationEventReceipt, uuid.New()),
			LeaseToken: uuid.New(), LeaseGeneration: 1,
		},
		Target: openapi.ChannelRegistrationTarget{
			DefinitionId: testPublicID(t, publicid.KindChannelDefinition, definitionID),
			ProviderRef:  "child", ProviderRefKind: "thread",
			Parent: &openapi.ChannelRegistrationParent{
				DefinitionId: testPublicID(t, publicid.KindChannelDefinition, parentDefinitionID),
				ProviderRef:  "parent", ProviderRefKind: "channel",
				ProviderMetadata: json.RawMessage(`{"number":9007199254740993}`),
			},
		},
	}
	input, _, err := channelWorkflowDeliveryInput(body)
	require.NoError(t, err)
	require.Equal(t, definitionID, input.Target.ChannelDefinitionID)
	require.NotNil(t, input.ParentTarget)
	require.Equal(t, parentDefinitionID, input.ParentTarget.ChannelDefinitionID)
	require.Equal(t, "parent", input.ParentTarget.ProviderRef)
	require.Equal(t, `{"number":9007199254740993}`, string(input.ParentTarget.ProviderMetadata))
	require.Equal(t, uuid.Nil, input.ParentTarget.ProjectID, "scope comes from the authenticated receipt")
	require.Equal(t, uuid.Nil, input.ParentTarget.IntegrationInstallID)

	parentID := testPublicID(t, publicid.KindIntegrationTarget, uuid.New())
	body.Target.ParentChannelId = &parentID
	_, _, err = channelWorkflowDeliveryInput(body)
	require.ErrorContains(t, err, "either parent or parent_channel_id")
	body.Target.Parent = nil
	input, _, err = channelWorkflowDeliveryInput(body)
	require.NoError(t, err)
	require.Nil(t, input.ParentTarget)
	require.NotEqual(t, uuid.Nil, input.Target.ParentChannelID)
}

func TestChannelWorkflowAddressValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*openapi.ChannelRegistrationParent)
	}{
		{"wrong definition ID", func(p *openapi.ChannelRegistrationParent) { p.DefinitionId = "itgt_invalid" }},
		{"empty address", func(p *openapi.ChannelRegistrationParent) { p.ProviderRef = " " }},
		{"address too long", func(p *openapi.ChannelRegistrationParent) { p.ProviderRef = strings.Repeat("x", 513) }},
		{"address NUL", func(p *openapi.ChannelRegistrationParent) { p.ProviderRef = "address\x00" }},
		{"kind too long", func(p *openapi.ChannelRegistrationParent) { p.ProviderRefKind = strings.Repeat("x", 129) }},
		{"name too long", func(p *openapi.ChannelRegistrationParent) {
			name := strings.Repeat("x", 513)
			p.DisplayName = &name
		}},
		{"metadata duplicate keys", func(p *openapi.ChannelRegistrationParent) { p.ProviderMetadata = json.RawMessage(`{"x":1,"x":2}`) }},
		{"metadata NUL", func(p *openapi.ChannelRegistrationParent) { p.ProviderMetadata = json.RawMessage(`{"x":"\u0000"}`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := openapi.ChannelRegistrationParent{
				DefinitionId: testPublicID(t, publicid.KindChannelDefinition, uuid.New()),
				ProviderRef:  "channel", ProviderRefKind: "channel",
			}
			tc.mutate(&parent)
			_, err := channelRegistrationAddress(parent)
			require.Error(t, err)
		})
	}
}

func TestDecodeChannelRegistrationTargetUsesExactFields(t *testing.T) {
	for _, raw := range []string{
		`{"provider_ref":"C1", "Provider_Ref":"C2"}`,
		`{"Provider_Ref":"C2"}`,
		`{"parent":{"Provider_Ref":"C2"}}`,
		`{"parent":{"parent":{"provider_ref":"C2"}}}`,
		`{"parent":null}`,
		`{"parent_channel_id":null}`,
		`{"display_name":null}`,
		`{"provider_metadata":null}`,
		`{"provider_metadata":{"x":1,"x":2}}`,
	} {
		_, err := decodeChannelRegistrationTarget(json.RawMessage(raw))
		require.Error(t, err, raw)
	}
	decoded, err := decodeChannelRegistrationTarget(json.RawMessage(
		`{"provider_ref":"C1","provider_metadata":{"x":9007199254740993},"parent":{"provider_ref":"P1"}}`))
	require.NoError(t, err)
	require.Equal(t, "C1", decoded.ProviderRef)
	require.Equal(t, "P1", decoded.Parent.ProviderRef)
	require.Equal(t, `{"x":9007199254740993}`, string(decoded.ProviderMetadata))
}
