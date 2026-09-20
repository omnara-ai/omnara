package integrationstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestScheduledRootKeepsDiscordParentAndGuildAuthority(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"valid", "wrong parent", "wrong guild", "missing thread", "wrong address", "extra slot",
		"wrong slot name", "wrong app",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			launch := integrationstore.ScheduledAppLaunch{
				TriggerID: uuid.New(), ProfileID: uuid.New(), ConfigID: uuid.New(),
				TriggerName: "Daily", DueAt: time.Now(),
				Destination:    json.RawMessage(`{"guild_id":"100","channel_id":"300"}`),
				OpeningMessage: "Daily update", Message: "Summarize activity.",
			}
			payload, err := json.Marshal(launch)
			require.NoError(t, err)
			receipt := integrationstore.IntegrationInboxRecord{
				IntegrationInboxSummary: integrationstore.IntegrationInboxSummary{AppID: uuid.New()},
				Source:                  integrationstore.IntegrationInboxSourceScheduledLaunch, Payload: payload,
			}
			root := appdefinition.Scope{Discord: &appdefinition.DiscordScope{
				GuildID: "100", ChannelID: "300", ThreadID: "500",
			}}
			switch scenario {
			case "wrong parent":
				root.Discord.ChannelID = "301"
			case "wrong guild":
				root.Discord.GuildID = "101"
			case "missing thread":
				root.Discord.ThreadID = ""
			}
			kind, ref, err := root.Conversation()
			require.NoError(t, err)
			selection := integrationstore.InboxAppSelection{
				AppID: receipt.AppID, Slot: "scheduled",
				Address: integrationstore.ConversationAddress{Kind: kind, Ref: ref},
			}
			switch scenario {
			case "wrong address":
				selection.Address.Ref = "301:500"
			case "wrong slot name":
				selection.Slot = "other"
			case "wrong app":
				selection.AppID = uuid.New()
			}
			slot := map[string]any{"scope": root, "selection": selection}
			plan := map[string]any{"scheduled": slot}
			if scenario == "extra slot" {
				plan["another"] = slot
			}
			raw, err := json.Marshal(plan)
			require.NoError(t, err)
			got, err := receipt.ScheduledRoot(appdefinition.ProviderDiscord, raw)
			if scenario == "valid" {
				require.NoError(t, err)
				require.Equal(t, root, got)
			} else {
				require.Error(t, err)
			}
		})
	}
}
