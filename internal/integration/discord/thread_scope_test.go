package discord

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestThreadScopeOptionalParentStillChecksSuppliedScope(t *testing.T) {
	for _, test := range []struct {
		name    string
		scope   Scope
		channel Channel
		valid   bool
	}{
		{"thread only", Scope{ThreadID: "555"}, Channel{ID: "555", ParentID: "444", GuildID: "333", Type: 11}, true},
		{"matching guild", Scope{GuildID: "333", ThreadID: "555"},
			Channel{ID: "555", ParentID: "444", GuildID: "333", Type: 12}, true},
		{"matching parent", Scope{ChannelID: "444", ThreadID: "555"},
			Channel{ID: "555", ParentID: "444", GuildID: "333", Type: 10}, true},
		{"wrong parent", Scope{ChannelID: "444", ThreadID: "555"},
			Channel{ID: "555", ParentID: "999", GuildID: "333", Type: 11}, false},
		{"wrong guild", Scope{GuildID: "333", ThreadID: "555"},
			Channel{ID: "555", ParentID: "444", GuildID: "999", Type: 11}, false},
		{"no guild", Scope{ThreadID: "555"}, Channel{ID: "555", ParentID: "444", Type: 11}, false},
		{"ordinary channel", Scope{ThreadID: "555"}, Channel{ID: "555", GuildID: "333", Type: 0}, false},
		{"DM", Scope{ThreadID: "555"}, Channel{ID: "555", Type: 1}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			posts := 0
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v10/channels/555":
					assert.Equal(t, http.MethodGet, r.Method)
					_ = json.NewEncoder(w).Encode(test.channel)
				case "/api/v10/channels/555/messages":
					assert.Equal(t, http.MethodPost, r.Method)
					posts++
					fmt.Fprint(w, messageJSON)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			})
			message, err := client.CreateMessage(t.Context(), test.scope, MessageArgs{Content: "hello", Nonce: "send_1"})
			if test.valid {
				require.NoError(t, err)
				require.Equal(t, "555", message.ChannelID)
				require.Equal(t, 1, posts)
			} else {
				requireAPIError(t, err, ScopeMismatch)
				require.Zero(t, posts)
			}
		})
	}
}

func TestThreadScopeRejectsMalformedSuppliedParentBeforeIO(t *testing.T) {
	client, _ := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("invalid scope made an HTTP request")
	})
	for _, scope := range []Scope{
		{}, {GuildID: "333"}, {ThreadID: "0"}, {ThreadID: "555", ChannelID: "invalid"},
		{ThreadID: "555", ChannelID: "555"}, {ThreadID: "555", GuildID: "invalid"},
	} {
		_, err := client.GetScopedChannel(t.Context(), scope)
		require.Error(t, err, scope)
	}
}
