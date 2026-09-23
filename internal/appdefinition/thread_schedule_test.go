package appdefinition

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/cronschedule"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestThreadSchedulesPublishAndValidateTheirInputs(t *testing.T) {
	t.Parallel()
	profileID := uuid.New()
	profile, err := publicid.Encode(publicid.KindAgentProfile, profileID)
	require.NoError(t, err)
	for _, app := range []Type{SlackThread, DiscordThread} {
		t.Run(string(app), func(t *testing.T) {
			t.Parallel()
			channel := "C123"
			if app == DiscordThread {
				channel = "123"
			}
			fields := map[string]any{
				"agent_profile_id": profile, "channel_id": channel,
				"opening_message_template": "{{.trigger.name}} — {{.trigger.local_date}}",
				"message_template":         "Review for {{.trigger.local_date}}.",
			}
			encode := func() json.RawMessage {
				t.Helper()
				raw, err := json.Marshal(fields)
				require.NoError(t, err)
				return raw
			}
			raw, err := ValidateScheduleSettings(app, encode())
			require.NoError(t, err)
			occurrence := cronschedule.Occurrence{
				Name: "Daily", DueAt: time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC),
				FiredAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC), Timezone: "America/Los_Angeles",
			}
			launch, err := PrepareThreadSchedule(app, raw, occurrence)
			require.NoError(t, err)
			require.Equal(t, profileID, launch.ProfileID)
			require.Equal(t, channel, launch.ChannelID)
			require.Equal(t, "Daily — 2026-09-21", launch.OpeningMessage)
			require.Equal(t, "Review for 2026-09-21.", launch.Message)
			for _, name := range []string{"guild_id", "thread_id", "unexpected"} {
				fields[name] = "123"
				_, err := ValidateScheduleSettings(app, encode())
				require.Error(t, err)
				delete(fields, name)
			}
			for _, invalid := range []string{"", "not-a-profile", channel} {
				fields["agent_profile_id"] = invalid
				_, err := ValidateScheduleSettings(app, encode())
				require.Error(t, err)
			}
			fields["agent_profile_id"] = profile
			for _, invalid := range []string{
				"", strings.Repeat("🚀", 2001), `{{printf "%1024s" "a"}}{{printf "%1024s" "b"}}`, "Daily\x00",
			} {
				fields["opening_message_template"] = invalid
				_, err := ValidateScheduleSettings(app, encode())
				require.Error(t, err)
			}
		})
	}
	_, err = ValidateScheduleSettings(GitHubPR, json.RawMessage(`{}`))
	require.Error(t, err)
}

func TestOpeningMessageBounds(t *testing.T) {
	data := cronschedule.MessageData("Daily", time.Now(), nil)
	_, err := renderThreadOpening(strings.Repeat("🚀", 2000), data)
	require.NoError(t, err)
	_, err = renderThreadOpening(strings.Repeat("🚀", 2001), data)
	require.Error(t, err)
	_, err = renderThreadOpening(`{{printf "%1024s" "a"}}{{printf "%1024s" "b"}}`, data)
	require.Error(t, err)
	_, err = renderThreadOpening("   ", data)
	require.Error(t, err)
	_, err = renderThreadOpening(`{{if false}}heading{{end}}`, data)
	require.Error(t, err)
}
