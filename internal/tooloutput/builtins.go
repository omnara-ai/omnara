package tooloutput

import (
	"maps"

	"github.com/omnara-ai/omnara/internal/interactionform"
)

var (
	RunCommandSchema    = outputSchema(mapSchema(runCommandSchema))
	ReadProcessSchema   = outputSchema(mapSchema(readProcessSchema))
	WriteProcessSchema  = outputSchema(mapSchema(processActionSchema))
	WatchProcessSchema  = outputSchema(mapSchema(processActionSchema))
	StopProcessSchema   = outputSchema(mapSchema(stopProcessSchema))
	ListProcessesSchema = outputSchema(mapSchema(listProcessesSchema))

	CreateMachineSchema = outputSchema(mapSchema(func() map[string]any {
		return extend(machine(), map[string]any{"created": constant(true), "ready": constant(false)},
			"created", "ready")
	}))
	DeleteMachineSchema = outputSchema(mapSchema(func() map[string]any {
		return extend(machine(), map[string]any{
			"deleted": constant(false), "deletion_in_progress": constant(true),
		}, "deleted", "deletion_in_progress")
	}))
	ListMachinesSchema           = outputSchema(mapSchema(listMachinesSchema))
	InspectMachineSchema         = outputSchema(mapSchema(inspectMachineSchema))
	ListGitHubRepositoriesSchema = outputSchema(mapSchema(listGitHubRepositoriesSchema))

	AskQuestionSchema             = outputSchema(reflectOutputSchema[interactionform.RenderedAnswers])
	ListInteractionHandlersSchema = outputSchema(mapSchema(listInteractionHandlersSchema))
	SetInteractionHandlerSchema   = outputSchema(mapSchema(setInteractionHandlerSchema))
	SkillSchema                   = outputSchema(mapSchema(func() map[string]any {
		return describe("The skill's SKILL.md instructions in a <skill_content> element, followed by where the "+
			"skill was installed. The result is text, so commands get it as a JSON string.", stringType())
	}))
	ToolSearchSchema = outputSchema(mapSchema(toolSearchSchema))

	StorageWriteSchema = outputSchema(mapSchema(func() map[string]any {
		return object(map[string]any{"path": describe("/memory/<store>/<file>", stringType())}, "path")
	}))
	StorageUploadSchema = outputSchema(mapSchema(func() map[string]any {
		return object(map[string]any{
			"path": describe("The new /artifacts/<artifact_id>, or the /memory/<store>/<file> written.", stringType()),
		}, "path")
	}))
	StorageDownloadSchema = outputSchema(mapSchema(func() map[string]any {
		return object(map[string]any{
			"destination": describe("The destination path as given in the input.", stringType()),
		}, "destination")
	}))

	GetCurrentTimeSchema         = outputSchema(mapSchema(getCurrentTimeSchema))
	ListScheduledMessagesSchema  = outputSchema(mapSchema(listScheduledMessagesSchema))
	CreateScheduledMessageSchema = outputSchema(mapSchema(createScheduledMessageSchema))
	DeleteScheduledMessageSchema = outputSchema(mapSchema(func() map[string]any {
		return object(map[string]any{
			"id":        stringType(),
			"deleted":   constant(true),
			"recurring": describe("Present when a recurring schedule was deleted.", constant(true)),
		}, "id", "deleted")
	}))

	SpawnAgentSchema = outputSchema(mapSchema(func() map[string]any {
		return object(map[string]any{
			"agent_id": agentID(),
			"name":     stringType(),
			"key":      stringType(),
			"state":    constant("running"),
			"message":  stringType(),
		}, "agent_id", "name", "key", "state", "message")
	}))
	ReadAgentSchema        = outputSchema(mapSchema(readAgentSchema))
	SendAgentMessageSchema = outputSchema(mapSchema(func() map[string]any {
		return object(map[string]any{
			"agent_id":  agentID(),
			"name":      stringType(),
			"delivered": constant(true),
			"message":   stringType(),
		}, "agent_id", "name", "delivered", "message")
	}))
	StopAgentSchema = outputSchema(mapSchema(func() map[string]any {
		return anyOf("With archive the subagent is archived; otherwise its current work is canceled and it stays idle.",
			titledSchema("archived", object(map[string]any{
				"agent_id": agentID(), "name": stringType(), "archived": constant(true), "state": constant("archived"),
			}, "agent_id", "name", "archived", "state")),
			titledSchema("stopped", object(map[string]any{
				"agent_id": agentID(), "name": stringType(), "archived": constant(false), "message": stringType(),
			}, "agent_id", "name", "archived", "message")),
		)
	}))
	ListAgentsSchema = outputSchema(mapSchema(func() map[string]any {
		return object(map[string]any{"agents": arrayOf(subagent())}, "agents")
	}))
)

