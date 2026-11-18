package toolcatalog

const (
	ToolNameGetCurrentTime         = "get_current_time"
	ToolNameGetScheduledMessages   = "get_scheduled_messages"
	ToolNameCreateScheduledMessage = "create_scheduled_message"
	ToolNameDeleteScheduledMessage = "delete_scheduled_message"

	// ScheduledMessageMaxDelaySeconds is 366 days.
	ScheduledMessageMaxDelaySeconds = 366 * 24 * 60 * 60
	ScheduledMessageMaxLength       = 20000

	getCurrentTimeToolDescription = "Get the current date and time in UTC. Use it before scheduling a message " +
		"for a specific time; do not guess the current time."
	getScheduledMessagesToolDescription = "List what is scheduled for you. `scheduled_messages` holds the " +
		"one-time messages you scheduled for yourself that have not been delivered yet, soonest first; one-time " +
		"messages scheduled by anyone else are not listed. `schedules` holds every recurring cron schedule that " +
		"messages you, including ones you did not create."
	createScheduledMessageToolDescription = "Schedule a message to yourself, to remind yourself to check on " +
		"something or continue work. Provide exactly one of `timestamp` or `delay_seconds` for a one-time " +
		"message, or `cron` for a recurring schedule that keeps messaging you until it is deleted. When a " +
		"message is due it is added to your queue and starts a new turn once you are free; delivery can lag by " +
		"about a minute. The delivered message is prefixed with a note about the schedule, so write `message` " +
		"as complete instructions to your future self. One-time scheduled messages count toward your " +
		"pending-message limit."
	deleteScheduledMessageToolDescription = "Delete a one-time message you scheduled for yourself that has not " +
		"been delivered yet, or any recurring schedule that messages you, by the id from get_scheduled_messages."
)

// ScheduledMessageToolNames lists the tools that let an agent schedule messages to itself.
func ScheduledMessageToolNames() []string {
	return []string{
		ToolNameGetCurrentTime,
		ToolNameGetScheduledMessages,
		ToolNameCreateScheduledMessage,
		ToolNameDeleteScheduledMessage,
	}
}

func getCurrentTimeTool() (Entry, error) {
	return toolEntry(ToolNameGetCurrentTime, getCurrentTimeToolDescription, nil, nil)
}

func getScheduledMessagesTool() (Entry, error) {
	return toolEntry(ToolNameGetScheduledMessages, getScheduledMessagesToolDescription, nil, nil)
}

func createScheduledMessageTool() (Entry, error) {
	return toolEntry(
		ToolNameCreateScheduledMessage,
		createScheduledMessageToolDescription,
		[]string{"message"},
		map[string]any{
			"message": map[string]any{
				"type":        "string",
				"minLength":   1,
				"maxLength":   ScheduledMessageMaxLength,
				"description": "Message text delivered to you at the scheduled time.",
			},
			"timestamp": map[string]any{
				"type": "string",
				"description": "Delivery time as an RFC 3339 timestamp with a UTC offset, such as " +
					"2026-12-01T09:00:00Z. Must be in the future and at most 366 days away.",
			},
			"delay_seconds": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     ScheduledMessageMaxDelaySeconds,
				"description": "Deliver this many seconds from now.",
			},
			"cron": map[string]any{
				"type":      "string",
				"minLength": 1,
				"description": "Five-field cron expression (minute hour day-of-month month day-of-week) for a " +
					"recurring schedule, such as `0 9 * * 1-5` for 09:00 on weekdays.",
			},
			"timezone": map[string]any{
				"type":        "string",
				"description": "IANA time zone the cron expression is evaluated in, such as America/New_York. Only valid with `cron`. Defaults to UTC.",
			},
		},
	)
}

func deleteScheduledMessageTool() (Entry, error) {
	return toolEntry(
		ToolNameDeleteScheduledMessage,
		deleteScheduledMessageToolDescription,
		[]string{"id"},
		map[string]any{
			"id": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "The id from create_scheduled_message or get_scheduled_messages: a one-time message (ain_...) or a recurring schedule (cron_...).",
			},
		},
	)
}
