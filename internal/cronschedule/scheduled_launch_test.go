package cronschedule

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOccurrenceLocalDateUsesDueTimezone(t *testing.T) {
	due := time.Date(2026, 3, 9, 1, 30, 0, 0, time.UTC)
	fired := due.Add(48 * time.Hour)
	data, err := OccurrenceMessageData("Daily", fired, &due, due, "America/Los_Angeles")
	require.NoError(t, err)
	got, err := RenderMessage(`{{.trigger.local_date}} {{.trigger.fired_at}} {{.trigger.last_fired_at}}`, data)
	require.NoError(t, err)
	require.Equal(t, "2026-03-08 2026-03-11T01:30:00Z 2026-03-09T01:30:00Z", got)
	require.NoError(t, ValidateMessageTemplate(`{{.trigger.local_date}}`))
}

func TestOpeningMessageBounds(t *testing.T) {
	data := MessageData("Daily", time.Now(), nil)
	_, err := RenderOpeningMessage(strings.Repeat("🚀", 2000), data)
	require.NoError(t, err)
	_, err = RenderOpeningMessage(strings.Repeat("🚀", 2001), data)
	require.Error(t, err)
	_, err = RenderOpeningMessage(`{{printf "%1024s" "a"}}{{printf "%1024s" "b"}}`, data)
	require.Error(t, err)
	_, err = RenderOpeningMessage("   ", data)
	require.Error(t, err)
	_, err = RenderOpeningMessage(`{{if false}}heading{{end}}`, data)
	require.Error(t, err)
}
