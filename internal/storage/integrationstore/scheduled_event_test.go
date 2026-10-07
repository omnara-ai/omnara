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
		"valid", "wrong parent", "missing thread", "wrong address", "extra recipient",
		"wrong recipient name", "wrong integration",
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
			claim := integrationstore.InboxLaunchClaim{
				IntegrationID: receipt.IntegrationID, LaunchKey: integrationdefinition.ScheduledLaunchKey,
				Address: integrationstore.ConversationAddress{Kind: kind, Ref: ref},
			}
			switch scenario {
			case "wrong address":
				claim.Address.Ref = "301:500"
			case "wrong recipient name":
				claim.LaunchKey = "other"
			case "wrong integration":
				claim.IntegrationID = uuid.New()
			}
			content, err := integrationdefinition.AppendInputContext("daily", root, json.RawMessage(`[{"type":"text","text":"Summarize activity."}]`))
			require.NoError(t, err)
			recipient := map[string]any{
				"launch_claim": claim,
				"launch":       map[string]any{"profile_id": profileID},
			}
			plan := map[string]any{integrationdefinition.ScheduledLaunchKey: recipient}
			if scenario == "extra recipient" {
				plan["another"] = recipient
			}
			raw, err := json.Marshal(map[string]any{
				"message": map[string]any{"scope": root, "content_blocks": content}, "recipients": plan,
			})
			require.NoError(t, err)
			err = receipt.ValidateScheduledPlan(integrationstore.IntegrationRecord{

				ID:              receipt.IntegrationID,
				ProjectID:       receipt.ProjectID,
				Name:            "daily",
				IntegrationKind: integrationdefinition.DiscordThread,
			}, raw)
			if scenario == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
