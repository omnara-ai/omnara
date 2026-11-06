package slack

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMessageParticipantPolicy(t *testing.T) {
	for _, tc := range []struct {
		name          string
		team          string
		channel       string
		channelType   string
		shared, allow bool
		want          bool
	}{
		{"local channel", "T123", "C123", "channel", false, false, true},
		{"local DM", "T123", "D123", "im", false, false, true},
		{"external disabled", "T_REMOTE", "C123", "channel", true, false, false},
		{"external enabled", "T_REMOTE", "C123", "channel", true, true, true},
		{"mention without channel type", "T_REMOTE", "C123", "", true, true, true},
		{"shared private channel", "T_REMOTE", "C123", "group", true, true, true},
		{"not shared", "T_REMOTE", "C123", "channel", false, true, false},
		{"external DM", "T_REMOTE", "D123", "im", true, true, false},
		{"external group DM", "T_REMOTE", "G123", "mpim", true, true, false},
		{"inconsistent group DM", "T_REMOTE", "C123", "mpim", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, teamField := range []string{"team", "source_team", "user_team"} {
				event := Event{Type: "app_mention", Channel: tc.channel, ChannelType: tc.channelType}
				switch teamField {
				case "team":
					event.Team = tc.team
				case "source_team":
					event.SourceTeam = tc.team
				case "user_team":
					event.UserTeam = tc.team
				}
				envelope := EventsEnvelope{Event: event, IsExtSharedChannel: tc.shared}
				require.Equal(t, tc.want, MessageParticipantAllowed("T123", envelope, tc.allow), teamField)
			}
		})
	}
}

func TestExternalProfileSelectionDoesNotAuthorizeAgentInteractions(t *testing.T) {
	identity := Identity{AppID: "A123", WorkspaceID: "T123", BotUserID: "U_BOT"}
	envelope := ActionsEnvelope{APIAppID: "A123", User: ActionsUser{ID: "U_REMOTE", TeamID: "T_REMOTE"}}
	envelope.Team.ID, envelope.Channel.ID = "T123", "C123"
	source := EventsEnvelope{IsExtSharedChannel: true, Event: Event{Type: "app_mention", Channel: "C123", Team: "T123"}}
	require.True(t, ValidateActionInstallation(identity, envelope))
	require.False(t, ProfileChoiceParticipantAllowed("T123", source, envelope, false))
	require.True(t, ProfileChoiceParticipantAllowed("T123", source, envelope, true))
	require.False(t, ValidateActionIdentity(identity, envelope), "opt-in never authorizes agent approvals")
	for _, channel := range []string{"", "D123", "G123"} {
		envelope.Channel.ID = channel
		require.False(t, ProfileChoiceParticipantAllowed("T123", source, envelope, true))
	}
	envelope.Team.ID = "T_REMOTE"
	require.False(t, ValidateActionInstallation(identity, envelope))
	envelope.Team.ID, envelope.APIAppID = "T123", "A_OTHER"
	require.False(t, ValidateActionInstallation(identity, envelope))
}

func TestProfileChoiceCannotReviveExternalRequestAfterOptOut(t *testing.T) {
	source := EventsEnvelope{
		IsExtSharedChannel: true, Event: Event{Type: "app_mention", Channel: "C123", Team: "T_REMOTE"},
	}
	action := ActionsEnvelope{User: ActionsUser{ID: "U_LOCAL", TeamID: "T123"}}
	action.Channel.ID = "C123"
	require.True(t, ProfileChoiceParticipantAllowed("T123", source, action, true))
	require.False(t, ProfileChoiceParticipantAllowed("T123", source, action, false))
	source.Event.Team = "T123"
	action.User.TeamID = "T_REMOTE"
	source.IsExtSharedChannel = false
	require.False(t, ProfileChoiceParticipantAllowed("T123", source, action, true))
}
