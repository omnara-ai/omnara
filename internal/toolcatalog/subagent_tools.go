package toolcatalog

const (
	spawnAgentToolDescription = "Launch a subagent for a complex, multi-step task or for independent work you want " +
		"to run in parallel. Pick `agent` from the configured subagent keys listed below: `self` keys copy your own " +
		"configuration and `profile` keys use another agent profile. The subagent starts with a clean context, so " +
		"`task` must be complete and self-contained. Returns immediately with the subagent's id. It runs in the " +
		"background and its final answer arrives later as a message from it, or through wait_agents. Never guess " +
		"or predict a pending subagent's result. Its answer is not shown to the user, so relay what matters. Give " +
		"it a `name` to address it later."
	waitAgentsToolDescription = "Block until subagents finish their current work, then return each one's final " +
		"answer or the question it is waiting on. Pass subagent names or ids in `agents`, or omit it to wait for " +
		"every running subagent. `mode` `all` (default) waits for every listed subagent; `any` returns when the " +
		"first finishes. Set `timeout_seconds` to give up and get a report of which subagents are still running. " +
		"Use this instead of polling list_agents."
	sendAgentMessageToolDescription = "Send a message to one of your subagents by name or id. Your plain text " +
		"output is not visible to subagents; this tool is the only way to reach them. Use it for follow-up " +
		"instructions, or to answer a question a subagent asked by passing its `interaction_id`. Messaging a " +
		"finished subagent resumes it with its context intact. The reply arrives later as a message from it."
	stopAgentToolDescription = "Stop a subagent by name or id. Cancels its current work and archives it, after " +
		"which it can no longer be messaged. Use it to end a subagent you no longer need or one that is taking " +
		"too long."
	listAgentsToolDescription = "List the subagents you can message, wait on, or stop, with each one's name, " +
		"key, state, and last activity. Names are the address for the other subagent tools. Do not poll this " +
		"to check progress; use wait_agents."
	subagentReferenceDescription = "Subagent name or id (agt_...)."
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
				"description": "Optional name for addressing the subagent later. Must be unique among your active subagents.",
			},
		},
	)
}

func waitAgentsTool() (Entry, error) {
	return toolEntry(
		ToolNameWaitAgents,
		waitAgentsToolDescription,
		nil,
		map[string]any{
			"agents": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string", "minLength": 1},
				"description": "Subagent names or ids to wait for. Omit to wait for all running subagents.",
			},
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{"all", "any"},
				"description": "Return when all listed subagents are done (default) or when any one is.",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     86400,
				"description": "Give up after this many seconds and report which subagents are still running.",
			},
		},
	)
}

func sendAgentMessageTool() (Entry, error) {
	return toolEntry(
		ToolNameSendAgentMessage,
		sendAgentMessageToolDescription,
		[]string{"agent", "message"},
		map[string]any{
			"agent": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": subagentReferenceDescription,
			},
			"message": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Message text delivered to the subagent.",
			},
			"interaction_id": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "When answering a question the subagent asked, the interaction id from that question.",
			},
		},
	)
}

func stopAgentTool() (Entry, error) {
	return toolEntry(
		ToolNameStopAgent,
		stopAgentToolDescription,
		[]string{"agent"},
		map[string]any{
			"agent": map[string]any{
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