// processOutput holds the output fields of a process observation. offset_byte is omitted when 0, and
// omitted_offset_byte, tail_offset_byte and output_tail appear together when long output keeps only its
// start and end.
func processOutput() map[string]any {
	return map[string]any{
		"output":              stringType(),
		"offset_byte":         integerType(),
		"next_offset_byte":    integerType(),
		"truncated":           constant(true),
		"omitted_offset_byte": integerType(),
		"tail_offset_byte":    integerType(),
		"output_tail":         stringType(),
	}
}

// processEnd holds the facts about how an ended process ended.
func processEnd() map[string]any {
	return map[string]any{
		"exit_code":         integerType(),
		"exit_signal":       stringType(),
		"state_reason_code": stringType(),
		"error":             stringType(),
	}
}

func processResult(state map[string]any, more ...map[string]any) map[string]any {
	properties := map[string]any{"process_id": processID(), "state": state}
	for _, fields := range more {
		maps.Copy(properties, fields)
	}
	return object(properties, "process_id", "state")
}

func runCommandSchema() map[string]any {
	return anyOf("The command's output so far while it is still running, or its output and exit once it has ended.",
		titledSchema("running", processResult(constant("running"), processOutput(), map[string]any{
			"note":  stringType(),
			"error": describe("Why the output could not be read.", stringType()),
		})),
		titledSchema("ended", processResult(enum("exited", "failed", "killed", "unknown"), processOutput(),
			processEnd())),
	)
}

func readProcessSchema() map[string]any {
	schema := processResult(enum("starting", "running", "exited", "failed", "killed", "unknown"), processOutput(),
		processEnd())
	schema["required"] = []string{"process_id", "state", "output", "next_offset_byte"}
	return schema
}

func processActionSchema() map[string]any {
	return processResult(enum("applied", "failed", "unknown"), map[string]any{
		"state_reason_code": stringType(),
		"error":             stringType(),
	})
}

func stopProcessSchema() map[string]any {
	return extend(processActionSchema(), map[string]any{
		"mode": describe("Present when the process had already stopped.", enum("interrupt", "terminate")),
	})
}

func listProcessesSchema() map[string]any {
	return object(map[string]any{
		"processes": arrayOf(object(map[string]any{
			"process_id":    processID(),
			"state":         enum("starting", "running"),
			"command_label": stringType(),
			"cwd":           stringType(),
			"started_at":    dateTime(),
			"updated_at":    dateTime(),
		}, "process_id", "state")),
	}, "processes")
}

func processID() map[string]any { return describe("prc_...", stringType()) }

func agentID() map[string]any { return describe("agt_...", stringType()) }

