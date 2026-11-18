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
	getScheduledMessagesToolDescription = "List the messages you have scheduled for yourself with " +
		"create_scheduled_message that have not been delivered yet, soonest first. Messages scheduled by anyone " +
		"else are not listed."
	createScheduledMessageToolDescription = "Schedule a message to yourself for a later time, to remind yourself " +
		"to check on something or continue work. Provide exactly one of `timestamp` or `delay_seconds`. At that " +
		"time the message is added to your queue and starts a new turn once you are free; delivery can lag the " +
		"scheduled time by about a minute. The delivered message is prefixed with a note that you scheduled it " +
		"and when, so write `message` as complete instructions to your future self. Scheduled messages count " +
		"toward your pending-message limit."
	deleteScheduledMessageToolDescription = "Delete a message you scheduled for yourself that has not been " +
		"delivered yet. Only messages returned by get_scheduled_messages can be deleted."
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
				"description": "The scheduled message's id (ain_...) from create_scheduled_message or get_scheduled_messages.",
			},
		},
	)
}
