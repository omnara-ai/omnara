package integrationstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/cronschedule"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestScheduledPlanKeepsIntegrationAndConversationAuthority(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"valid", "wrong parent", "missing thread", "wrong address", "extra slot",
		"wrong slot name", "wrong integration",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			profileID := uuid.New()
			profileRef, err := publicid.Encode(publicid.KindAgentProfile, profileID)
			require.NoError(t, err)
			settings, err := json.Marshal(integrationdefinition.ThreadScheduleSettings{
				AgentProfileID: profileRef, ChannelID: "300",
				OpeningMessageTemplate: "Daily update", MessageTemplate: "Summarize activity.",
			})
			require.NoError(t, err)
			launch := integrationstore.ScheduledIntegrationEvent{
				TriggerID: uuid.New(), Settings: settings,
				Occurrence: cronschedule.Occurrence{Name: "Daily", DueAt: time.Now(), FiredAt: time.Now(), Timezone: "UTC"},
			}
			payload, err := json.Marshal(launch)
			require.NoError(t, err)
			receipt := integrationstore.IntegrationInboxRecord{
				IntegrationID: uuid.New(),
				Source:        integrationstore.IntegrationInboxSourceScheduled, Payload: payload,
			}
			root := integrationdefinition.Scope{Discord: &integrationdefinition.DiscordScope{
				GuildID: "100", ChannelID: "300", ThreadID: "500",
			}}
			switch scenario {
			case "wrong parent":
				root.Discord.ChannelID = "301"
			case "missing thread":
				root.Discord.ThreadID = ""
			}
			kind, ref, err := root.Conversation()
			require.NoError(t, err)
			selection := integrationstore.InboxIntegrationSelection{
				IntegrationID: receipt.IntegrationID, Slot: "scheduled",
				Address: integrationstore.ConversationAddress{Kind: kind, Ref: ref},
			}
			switch scenario {
			case "wrong address":
				selection.Address.Ref = "301:500"
			case "wrong slot name":
				selection.Slot = "other"
			case "wrong integration":
				selection.IntegrationID = uuid.New()
			}
			content, err := integrationdefinition.AppendInputContext("daily", root, json.RawMessage(`[{"type":"text","text":"Summarize activity."}]`))
			require.NoError(t, err)
			slot := map[string]any{
				"scope": root, "selection": selection,
				"launch": map[string]any{"profile_id": profileID, "initial_input": map[string]any{"content_blocks": content}},
			}
			plan := map[string]any{"scheduled": slot}
			if scenario == "extra slot" {
				plan["another"] = slot
			}
			raw, err := json.Marshal(plan)
			require.NoError(t, err)
			err = receipt.ValidateScheduledPlan(integrationstore.ProjectIntegrationRecord{

				ID:              receipt.IntegrationID,
				ProjectID:       receipt.ProjectID,
				Name:            "daily",
				IntegrationType: integrationdefinition.DiscordThread,
			}, raw)
			if scenario == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
