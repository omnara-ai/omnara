package integrationstore

import "testing"

func TestGitHubWebhookCredentialLookupRejectsUnboundedHints(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		appID string
		limit int
	}{
		{"", 1}, {"0", 1}, {"-1", 1}, {"0123", 1}, {"+123", 1}, {"123 OR true", 1},
		{"9223372036854775808", 1}, {"123", 0}, {"123", -1}, {"123", GitHubWebhookCredentialLimit + 1},
	} {
		if _, err := (&Store{}).ListGitHubWebhookCredentialApps(t.Context(), tc.appID, tc.limit); err == nil {
			t.Fatalf("accepted appID=%q limit=%d", tc.appID, tc.limit)
		}
	}
}
