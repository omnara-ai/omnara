package tools

import (
	"encoding/json"
	"github.com/omnara-ai/omnara/internal/cronschedule"
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
		"cron and delay": `{"message":"check","cron":"0 9 * * *","delay_seconds":5}`,
		"bad cron":       `{"message":"check","cron":"every day"}`,
		"bad timezone":   `{"message":"check","cron":"0 9 * * *","timezone":"Mars/Olympus"}`,
		"stray timezone": `{"message":"check","delay_seconds":5,"timezone":"UTC"}`,
	} {
		_, err := resolveCreateScheduledMessageRequest(json.RawMessage(raw))
		require.Error(t, err, name)
	}
}

func TestScheduledMessageNoteStatesBothTimes(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 11, 18, 18, 0, 0, 0, time.UTC)
	note := scheduledMessageNote(created, created.Add(time.Hour))
	require.Contains(t, note, "at 2026-11-18T18:00:00Z")
	require.Contains(t, note, "delivery at 2026-11-18T19:00:00Z")
}

func TestResolveDeleteScheduledMessageRequest(t *testing.T) {
	t.Parallel()
	input, err := resolveDeleteScheduledMessageRequest(json.RawMessage(`{"id":" ain_abc "}`))
	require.NoError(t, err)
	require.Equal(t, "ain_abc", input.ID)
	_, err = resolveDeleteScheduledMessageRequest(json.RawMessage(`{"id":""}`))
	require.Error(t, err)
}

func TestResolveCreateScheduledMessageRequestWithCron(t *testing.T) {
	t.Parallel()
	recurring, err := resolveCreateScheduledMessageRequest(
		json.RawMessage(`{"message":"Summarize {{.trigger.name}} today.","cron":" 0 9 * * 1-5 ","timezone":"America/New_York"}`),
	)
	require.NoError(t, err)
	require.Equal(t, "0 9 * * 1-5", recurring.cron)
	require.Equal(t, "America/New_York", recurring.timezone)
	require.Zero(t, recurring.delay)
	require.Nil(t, recurring.scheduledFor)

	firedAt := time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)
	rendered, err := cronschedule.RenderMessage(
		scheduleMessageTemplate(recurring), cronschedule.MessageData("agent-schedule", firedAt, nil),
	)
	require.NoError(t, err)
	require.Equal(t, "Scheduled message: your recurring schedule fired at "+
		"2026-11-18T14:00:00Z.\n\nSummarize {{.trigger.name}} today.", rendered)

	utc, err := resolveCreateScheduledMessageRequest(json.RawMessage(`{"message":"check","cron":"*/5 * * * *"}`))
	require.NoError(t, err)
	require.Equal(t, "UTC", utc.timezone)
}

func TestListScheduledMessagesCursor(t *testing.T) {
	t.Parallel()
	offset, err := resolveListScheduledMessagesOffset(json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Zero(t, offset)
	offset, err = resolveListScheduledMessagesOffset(json.RawMessage(`{"cursor":"20"}`))
	require.NoError(t, err)
	require.Equal(t, 20, offset)
	for _, raw := range []string{`{"cursor":"-1"}`, `{"cursor":"abc"}`, `{"limit":5}`} {
		_, err := resolveListScheduledMessagesOffset(json.RawMessage(raw))
		require.Error(t, err, raw)
	}

	messages, schedules := []int{1, 2, 3}, []int{4, 5}
	require.Equal(t, []int{3}, pageSlice(messages, 2, 4))
	require.Equal(t, []int{4}, pageSlice(schedules, 2-len(messages), 4-len(messages)))
	require.Empty(t, pageSlice(messages, 4, 6))
	require.Equal(t, []int{5}, pageSlice(schedules, 4-len(messages), 6-len(messages)))
}