func machine() map[string]any {
	return object(map[string]any{
		"machine_id":              describe("mch_...", stringType()),
		"source_kind":             enum("byo", "pool"),
		"binding_kind":            enum("explicit", "pool"),
		"binding_state":           enum("attached", "released"),
		"display_name":            stringType(),
		"machine_pool_id":         describe("mpo_...", stringType()),
		"machine_pool_name":       stringType(),
		"cpu":                     integerType(),
		"memory_mb":               integerType(),
		"lifecycle_state":         enum("", "provisioning", "provision_failed", "active", "deleting", "delete_failed", "deleted"),
		"connection_state":        enum("", "online", "asleep", "offline"),
		"connection_state_reason": stringType(),
		"description":             stringType(),
		"cwd":                     stringType(),
		"executable":              booleanType(),
		"project_grant_missing": describe("Present when the project lost its grant to the machine; most details "+
			"are then omitted.", constant(true)),
		"lifecycle_reason_code":    stringType(),
		"lifecycle_reason_message": stringType(),
		"created_at":               dateTime(),
		"updated_at":               dateTime(),
	}, "machine_id", "binding_state", "lifecycle_state", "connection_state", "description", "cwd", "executable",
		"lifecycle_reason_code", "lifecycle_reason_message", "created_at", "updated_at")
}

func machinePool() map[string]any {
	return object(map[string]any{
		"machine_pool_id":     describe("mpo_...", stringType()),
		"machine_pool_name":   stringType(),
		"description":         stringType(),
		"supported_overrides": arrayOf(enum("cpu", "memory_mb")),
		"default_cpu":         integerType(),
		"default_memory_mb":   integerType(),
		"min_cpu":             integerType(),
		"max_cpu":             integerType(),
		"min_memory_mb":       integerType(),
		"max_memory_mb":       integerType(),
	}, "machine_pool_id", "machine_pool_name", "supported_overrides")
}

func listMachinesSchema() map[string]any {
	return object(withOffsetPage(map[string]any{
		"machines":      arrayOf(machine()),
		"machine_pools": arrayOf(machinePool()),
	}), "machines", "machine_pools", "has_more")
}

func inspectMachineSchema() map[string]any {
	return extend(machine(), map[string]any{
		"failure_report": describe("The machine's last startup or daemon failure, when it has one.", object(
			map[string]any{
				"stage": enum("startup_script", "daemon_install", "daemon_update", "daemon_runtime",
					"daemon_uninstall", "daemon_uninstalled"),
				"output_tail":      stringType(),
				"output_truncated": booleanType(),
				"reported_at":      dateTime(),
				"exit_status":      integerType(),
				"daemon_version":   stringType(),
				"target_version":   stringType(),
			}, "stage", "output_tail", "output_truncated", "reported_at")),
	})
}

func listGitHubRepositoriesSchema() map[string]any {
	return object(withOffsetPage(map[string]any{
		"can_push": booleanType(),
		"repositories": arrayOf(object(map[string]any{
			"id":        integerType(),
			"full_name": stringType(),
		}, "id", "full_name")),
	}), "can_push", "repositories", "has_more")
}

func listInteractionHandlersSchema() map[string]any {
	destination := anyOf("The conversation the handler sends to.",
		titledSchema("slack", object(map[string]any{
			"channel_id": stringType(),
			"thread_ts":  stringType(),
		}, "channel_id")),
		titledSchema("github", object(map[string]any{
			"repository_id": integerType(),
			"pull_request":  integerType(),
		}, "repository_id", "pull_request")),
		titledSchema("discord", object(map[string]any{
			"guild_id":   stringType(),
			"channel_id": stringType(),
			"thread_id":  stringType(),
		})),
	)
	return object(withOffsetPage(map[string]any{
		"auto_select": booleanType(),
		"handlers": arrayOf(object(map[string]any{
			"handler":      stringType(),
			"destination":  destination,
			"description":  stringType(),
			"input_schema": describe("JSON Schema of the handler's args.", anyObject()),
		}, "handler", "destination", "description", "input_schema")),
		"selection": describe("null when questions and approvals go only to the dashboard.", nullable(object(
			map[string]any{
				"handler":        stringType(),
				"integration_id": describe("itg_...", stringType()),
				"args":           anyObject(),
				"destination":    destination,
			}, "handler", "integration_id", "args", "destination"))),
	}), "auto_select", "handlers", "selection", "has_more")
}

