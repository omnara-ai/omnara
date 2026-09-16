//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestChannelConnectorWorkflowInlineParentRegistrationAndReplay(t *testing.T) {
	t.Parallel()
	f := newChannelWorkflowHTTPFixture(t)
	body := f.delivery(t, "inline-parent-first")
	parent := publishWorkflowHTTPParent(t, f)
	body.Target.Parent = &parent
	path := f.path(t, "workflows/deliver")
	first := f.post(t, path, workflowHTTPJSON(t, body), f.token, http.StatusOK)
	require.Equal(t, true, first["created_agent"])
	require.Equal(t, true, first["created_input"])
	storedParent := requireWorkflowHTTPParent(t, f, first, parent, 1)

	// Receipt replay and a distinct receipt for the same semantic input must
	// return the accepted child, without registering the replacement parent.
	for _, newReceipt := range []bool{false, true} {
		replay := body
		if newReceipt {
			replay = f.delivery(t, "inline-parent-redelivery")
			replay.InputKey = body.InputKey
		}
		changedParent := parent
		changedParent.ProviderRef = "unaccepted-parent"
		changedParent.ProviderMetadata = json.RawMessage(`{"sequence":7}`)
		replay.Target.Parent = &changedParent
		replayed := f.post(t, path, workflowHTTPJSON(t, replay), f.token, http.StatusOK)
		for _, field := range []string{"agent_id", "agent_input_id", "channel_id", "binding_id", "content_blocks"} {
			require.Equal(t, first[field], replayed[field], field)
		}
		require.Equal(t, false, replayed["created_agent"])
		require.Equal(t, false, replayed["created_input"])
		reused := requireWorkflowHTTPParent(t, f, replayed, parent, 1)
		require.Equal(t, storedParent.ID, reused.ID)
	}

	// A new input can name the already registered parent directly. This does
	// not reparent the child, duplicate its binding, or grant parent access.
	next := f.delivery(t, "existing-parent-follow-up")
	parentID := testPublicID(t, publicid.KindIntegrationTarget, storedParent.ID)
	next.Target.ParentChannelId = &parentID
	followup := f.post(t, path, workflowHTTPJSON(t, next), f.token, http.StatusOK)
	require.Equal(t, false, followup["created_agent"])
	require.Equal(t, true, followup["created_input"])
	require.NotEqual(t, first["agent_input_id"], followup["agent_input_id"])
	for _, field := range []string{"agent_id", "channel_id", "binding_id"} {
		require.Equal(t, first[field], followup[field], field)
	}
	reused := requireWorkflowHTTPParent(t, f, followup, parent, 2)
	require.Equal(t, storedParent.ID, reused.ID)
}

func TestChannelConnectorWorkflowRejectsInvalidParentBeforeEffects(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"both_parent_forms", "duplicate_metadata", "nested_escaped_duplicate_metadata"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newChannelWorkflowHTTPFixture(t)
			body := f.delivery(t, "invalid-parent")
			parent := publishWorkflowHTTPParent(t, f)
			body.Target.Parent = &parent
			switch name {
			case "both_parent_forms":
				id := testPublicID(t, publicid.KindIntegrationTarget, uuid.New())
				body.Target.ParentChannelId = &id
			case "duplicate_metadata":
				parent.ProviderMetadata = json.RawMessage(`{"key":1,"key":2}`)
			case "nested_escaped_duplicate_metadata":
				parent.ProviderMetadata = json.RawMessage(`{"nested":[{"key":1,"\u006bey":2}]}`)
			}
			raw := workflowHTTPJSON(t, body)
			if name != "both_parent_forms" {
				require.Contains(t, raw, string(parent.ProviderMetadata), "send ambiguous bytes through the real HTTP decoder")
			}
			failed := f.post(t, f.path(t, "workflows/deliver"), raw, f.token, http.StatusBadRequest)
			require.Equal(t, string(openapi.ErrorCodeInvalidRequest), failed["code"])
			requireWorkflowHTTPParentRejectionHasNoEffects(t, f)

			// Correcting the same still-leased receipt remains admissible: the
			// rejected request must not leave an outcome or consume the input key.
			body.Target.ParentChannelId = nil
			parent.ProviderMetadata = json.RawMessage(`{"sequence":9007199254740993}`)
			accepted := f.post(t, f.path(t, "workflows/deliver"), workflowHTTPJSON(t, body), f.token, http.StatusOK)
			require.Equal(t, true, accepted["created_agent"])
			require.Equal(t, true, accepted["created_input"])
			requireWorkflowHTTPParent(t, f, accepted, parent, 1)
		})
	}
}

