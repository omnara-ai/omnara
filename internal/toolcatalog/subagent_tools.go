package toolcatalog

const (
	spawnAgentToolDescription = "Launch a subagent for a complex, multi-step task or for independent work you want " +
		"to run in parallel. Pick `agent` from the configured subagent keys listed below: `self` keys copy your own " +
		"configuration and `profile` keys use another agent profile. The subagent starts with a clean context, so " +
		"`task` must be complete and self-contained. Returns immediately with the subagent's id. It runs in the " +
		"background and its final answer arrives later as a message from it; read_agent shows its progress. Never " +
		"guess or predict a pending subagent's result. Its answer is not shown to the user, so relay what matters. " +
		"`name` is a display label only; address the subagent by the `agent_id` in the result."
	readAgentToolDescription = "Read a subagent's timeline by agent_id, in the same shape as the agent turns API. " +
		"Without `turn_id` it returns the newest turns first, each with its opening events and latest event; " +
		"page older turns with `before_turn_sequence`. With `turn_id` it returns that turn's events, newest " +
		"first, and pages older ones with `before_sequence`. Use it to check on progress or to fetch a finished " +
		"subagent's answer without waiting for its message. Never guess at a subagent's result; read it or wait " +
		"for its message."
	sendAgentMessageToolDescription = "Send a message to one of your subagents by agent_id. Your plain text " +
		"output is not visible to subagents; this tool is the only way to reach them. The subagent reads the " +
		"message once its current model call and tool batch finish; it does not stop running work. It does " +
		"cancel any question or permission request the subagent has open; it cannot answer those, only humans " +
		"can. Messaging a finished subagent resumes it with its context intact. The reply arrives later as a " +
		"message from it."
	stopAgentToolDescription = "Stop a subagent by agent_id. Cancels its current work and leaves it idle, so " +
		"you can still message it to continue with its context intact. Pass `archive: true` to also archive it " +
		"and its own subagents, after which it can no longer be messaged. Use it to interrupt a subagent that " +
		"is taking too long or to end one you no longer need."
	listAgentsToolDescription = "List the subagents you can read, message, or stop, with each one's agent_id, " +
		"name, key, state, and last activity. agent_id is the address for the other subagent tools. Subagent " +
		"results arrive as messages, so do not poll this to check progress."
	subagentReferenceDescription = "The subagent's agent_id (agt_...) as returned by spawn_agent or list_agents."
)

func spawnAgentTool() (Entry, error) {
	return toolEntry(
		ToolNameSpawnAgent,
		spawnAgentToolDescription,
		[]string{"agent", "task"},
		map[string]any{
			"agent": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Subagent key from the configured subagents.",
			},
			"task": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Complete, self-contained instructions for the subagent. It has no other context.",
			},
			"name": map[string]any{
				"type":        "string",
				"minLength":   1,
				"maxLength":   64,
				"description": "Optional display label shown in the console and in messages from the subagent.",
			},
		},
	)
}

func readAgentTool() (Entry, error) {
	return toolEntry(
		ToolNameReadAgent,
		readAgentToolDescription,
		[]string{"agent_id"},
		map[string]any{
			"agent_id": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": subagentReferenceDescription,
			},
			"turn_id": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "A turn id (trn_...) from a previous read_agent result. When set, returns that turn's events instead of the turn list.",
			},
			"before_turn_sequence": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Without turn_id: return turns before this turn sequence, newest first. Omit or pass 0 for the latest turns. Use next_before_turn_sequence from the previous result to page.",
			},
			"before_sequence": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "With turn_id: return events before this sequence, newest first. Omit or pass 0 for the latest events. Use next_before_sequence from the previous result to page.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     500,
				"description": "Maximum turns (default 10, at most 100) or events (default 20, at most 500) to return.",
			},
		},
	)
}

func sendAgentMessageTool() (Entry, error) {
	return toolEntry(
		ToolNameSendAgentMessage,
		sendAgentMessageToolDescription,
		[]string{"agent_id", "message"},
		map[string]any{
			"agent_id": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": subagentReferenceDescription,
			},
			"message": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Message text delivered to the subagent.",
			},
		},
	)
}

func stopAgentTool() (Entry, error) {
	return toolEntry(
		ToolNameStopAgent,
		stopAgentToolDescription,
		[]string{"agent_id"},
		map[string]any{
			"agent_id": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": subagentReferenceDescription,
			},
			"archive": map[string]any{
				"type":        "boolean",
				"description": "Also archive the subagent and its subagents instead of leaving it idle. Defaults to false.",
			},
		},
	)
}

func listAgentsTool() (Entry, error) {
	return toolEntry(ToolNameListAgents, listAgentsToolDescription, nil, nil)
}
