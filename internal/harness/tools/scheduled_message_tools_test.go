package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveCreateScheduledMessageRequest(t *testing.T) {
	t.Parallel()
	delayed, err := resolveCreateScheduledMessageRequest(json.RawMessage(`{"message":" check ","delay_seconds":90}`))
	require.NoError(t, err)
	require.Equal(t, "check", delayed.message)
	require.Equal(t, 90*time.Second, delayed.delay)
	require.Nil(t, delayed.scheduledFor)

	timed, err := resolveCreateScheduledMessageRequest(
		json.RawMessage(`{"message":"check","timestamp":"2026-12-01T10:00:00+01:00"}`),
	)
	require.NoError(t, err)
	require.Zero(t, timed.delay)
	require.Equal(t, time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC), *timed.scheduledFor)

	for name, raw := range map[string]string{
		"neither time":   `{"message":"check"}`,
		"both times":     `{"message":"check","timestamp":"2026-12-01T09:00:00Z","delay_seconds":5}`,
		"blank message":  `{"message":"  ","delay_seconds":5}`,
		"zero delay":     `{"message":"check","delay_seconds":0}`,
		"huge delay":     `{"message":"check","delay_seconds":99999999}`,
		"no utc offset":  `{"message":"check","timestamp":"2026-12-01T09:00:00"}`,
		"unknown field":  `{"message":"check","delay_seconds":5,"mode":"steering"}`,
		"fractional":     `{"message":"check","delay_seconds":1.5}`,
		"not a datetime": `{"message":"check","timestamp":"tomorrow"}`,
	} {
		_, err := resolveCreateScheduledMessageRequest(json.RawMessage(raw))
		require.Error(t, err, name)
	}
}

func TestScheduledMessageTextPrefixesTheMessage(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 11, 18, 18, 0, 0, 0, time.UTC)
	text := scheduledMessageText("Check the deploy.", created, created.Add(time.Hour))
	require.True(t, strings.HasSuffix(text, "\n\nCheck the deploy."))
	require.Contains(t, text, "at 2026-11-18T18:00:00Z")
	require.Contains(t, text, "delivery at 2026-11-18T19:00:00Z")
}

func TestResolveDeleteScheduledMessageRequest(t *testing.T) {
	t.Parallel()
	input, err := resolveDeleteScheduledMessageRequest(json.RawMessage(`{"id":" ain_abc "}`))
	require.NoError(t, err)
	require.Equal(t, "ain_abc", input.ID)
	_, err = resolveDeleteScheduledMessageRequest(json.RawMessage(`{"id":""}`))
	require.Error(t, err)
}
