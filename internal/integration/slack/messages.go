package slack

import (
	"net/url"
	"strings"
)

func ConversationURI(workspaceID, providerRef string) string {
	workspaceID = strings.TrimSpace(workspaceID)
	conversationID, _, _ := strings.Cut(strings.TrimSpace(providerRef), ":")
	if workspaceID == "" || conversationID == "" {
		return ""
	}
	values := url.Values{}
	values.Set("channel", conversationID)
	values.Set("team", workspaceID)
	return "https://slack.com/app_redirect?" + values.Encode()
}
