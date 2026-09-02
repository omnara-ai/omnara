package toolcatalog

const (
	spawnAgentToolDescription = "Start a subagent from one of the configured subagent handles. The subagent runs " +
		"asynchronously with a clean context; only `task` reaches it. Returns immediately with the subagent's id. " +
		"Its final answer arrives later as a message from the subagent, or through wait_agents."
	waitAgentsToolDescription = "Wait until subagents finish their current work. Pass the subagent names or ids " +
		"to wait for, or omit `agents` to wait for every running subagent. Completes with each subagent's final " +
		"answer, or with a timeout report."
	sendAgentMessageToolDescription = "Send a message to one of your subagents. Use it to give follow-up " +
		"instructions or to answer a question the subagent asked (pass `interaction_id` from the question). " +
		"The subagent's reply arrives later as a message from it."
	stopAgentToolDescription = "Cancel and archive one of your subagents. Its work stops and it can no longer " +
		"be messaged."
	listAgentsToolDescription    = "List your subagents with their names, handles, states, and last activity."
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
				"description": "Subagent handle from the configured subagents.",
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
