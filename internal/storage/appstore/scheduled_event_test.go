package appstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/cronschedule"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/stretchr/testify/require"
)

func TestScheduledPlanKeepsAppAndConversationAuthority(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"valid", "wrong parent", "missing thread", "wrong address", "extra slot",
		"wrong slot name", "wrong app",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			profileID := uuid.New()
			profileRef, err := publicid.Encode(publicid.KindAgentProfile, profileID)
			require.NoError(t, err)
			settings, err := json.Marshal(appdefinition.ThreadScheduleSettings{
				AgentProfileID: profileRef, ChannelID: "300",
				OpeningMessageTemplate: "Daily update", MessageTemplate: "Summarize activity.",
			})
			require.NoError(t, err)
			launch := appstore.ScheduledAppEvent{
				TriggerID: uuid.New(), Settings: settings,
				Occurrence: cronschedule.Occurrence{Name: "Daily", DueAt: time.Now(), FiredAt: time.Now(), Timezone: "UTC"},
			}
			payload, err := json.Marshal(launch)
			require.NoError(t, err)
			receipt := appstore.AppInboxRecord{
				AppID:  uuid.New(),
				Source: appstore.AppInboxSourceScheduled, Payload: payload,
			}
			root := appdefinition.Scope{Discord: &appdefinition.DiscordScope{
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
			selection := appstore.InboxAppSelection{
				AppID: receipt.AppID, Slot: "scheduled",
				Address: appstore.ConversationAddress{Kind: kind, Ref: ref},
			}
			switch scenario {
			case "wrong address":
				selection.Address.Ref = "301:500"
			case "wrong slot name":
				selection.Slot = "other"
			case "wrong app":
				selection.AppID = uuid.New()
			}
			content, err := appdefinition.AppendInputContext("daily", root, json.RawMessage(`[{"type":"text","text":"Summarize activity."}]`))
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
			err = receipt.ValidateScheduledPlan(appstore.ProjectAppRecord{
				ID: receipt.AppID, ProjectID: receipt.ProjectID, Name: "daily", AppType: appdefinition.DiscordThread,
			}, raw)
			if scenario == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