func publishWorkflowHTTPParent(t *testing.T, f channelWorkflowHTTPFixture) openapi.ChannelRegistrationParent {
	t.Helper()
	definition := workflowDefinitionRequest()
	definition.ImplementationKey = "parent"
	definition.Kind = openapi.ChannelKindDiscordChannel
	published := f.post(t, f.path(t, "channel-definitions/publish"),
		workflowHTTPJSON(t, definition), f.token, http.StatusOK)
	return openapi.ChannelRegistrationParent{
		DefinitionId: channelReceiptString(t, published, "id"), ProviderRef: "parent-one", ProviderRefKind: "channel",
		ProviderMetadata: json.RawMessage(`{"sequence":9007199254740993}`),
	}
}

func requireWorkflowHTTPParent(
	t *testing.T, f channelWorkflowHTTPFixture, result map[string]any,
	input openapi.ChannelRegistrationParent, wantInputs int,
) integrationstore.IntegrationTargetRecord {
	t.Helper()
	ctx := t.Context()
	store := f.project.Store.Integrations()
	parent, err := store.GetIntegrationTargetByProviderRef(ctx, f.install.ProjectID, f.install.ID, input.ProviderRef)
	require.NoError(t, err)
	childID := mustPublicHTTPID(t, publicid.KindIntegrationTarget, channelReceiptString(t, result, "channel_id"))
	child, err := store.GetIntegrationTarget(ctx, f.install.ProjectID, childID)
	require.NoError(t, err)
	require.NotEqual(t, parent.ID, child.ID)
	require.Equal(t, parent.ID, child.ParentChannelID)
	require.Equal(t, uuid.Nil, parent.ParentChannelID)
	require.Equal(t, mustPublicHTTPID(t, publicid.KindChannelDefinition, input.DefinitionId), parent.ChannelDefinitionID)
	require.Equal(t, "channel", parent.ProviderRefKind)
	for _, target := range []integrationstore.IntegrationTargetRecord{parent, child} {
		require.Equal(t, f.install.ProjectID, target.ProjectID)
		require.Equal(t, f.install.ID, target.IntegrationInstallID)
	}
	agentID := mustPublicHTTPID(t, publicid.KindAgent, channelReceiptString(t, result, "agent_id"))
	inputID := mustPublicHTTPID(t, publicid.KindAgentInput, channelReceiptString(t, result, "agent_input_id"))
	bindingID := mustPublicHTTPID(t, publicid.KindIntegrationBinding, channelReceiptString(t, result, "binding_id"))
	var targets, parentBindings, childBindings, inputs int
	var exactNumber string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM integration_targets WHERE integration_install_id=$1),
  (SELECT count(*) FROM integration_target_bindings WHERE integration_target_id=$2),
  (SELECT count(*) FROM integration_target_bindings WHERE integration_target_id=$3),
  (SELECT count(*) FROM agent_inputs WHERE agent_id=$4 AND input_kind='content'),
  (SELECT provider_metadata->>'sequence' FROM integration_targets WHERE id=$2)`,
		f.install.ID, parent.ID, child.ID, agentID).
		Scan(&targets, &parentBindings, &childBindings, &inputs, &exactNumber))
	require.Equal(t, 2, targets, "replay must not register another parent")
	require.Zero(t, parentBindings, "parent registration grants no implicit receive/read/send authority")
	require.Equal(t, 1, childBindings)
	require.Equal(t, wantInputs, inputs)
	require.Equal(t, "9007199254740993", exactNumber, "metadata must survive HTTP and PostgreSQL without float rounding")
	var inputChannel, inputBinding uuid.UUID
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT input.integration_target_id,
  input.integration_target_binding_id FROM agent_inputs input
WHERE input.id=$1 AND input.agent_id=$2`, inputID, agentID).Scan(&inputChannel, &inputBinding))
	require.Equal(t, child.ID, inputChannel)
	require.Equal(t, bindingID, inputBinding)
	return parent
}

func requireWorkflowHTTPParentRejectionHasNoEffects(t *testing.T, f channelWorkflowHTTPFixture) {
	t.Helper()
	var agents, workflows, targets, bindings, inputs, artifacts, outcomes int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT
  (SELECT count(*) FROM agents), (SELECT count(*) FROM integration_workflows),
  (SELECT count(*) FROM integration_targets), (SELECT count(*) FROM integration_target_bindings),
  (SELECT count(*) FROM agent_inputs), (SELECT count(*) FROM artifacts),
  (SELECT count(*) FROM integration_event_outcomes)`).
		Scan(&agents, &workflows, &targets, &bindings, &inputs, &artifacts, &outcomes))
	require.Equal(t, []int{0, 0, 0, 0, 0, 0, 0}, []int{agents, workflows, targets, bindings, inputs, artifacts, outcomes})
	uploads, deletions, retained := f.blobs.snapshot()
	require.Empty(t, uploads, "reject malformed parent before preparing any artifact")
	require.Empty(t, deletions)
	require.Zero(t, retained)
}
