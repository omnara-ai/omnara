package slack

import (
	"testing"
)

func TestConversationURI(t *testing.T) {
	tests := []struct {
		name        string
		workspaceID string
		providerRef string
		want        string
	}{
		{
			name:        "thread",
			workspaceID: "T123",
			providerRef: "C123:1712345678.000100",
			want:        "https://slack.com/app_redirect?channel=C123&team=T123",
		},
		{
			name:        "direct message conversation",
			workspaceID: "T123",
			providerRef: "D123",
			want:        "https://slack.com/app_redirect?channel=D123&team=T123",
		},
		{
			name:        "encodes values",
			workspaceID: "T 123",
			providerRef: "C 123:1712345678.000100",
			want:        "https://slack.com/app_redirect?channel=C+123&team=T+123",
		},
		{
			name:        "missing workspace",
			workspaceID: "",
			providerRef: "C123:1712345678.000100",
			want:        "",
		},
		{
			name:        "missing conversation",
			workspaceID: "T123",
			providerRef: ":1712345678.000100",
			want:        "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ConversationURI(test.workspaceID, test.providerRef); got != test.want {
				t.Fatalf("ConversationURI() = %q, want %q", got, test.want)
			}
		})
	}
}