func setInteractionHandlerSchema() map[string]any {
	return object(map[string]any{
		"selection": describe("null when questions and approvals go only to the dashboard.", nullable(object(
			map[string]any{"handler": stringType(), "args": anyObject()}, "handler", "args"))),
		"auto_select": booleanType(),
	}, "selection", "auto_select")
}

func toolSearchSchema() map[string]any {
	tool := func(outputSchema string) map[string]any {
		return object(map[string]any{
			"name":          stringType(),
			"description":   stringType(),
			"input_schema":  anyObject(),
			"output_schema": describe(outputSchema, anyObject()),
		}, "name", "input_schema")
	}
	return anyOf("The deferred tools a model call loaded, or the tools commands can call when run with "+
		"`omnarad call`.",
		titledSchema("model call", object(map[string]any{
			"pattern":              stringType(),
			"tool_names":           arrayOf(stringType()),
			"total_deferred_tools": integerType(),
			"tools":                arrayOf(tool("Not set for model calls.")),
		}, "pattern", "tool_names", "total_deferred_tools", "tools")),
		titledSchema("omnarad call", object(map[string]any{
			"pattern": stringType(),
			"tools":   arrayOf(tool("What the tool prints when called with `omnarad call`.")),
			"total":   integerType(),
		}, "pattern", "tools", "total")),
	)
}

func getCurrentTimeSchema() map[string]any {
	return object(map[string]any{
		"current_time": dateTime(),
		"unix_seconds": integerType(),
		"weekday":      enum("Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"),
		"timezone":     constant("UTC"),
	}, "current_time", "unix_seconds", "weekday", "timezone")
}

func listScheduledMessagesSchema() map[string]any {
	return object(map[string]any{
		"scheduled_messages": arrayOf(object(map[string]any{
			"id":              describe("ain_...", stringType()),
			"scheduled_for":   dateTime(),
			"created_at":      dateTime(),
			"message_preview": stringType(),
		}, "id", "scheduled_for", "created_at", "message_preview")),
		"schedules": arrayOf(object(map[string]any{
			"id":              describe("cron_...", stringType()),
			"cron":            stringType(),
			"timezone":        stringType(),
			"enabled":         booleanType(),
			"next_run_at":     dateTime(),
			"last_run_at":     dateTime(),
			"created_at":      dateTime(),
			"name":            stringType(),
			"message_preview": stringType(),
		}, "id", "cron", "timezone", "enabled", "created_at", "name", "message_preview")),
	}, "scheduled_messages", "schedules")
}

func createScheduledMessageSchema() map[string]any {
	return anyOf("A one-time message for timestamp or delay_seconds, or a recurring schedule for cron.",
		titledSchema("one-time", object(map[string]any{
			"id":            describe("ain_...", stringType()),
			"scheduled_for": dateTime(),
			"created_at":    dateTime(),
			"scheduled":     constant(true),
		}, "id", "scheduled_for", "created_at", "scheduled")),
		titledSchema("recurring", object(map[string]any{
			"id":          describe("cron_...", stringType()),
			"cron":        stringType(),
			"timezone":    stringType(),
			"scheduled":   constant(true),
			"recurring":   constant(true),
			"next_run_at": dateTime(),
		}, "id", "cron", "timezone", "scheduled", "recurring")),
	)
}

func subagent() map[string]any {
	return object(map[string]any{
		"agent_id":               agentID(),
		"name":                   stringType(),
		"key":                    stringType(),
		"state":                  enum("running", "idle", "waiting_on_interaction", "archived"),
		"last_activity_at":       dateTime(),
		"running_process_count":  integerType(),
		"working_subagent_count": integerType(),
	}, "agent_id", "key", "state", "last_activity_at")
}

