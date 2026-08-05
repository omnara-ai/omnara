//go:build integration

package httpapi

import "time"

func unitSlackSignedHeaders(body, signingSecret string) map[string]string {
	return slackSignedHeadersAt(body, signingSecret, time.Now().UTC())
}
