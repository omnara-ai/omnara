package toolcatalog

const (
	spawnAgentToolDescription = "Launch a subagent for a complex, multi-step task or for independent work you want " +
		"to run in parallel. Pick `agent` from the configured subagent keys listed below: `self` keys copy your own " +
		"configuration and `profile` keys use another agent profile. The subagent starts with a clean context, so " +
		"`task` must be complete and self-contained. Returns immediately with the subagent's id. It runs in the " +
		"background and its final answer arrives later as a message from it; read_agent shows its progress. Never " +
		"guess or predict a pending subagent's result. Its answer is not shown to the user, so relay what matters. " +
		"`name` is a display label only; address the subagent by the `agent_ref` in the result."
	readAgentToolDescription = "Read a subagent's timeline by agent_ref: its inputs, model outputs, and tool results " +
		"with text content. Use it to check on progress or to fetch a finished subagent's answer without waiting " +
		"for its message. Page forward with `after_sequence` (oldest first) or backward from the end with " +
		"`before_sequence` (newest first). Never guess at a subagent's result; read it or wait for its message."
	sendAgentMessageToolDescription = "Send a message to one of your subagents by agent_ref. Your plain text " +
		"output is not visible to subagents; this tool is the only way to reach them. The message interrupts " +
		"whatever the subagent is doing and cancels any question or permission request it has open; it cannot " +
		"answer those, only humans can. Messaging a finished subagent resumes it with its context intact. The " +
		"reply arrives later as a message from it."
	stopAgentToolDescription = "Stop a subagent by agent_ref. Cancels its current work and archives it, after " +
		"which it can no longer be messaged. Use it to end a subagent you no longer need or one that is taking " +
		"too long."
	listAgentsToolDescription = "List the subagents you can read, message, or stop, with each one's agent_ref, " +
		"name, key, state, and last activity. agent_ref is the address for the other subagent tools. Subagent " +
		"results arrive as messages, so do not poll this to check progress."
	subagentReferenceDescription = "The agent_ref returned by spawn_agent or list_agents, or the full agent id (agt_...)."
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
		[]string{"agent_ref"},
		map[string]any{
			"agent_ref": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": subagentReferenceDescription,
			},
			"after_sequence": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Return events after this sequence, oldest first. Omit or pass 0 to start from the beginning.",
			},
			"before_sequence": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Return events before this sequence, newest first. Pass 0 for the latest events. Ignored when after_sequence is set.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     100,
				"description": "Maximum events to return. Defaults to 20.",
			},
		},
	)
}

func sendAgentMessageTool() (Entry, error) {
	return toolEntry(
		ToolNameSendAgentMessage,
		sendAgentMessageToolDescription,
		[]string{"agent_ref", "message"},
		map[string]any{
			"agent_ref": map[string]any{
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
		[]string{"agent_ref"},
		map[string]any{
			"agent_ref": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": subagentReferenceDescription,
			},
		},
	)
}

func listAgentsTool() (Entry, error) {
	return toolEntry(ToolNameListAgents, listAgentsToolDescription, nil, nil)
}