func readAgentSchema() map[string]any {
	blockMetadata := map[string]any{"type": "object", "additionalProperties": stringType()}
	block := func(kind string, properties map[string]any, required ...string) map[string]any {
		properties["type"] = constant(kind)
		properties["metadata"] = blockMetadata
		return object(properties, append([]string{"type"}, required...)...)
	}
	textBlock := func(kind string) map[string]any {
		return block(kind, map[string]any{"text": stringType()}, "text")
	}
	mediaRef := block("media_ref", map[string]any{
		"artifact_id":                describe("art_...", stringType()),
		"exclude_from_model_context": booleanType(),
	}, "artifact_id")
	toolCall := block("tool_call", map[string]any{
		"tool_call_id": describe("tcl_...", stringType()),
		"name":         stringType(),
		"input":        anyObject(),
		"tool_type":    enum("built_in", "custom", "mcp"),
	}, "tool_call_id", "name", "input", "tool_type")
	structuredData := block("structured_data", map[string]any{"value": map[string]any{}}, "value")
	blocks := func(alternatives ...map[string]any) map[string]any {
		return arrayOf(map[string]any{"anyOf": alternatives})
	}
	event := func(kind string, properties map[string]any, required ...string) map[string]any {
		properties["event_kind"] = constant(kind)
		properties["sequence"] = integerType()
		properties["created_at"] = dateTime()
		return object(properties, append([]string{"event_kind", "sequence", "created_at"}, required...)...)
	}
	anyEvent := map[string]any{"anyOf": []any{
		event("agent_input", map[string]any{
			"input_kind":     enum("config_change", "content", "control", "interaction_response"),
			"control_type":   enum("cancel_current"),
			"content_blocks": blocks(textBlock("text"), mediaRef),
		}, "input_kind", "content_blocks"),
		event("model_output", map[string]any{
			"stop_reason":    enum("content_filter", "end_turn", "error", "max_tokens", "refusal", "tool_use"),
			"content_blocks": blocks(textBlock("text"), mediaRef, textBlock("reasoning"), toolCall, textBlock("error")),
		}, "stop_reason", "content_blocks"),
		event("tool_result", map[string]any{
			"tool_call_id":   describe("tcl_...", stringType()),
			"outcome":        enum("canceled", "denied", "failed", "succeeded"),
			"content_blocks": blocks(textBlock("text"), mediaRef, structuredData),
		}, "tool_call_id", "outcome", "content_blocks"),
		event("context_checkpoint", map[string]any{
			"summarized_through_event_sequence": integerType(),
			"summary":                           stringType(),
		}, "summarized_through_event_sequence", "summary"),
	}}
	turn := object(map[string]any{
		"id":                    describe("trn_...", stringType()),
		"turn_sequence":         integerType(),
		"event_count":           integerType(),
		"opening_events":        arrayOf(anyEvent),
		"latest_event":          describe("Omitted when it is one of opening_events.", anyEvent),
		"latest_semantic_event": describe("Omitted when it is latest_event or one of opening_events.", anyEvent),
		"started_at":            dateTime(),
		"updated_at":            dateTime(),
	}, "id", "turn_sequence", "event_count", "opening_events", "started_at", "updated_at")
	return anyOf("Without turn_id the subagent's newest turns first; with turn_id a page of that turn's events, "+
		"oldest first.",
		titledSchema("turns", object(map[string]any{
			"agent":                     subagent(),
			"turns":                     arrayOf(turn),
			"next_before_turn_sequence": nullable(integerType()),
		}, "agent", "turns", "next_before_turn_sequence")),
		titledSchema("turn events", object(map[string]any{
			"agent":                subagent(),
			"turn_id":              describe("trn_...", stringType()),
			"events":               arrayOf(anyEvent),
			"next_before_sequence": nullable(integerType()),
		}, "agent", "turn_id", "events", "next_before_sequence")),
	)
}
