package tools

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/processresult"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/tooloutput"
	"github.com/stretchr/testify/require"
)

func builtInOutputSchema(t *testing.T, name string) json.RawMessage {
	t.Helper()
	spec := builtInSpec(t, name)
	require.NotEmpty(t, spec.OutputSchema, name)
	require.NoError(t, jsonschema.ValidateSchema(spec.OutputSchema), name)
	return spec.OutputSchema
}

func integrationOutputSchema(t *testing.T, kind integrationdefinition.Kind, operation string) json.RawMessage {
	t.Helper()
	spec := integrationSpec(t, kind, operation)
	require.NotEmpty(t, spec.OutputSchema, operation)
	require.NoError(t, jsonschema.ValidateSchema(spec.OutputSchema), operation)
	return spec.OutputSchema
}

func requireRejectedByOutputSchema(t *testing.T, schema json.RawMessage, value string) {
	t.Helper()
	require.Error(t, jsonschema.Validate(schema, json.RawMessage(value)), value)
}

func structuredContentValue(t *testing.T, content toolResultContent) json.RawMessage {
	t.Helper()
	return readAgentStructuredValue(t, content)
}

func TestEveryOwnedToolDeclaresAnOutputSchema(t *testing.T) {
	catalog, err := toolcatalog.Default()
	require.NoError(t, err)
	names := map[string]bool{}
	for _, entry := range catalog.Entries() {
		names[entry.Name] = true
		require.NotEmpty(t, entry.OutputSchema, entry.Name)
		require.NoError(t, jsonschema.ValidateSchema(entry.OutputSchema), entry.Name)
	}
	for _, name := range [][]string{
		toolcatalog.ScheduledMessageToolNames(),
		toolcatalog.InteractionHandlerToolNames(),
		{
			toolcatalog.ToolNameSpawnAgent, toolcatalog.ToolNameReadAgent, toolcatalog.ToolNameSendAgentMessage,
			toolcatalog.ToolNameStopAgent, toolcatalog.ToolNameListAgents,
		},
	} {
		for _, name := range name {
			require.True(t, names[name], "%s is missing from the catalog", name)
		}
	}
	for _, definition := range integrationdefinition.All() {
		for _, tool := range toolcatalog.IntegrationTools(definition.IntegrationKind) {
			schema := integrationOutputSchema(t, definition.IntegrationKind, tool.Operation)
			require.NotEqual(t, `{}`, string(schema), "%s %s", definition.IntegrationKind, tool.Operation)
		}
	}
}

func TestProcessToolResultsMatchOutputSchemas(t *testing.T) {
	process := processresult.Process{ID: uuid.New(), State: "running"}
	id, err := publicid.Encode(publicid.KindProcess, process.ID)
	require.NoError(t, err)
	observation := json.RawMessage(`{"process_id":"` + id + `","output":"star","cursor":0,"next_cursor":16,` +
		`"truncated":false,"output_tail":"le-end","omitted_cursor":4,"tail_cursor":10}`)
	headAndTail := processresult.Action{Payload: json.RawMessage(`{"max_bytes":10,"head_bytes":4}`)}

	runCommand := builtInOutputSchema(t, toolcatalog.ToolNameRunCommand)
	noStdin := processcmd.ForShell("make test", processcmd.ShellDefault, processcmd.IOModePipe)
	noStdin.Shell.NoStdin = true
	running := process
	running.ExecutionSpec = noStdin
	started, err := processresult.Started(running, observation)
	require.NoError(t, err)
	require.Contains(t, string(started), `"note"`)
	requireMatchesOutputSchema(t, runCommand, started)
	unread, err := processresult.Started(process, json.RawMessage(`{"error":"output unavailable"}`))
	require.NoError(t, err)
	requireMatchesOutputSchema(t, runCommand, unread)

	exitCode := 2
	exited := processresult.Process{ID: process.ID, State: "exited", ExitCode: &exitCode}
	_, ended, err := processresult.TerminalOutput(exited, json.RawMessage(
		`{"output":"oops\n","cursor":3,"next_cursor":8,"truncated":true}`))
	require.NoError(t, err)
	requireMatchesOutputSchema(t, runCommand, ended)
	killed := processresult.Process{ID: process.ID, State: "killed", ExitSignal: "SIGKILL",
		StateReasonCode: "terminate_requested", StateReasonMessage: "terminated"}
	_, ended, err = processresult.TerminalOutput(killed, nil)
	require.NoError(t, err)
	requireMatchesOutputSchema(t, runCommand, ended)
	requireRejectedByOutputSchema(t, runCommand, `{"process_id":"`+id+`","state":"running","exit_code":1}`)

	readProcess := builtInOutputSchema(t, toolcatalog.ToolNameReadProcess)
	read, _, err := processresult.Read(process, headAndTail, observation)
	require.NoError(t, err)
	requireMatchesOutputSchema(t, readProcess, read)
	exited.State, exited.StateReasonCode, exited.StateReasonMessage = "failed", "timeout", "timed out"
	read, _, err = processresult.Read(exited, processresult.Action{Payload: json.RawMessage(`{}`)},
		json.RawMessage(`{"process_id":"`+id+`","output":"","cursor":0,"next_cursor":0}`))
	require.NoError(t, err)
	require.Contains(t, string(read), `"error"`)
	requireMatchesOutputSchema(t, readProcess, read)
	failedRead, err := processresult.ReadFailure(process, processresult.Action{}, "output_unavailable", "")
	require.NoError(t, err)
	require.Error(t, jsonschema.Validate(readProcess, failedRead), "error results are not part of the contract")

	for _, name := range []string{toolcatalog.ToolNameWriteProcess, toolcatalog.ToolNameWatchProcess,
		toolcatalog.ToolNameStopProcess} {
		schema := builtInOutputSchema(t, name)
		applied, err := processresult.ActionResult(process.ID, uuid.New(), "applied", "", "")
		require.NoError(t, err)
		requireMatchesOutputSchema(t, schema, applied)
		failed, err := processresult.ActionResult(process.ID, uuid.New(), "failed", "process_terminal",
			"the process ended before this action was accepted")
		require.NoError(t, err)
		requireMatchesOutputSchema(t, schema, failed)
	}
	stopped, err := alreadyStoppedProcessResult(id, executionstore.ProcessActionKindTerminate)
	require.NoError(t, err)
	requireMatchesOutputSchema(t, builtInOutputSchema(t, toolcatalog.ToolNameStopProcess),
		structuredContentValue(t, stopped))

	startedAt := time.Date(2026, 11, 2, 3, 4, 5, 0, time.UTC)
	list := processListObservations([]executionstore.ActiveProcessRecord{
		{ID: process.ID, State: executionstore.ProcessStateRunning, Cwd: "/workspace", SourceStartedAt: &startedAt,
			UpdatedAt: startedAt, ExecutionSpec: processcmd.ForShell("make test", processcmd.ShellDefault,
				processcmd.IOModePipe)},
		{ID: uuid.New(), State: executionstore.ProcessStateStarting, UpdatedAt: startedAt},
	})
	require.Len(t, list, 2)
	requireMatchesOutputSchema(t, builtInOutputSchema(t, toolcatalog.ToolNameListProcesses),
		map[string]any{"processes": list})
}

func TestMachineToolResultsMatchOutputSchemas(t *testing.T) {
	at := time.Date(2026, 11, 2, 3, 4, 5, 0, time.UTC)
	cpu, memory := 4, 8192
	record := executionstore.PoolMachineRecord{
		Binding: executionstore.AgentMachineBindingRecord{
			MachineID: uuid.New(), BindingKind: executionstore.MachineBindingKindPool,
			State: executionstore.AgentMachineBindingStateAttached, Description: "Build box", Cwd: "/workspace",
			CreatedAt: at, UpdatedAt: at,
		},
		Machine: executionstore.MachineRecord{
			ID: uuid.New(), MachinePoolID: uuid.New(), SourceKind: executionstore.MachineSourceKindPool,
			DisplayName: "builder", LifecycleState: executionstore.MachineLifecycleStateProvisioning,
			ConnectionState: executionstore.MachineConnectionStateOffline, ConnectionStateReason: "starting",
			CPU: &cpu, MemoryMB: &memory, Cwd: "/workspace",
		},
		MachinePoolName: "Build Pool",
	}
	created, err := machineProvisioningAcceptedResult(record)
	require.NoError(t, err)
	requireMatchesOutputSchema(t, builtInOutputSchema(t, toolcatalog.ToolNameCreateMachine),
		structuredContentValue(t, created))
	record.Machine.LifecycleState = executionstore.MachineLifecycleStateDeleting
	deleted, err := machineDeletionAcceptedResult(record)
	require.NoError(t, err)
	requireMatchesOutputSchema(t, builtInOutputSchema(t, toolcatalog.ToolNameDeleteMachine),
		structuredContentValue(t, deleted))

	observed := executionstore.AgentMachineObservationRecord{
		MachineID: uuid.New(), SourceKind: executionstore.MachineSourceKindPool,
		BindingKind: executionstore.MachineBindingKindPool, BindingState: executionstore.AgentMachineBindingStateAttached,
		DisplayName: "builder", MachinePoolID: uuid.New(), MachinePoolName: "Build Pool", CPU: &cpu, MemoryMB: &memory,
		LifecycleState: executionstore.MachineLifecycleStateProvisionFailed, ConnectionState: "",
		ConnectionStateReason: "never connected", Description: "Build box", Cwd: "/workspace",
		LifecycleReasonCode: "startup_failed", LifecycleReasonMessage: "startup script failed",
		FailureReport: json.RawMessage(`{"stage":"startup_script","output_tail":"exit 1","output_truncated":false,` +
			`"reported_at":"2026-11-02T03:04:05.123456+00:00","exit_status":1}`),
		BindingCreatedAt: at, BindingUpdatedAt: at,
	}
	inspect := builtInOutputSchema(t, toolcatalog.ToolNameInspectMachine)
	inspection, err := agentMachineInspection(observed)
	require.NoError(t, err)
	requireMatchesOutputSchema(t, inspect, inspection)
	missingGrant := observed
	missingGrant.ProjectGrantMissing = true
	inspection, err = agentMachineInspection(missingGrant)
	require.NoError(t, err)
	requireMatchesOutputSchema(t, inspect, inspection)
	requireRejectedByOutputSchema(t, inspect, `{"machine_id":"mch_x"}`)

	machine, err := agentMachineObservation(observed)
	require.NoError(t, err)
	poolID, err := publicid.Encode(publicid.KindMachinePool, uuid.New())
	require.NoError(t, err)
	listMachines := builtInOutputSchema(t, toolcatalog.ToolNameListMachines)
	requireMatchesOutputSchema(t, listMachines, machineListResult{
		Machines: []machineObservationPayload{machine},
		MachinePools: []machinePoolPayload{{
			MachinePoolID: poolID, MachinePoolName: "Build Pool", Description: "Builders",
			SupportedOverrides: []string{"cpu", "memory_mb"}, DefaultCPU: &cpu, DefaultMemoryMB: &memory,
			MinCPU: &cpu, MaxCPU: &cpu, MinMemoryMB: &memory, MaxMemoryMB: &memory,
		}},
		OffsetPage: listing.ContinueAt(2, true),
	})
	requireMatchesOutputSchema(t, listMachines, machineListResult{
		Machines: []machineObservationPayload{}, MachinePools: []machinePoolPayload{},
	})

	repositories := builtInOutputSchema(t, toolcatalog.ToolNameListGitHubRepositories)
	requireMatchesOutputSchema(t, repositories, githubRepositoryListResult{
		CanPush:      true,
		Repositories: []githubRepositoryPayload{{ID: 42, FullName: "omnara-ai/omnara"}},
		OffsetPage:   listing.ContinueAt(1, true),
	})
	requireMatchesOutputSchema(t, repositories, githubRepositoryListResult{Repositories: []githubRepositoryPayload{}})
}

func TestInteractionToolResultsMatchOutputSchemas(t *testing.T) {
	answers := builtInOutputSchema(t, toolcatalog.ToolNameAskQuestion)
	requireMatchesOutputSchema(t, answers, interactionform.RenderedAnswers{Answers: []interactionform.RenderedAnswer{
		{QuestionIndex: 0, Question: "Ship it?", SelectedOptions: []interactionform.SelectedAnswerOption{
			{OptionIndex: 1, Label: "Other"}}, Text: "After the demo"},
		{QuestionIndex: 1, Question: "Which?", SelectedOptions: []interactionform.SelectedAnswerOption{
			{OptionIndex: 0, Label: "A"}}},
	}})

	inputSchema := json.RawMessage(`{"additionalProperties":false,"properties":{},"type":"object"}`)
	handlers := map[string]agentconfig.InteractionHandlerEntry{}
	for key, scope := range map[string]any{
		"slack":   integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"},
		"github":  integrationdefinition.GitHubScope{RepositoryID: 7, PullRequest: 9},
		"discord": integrationdefinition.DiscordScope{ThreadID: "123"},
	} {
		destination, err := json.Marshal(scope)
		require.NoError(t, err)
		handlers[key] = agentconfig.InteractionHandlerEntry{Destination: destination, Handler: key,
			Description: "Questions and approvals in this agent's assigned conversation.", InputSchema: inputSchema}
	}
	listHandlers := builtInOutputSchema(t, toolcatalog.ToolNameListInteractionHandlers)
	selection := &agentconfig.HandlerSelection{Handler: "slack", IntegrationID: "itg_example",
		Args: json.RawMessage(`{}`), Destination: handlers["slack"].Destination}
	page, err := agentconfig.ListInteractionHandlers(handlers, selection, 0, 2)
	require.NoError(t, err)
	require.True(t, page.HasMore)
	requireMatchesOutputSchema(t, listHandlers, page)
	page, err = agentconfig.ListInteractionHandlers(handlers, nil, 2, 2)
	require.NoError(t, err)
	requireMatchesOutputSchema(t, listHandlers, page)

	setHandler := builtInOutputSchema(t, toolcatalog.ToolNameSetInteractionHandler)
	requireMatchesOutputSchema(t, setHandler, interactionHandlerSelectionResult(
		executionstore.InteractionSelection{HandlerKey: "slack", AutoSelect: true}, json.RawMessage(`{}`)))
	requireMatchesOutputSchema(t, setHandler, interactionHandlerSelectionResult(
		executionstore.InteractionSelection{}, json.RawMessage(`{}`)))
}

func TestStorageAndSkillResultsMatchOutputSchemas(t *testing.T) {
	requireMatchesOutputSchema(t, builtInOutputSchema(t, toolcatalog.ToolNameStorageWrite),
		map[string]any{"path": "/memory/notes/todo.md"})
	upload := builtInOutputSchema(t, toolcatalog.ToolNameStorageUpload)
	requireMatchesOutputSchema(t, upload, map[string]any{"path": "/artifacts/art_" + strings.Repeat("a", 26)})
	requireMatchesOutputSchema(t, upload, map[string]any{"path": "/memory/notes/screenshot.png"})
	requireMatchesOutputSchema(t, builtInOutputSchema(t, toolcatalog.ToolNameStorageDownload),
		map[string]any{"destination": "~/report.md"})
	requireMatchesOutputSchema(t, builtInOutputSchema(t, toolcatalog.ToolNameSkill),
		"<skill_content name=\"deploy\">\nRun make deploy.\n</skill_content>")
}

func TestToolSearchResultsMatchOutputSchema(t *testing.T) {
	schema := builtInOutputSchema(t, toolcatalog.ToolNameToolSearch)
	specs := []modelcontext.ToolSpec{
		{Name: "lookup", Description: "Look up a user.", Deferred: true, CodeMode: true,
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`),
			OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "notify", Deferred: true, InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	search, err := modelcontext.SearchDeferredTools(specs, "lookup|notify", 5)
	require.NoError(t, err)
	require.NotEmpty(t, search.Tools)
	requireMatchesOutputSchema(t, schema, search)
	none, err := modelcontext.SearchDeferredTools(specs, "absent", 5)
	require.NoError(t, err)
	requireMatchesOutputSchema(t, schema, none)
	program, err := modelcontext.SearchTools(specs, "lookup", 5)
	require.NoError(t, err)
	require.NotEmpty(t, program.Tools)
	requireMatchesOutputSchema(t, schema, program)
}

func TestScheduledMessageResultsMatchOutputSchemas(t *testing.T) {
	now := time.Date(2026, 11, 2, 3, 4, 5, 0, time.UTC)
	requireMatchesOutputSchema(t, builtInOutputSchema(t, toolcatalog.ToolNameGetCurrentTime), currentTimeResult(now))

	message, err := scheduledMessageSummaryFromRecord(executionstore.ScheduledMessage{
		ID: uuid.New(), Text: "Check the deploy", CreatedAt: now, ScheduledFor: now.Add(time.Hour),
	})
	require.NoError(t, err)
	next, last := now.Add(time.Hour), now.Add(-time.Hour)
	schedule := executionstore.AgentSchedule{
		ID: uuid.New(), CronExpression: "0 9 * * 1-5", Timezone: "UTC", MessageTemplate: "Standup",
		Enabled: true, NextFireAfter: &next, LastFiredAt: &last, CreatedAt: now,
	}
	recurring, err := scheduleSummaryFromRecord(schedule)
	require.NoError(t, err)
	unscheduled := schedule
	unscheduled.NextFireAfter, unscheduled.LastFiredAt, unscheduled.Enabled = nil, nil, false
	paused, err := scheduleSummaryFromRecord(unscheduled)
	require.NoError(t, err)
	list := builtInOutputSchema(t, toolcatalog.ToolNameListScheduledMessages)
	requireMatchesOutputSchema(t, list, map[string]any{
		"scheduled_messages": []scheduledMessageSummary{message},
		"schedules":          []scheduleSummary{recurring, paused},
	})
	requireMatchesOutputSchema(t, list, map[string]any{
		"scheduled_messages": []scheduledMessageSummary{}, "schedules": []scheduleSummary{},
	})

	create := builtInOutputSchema(t, toolcatalog.ToolNameCreateScheduledMessage)
	requireMatchesOutputSchema(t, create, scheduledMessageResult(message))
	for _, schedule := range []executionstore.AgentSchedule{schedule, unscheduled} {
		result, err := scheduleResult(schedule)
		require.NoError(t, err)
		requireMatchesOutputSchema(t, create, result)
	}

	remove := builtInOutputSchema(t, toolcatalog.ToolNameDeleteScheduledMessage)
	requireMatchesOutputSchema(t, remove, map[string]any{"id": message.ID, "deleted": true})
	requireMatchesOutputSchema(t, remove, map[string]any{"id": recurring.ID, "deleted": true, "recurring": true})
}

func TestSubagentToolResultsMatchOutputSchemas(t *testing.T) {
	child := executionstore.AgentRecord{ID: uuid.New(), Name: "Researcher"}
	spawned, err := spawnAgentResultContent(child, "research")
	require.NoError(t, err)
	requireMatchesOutputSchema(t, builtInOutputSchema(t, toolcatalog.ToolNameSpawnAgent),
		structuredContentValue(t, spawned))

	agentID, err := publicid.Encode(publicid.KindAgent, child.ID)
	require.NoError(t, err)
	requireMatchesOutputSchema(t, builtInOutputSchema(t, toolcatalog.ToolNameSendAgentMessage),
		agentMessageDeliveredResult(agentID, ""))
	stop := builtInOutputSchema(t, toolcatalog.ToolNameStopAgent)
	requireMatchesOutputSchema(t, stop, agentStoppedResult(agentID, "Researcher", true))
	requireMatchesOutputSchema(t, stop, agentStoppedResult(agentID, "Researcher", false))
	requireRejectedByOutputSchema(t, stop, `{"agent_id":"agt_x","name":"","archived":true,"message":"x"}`)

	status := executionstore.SubagentStatus{
		AgentID: child.ID, Name: "Researcher", Key: "research", State: executionstore.SubagentStateRunning,
		LastActivityAt: time.Now(),
	}
	status.RunningProcesses.Count, status.WorkingSubagents.Count = 2, 1
	summary, err := subagentSummaryFromStatus(status)
	require.NoError(t, err)
	idle := status
	idle.Name, idle.State = "", executionstore.SubagentStateIdle
	idle.RunningProcesses.Count, idle.WorkingSubagents.Count = 0, 0
	idleSummary, err := subagentSummaryFromStatus(idle)
	require.NoError(t, err)
	requireMatchesOutputSchema(t, builtInOutputSchema(t, toolcatalog.ToolNameListAgents),
		map[string]any{"agents": []subagentSummary{summary, idleSummary}})

	readAgent := builtInOutputSchema(t, toolcatalog.ToolNameReadAgent)
	mediaID, toolCallID := uuid.New(), uuid.New()
	events := make([]executionstore.AgentEventReadRecord, 0, 4)
	for _, kind := range []string{"agent_input", "model_output", "tool_result", "context_checkpoint"} {
		event := readAgentEventFixture(kind)
		event.AgentID = child.ID
		switch kind {
		case "agent_input":
			event.ContentBlocks = json.RawMessage(`[{"type":"text","text":"Please investigate"},` +
				`{"type":"media_ref","artifact_id":"` + mediaID.String() + `"}]`)
		case "model_output":
			event.ContentBlocks = json.RawMessage(`[{"type":"reasoning","text":"Checking"},` +
				`{"type":"tool_call","tool_call_id":"` + toolCallID.String() + `","tool_type":"built_in",` +
				`"name":"web_search","input":{"query":"omnara"}},{"type":"text","text":"Found it"},` +
				`{"type":"error","text":"partial"}]`)
		case "tool_result":
			event.ContentBlocks = json.RawMessage(`[{"type":"structured_data","value":{"results":[]}},` +
				`{"type":"text","text":"done"},{"type":"media_ref","artifact_id":"` + mediaID.String() + `",` +
				`"exclude_from_model_context":false}]`)
		}
		events = append(events, event)
	}
	turn := executionstore.AgentTurnReadRecord{
		AgentTurnRecord: executionstore.AgentTurnRecord{ID: events[0].TurnID, AgentID: child.ID, TurnSequence: 3},
		EventCount:      4, StartedAt: events[0].CreatedAt, UpdatedAt: events[0].CreatedAt,
		OpeningEvents: events[:1], LatestEvent: events[3], LatestSemanticEvent: events[1],
	}
	older := turn
	older.ID, older.TurnSequence = uuid.New(), 2
	turns, err := readAgentTurnsContent(status, []executionstore.AgentTurnReadRecord{turn, older}, 1)
	require.NoError(t, err)
	value := structuredContentValue(t, turns)
	require.Contains(t, string(value), `"latest_semantic_event"`)
	requireMatchesOutputSchema(t, readAgent, value)
	empty, err := readAgentTurnsContent(idle, nil, 10)
	require.NoError(t, err)
	requireMatchesOutputSchema(t, readAgent, structuredContentValue(t, empty))
	turnID, err := publicid.Encode(publicid.KindAgentTurn, events[0].TurnID)
	require.NoError(t, err)
	page, err := readAgentTurnEventsContent(status, turnID, events, 3)
	require.NoError(t, err)
	requireMatchesOutputSchema(t, readAgent, structuredContentValue(t, page))
}

func TestIntegrationToolResultsMatchOutputSchemas(t *testing.T) {
	slackPost := integrationOutputSchema(t, integrationdefinition.SlackThread,
		toolcatalog.IntegrationOperationPostMessage)
	requireMatchesOutputSchema(t, slackPost, tooloutput.SlackMessage{
		Integration: "slack", ChannelID: "C123", MessageTS: "1.2", ThreadTS: ""})
	requireMatchesOutputSchema(t, slackPost, tooloutput.SlackFiles{
		Integration: "slack", ChannelID: "C123", ThreadTS: "1.2", FileIDs: []string{"F1"}})
	requireMatchesOutputSchema(t, integrationOutputSchema(t, integrationdefinition.DiscordThread,
		toolcatalog.IntegrationOperationPostMessage), tooloutput.DiscordMessage{
		Integration: "discord", ChannelID: "123", MessageID: "456", ThreadID: "123"})

	github := func(operation string) json.RawMessage {
		return integrationOutputSchema(t, integrationdefinition.GitHubPR, operation)
	}
	at := time.Date(2026, 11, 2, 3, 4, 5, 0, time.UTC)
	comment := githubDiscussionComment(at)
	line := 7
	reviewComment := githubReviewComment(comment, &line)
	requireMatchesOutputSchema(t, github(toolcatalog.IntegrationOperationDiscussionComment), comment)
	requireMatchesOutputSchema(t, github(toolcatalog.IntegrationOperationReply), reviewComment)
	requireMatchesOutputSchema(t, github(toolcatalog.IntegrationOperationReply), githubReviewComment(comment, nil))
	reviewCommentSchema := github(toolcatalog.IntegrationOperationReviewComment)
	requireMatchesOutputSchema(t, reviewCommentSchema, reviewComment)
	requireMatchesOutputSchema(t, reviewCommentSchema, githubPendingReviewComment())

	receipt := githubReviewReceipt("PENDING")
	requireMatchesOutputSchema(t, github(toolcatalog.IntegrationOperationStartReview), receipt)
	requireRejectedByOutputSchema(t, github(toolcatalog.IntegrationOperationStartReview),
		mustJSON(t, githubReviewReceipt("COMMENTED")))
	requireMatchesOutputSchema(t, github(toolcatalog.IntegrationOperationSubmitReview), githubReviewReceipt("COMMENTED"))
	requireMatchesOutputSchema(t, github(toolcatalog.IntegrationOperationDiscardReview),
		githubDiscardedReview())
	requireMatchesOutputSchema(t, github(toolcatalog.IntegrationOperationResolveThread),
		githubResolvedThread())

	requireMatchesOutputSchema(t, github(toolcatalog.IntegrationOperationAttachPullRequest),
		tooloutput.GitHubPullRequestAttachment{RepositoryID: 7, PullRequest: 9, URL: "https://github.com/o/r/pull/9"})
	requireMatchesOutputSchema(t, github(toolcatalog.IntegrationOperationDetachPullRequest),
		tooloutput.GitHubPullRequestAttachment{RepositoryID: 7, PullRequest: 9})
	attached := github(toolcatalog.IntegrationOperationListAttachedPullRequests)
	requireMatchesOutputSchema(t, attached, tooloutput.GitHubAttachedPullRequests{
		PullRequests: []integrationdefinition.GitHubScope{{RepositoryID: 7, PullRequest: 9}},
		OffsetPage:   listing.ContinueAt(1, true),
	})
	requireMatchesOutputSchema(t, attached, tooloutput.GitHubAttachedPullRequests{
		PullRequests: []integrationdefinition.GitHubScope{}})
}

func TestIntegrationResultTypesMatchOutputSchemas(t *testing.T) {
	cases := []struct {
		kind      integrationdefinition.Kind
		operation string
		value     any
	}{
		{integrationdefinition.SlackThread, toolcatalog.IntegrationOperationPostMessage, tooloutput.SlackMessage{}},
		{integrationdefinition.SlackThread, toolcatalog.IntegrationOperationPostMessage, tooloutput.SlackFiles{}},
		{integrationdefinition.DiscordThread, toolcatalog.IntegrationOperationPostMessage, tooloutput.DiscordMessage{}},
		{integrationdefinition.GitHubPR, toolcatalog.IntegrationOperationDiscussionComment, github.DiscussionComment{}},
		{integrationdefinition.GitHubPR, toolcatalog.IntegrationOperationReply, github.ReviewComment{}},
		{integrationdefinition.GitHubPR, toolcatalog.IntegrationOperationReviewComment, github.ReviewComment{}},
		{integrationdefinition.GitHubPR, toolcatalog.IntegrationOperationAttachPullRequest,
			tooloutput.GitHubPullRequestAttachment{}},
		{integrationdefinition.GitHubPR, toolcatalog.IntegrationOperationListAttachedPullRequests,
			tooloutput.GitHubAttachedPullRequests{}},
	}
	for _, test := range cases {
		schema := integrationOutputSchema(t, test.kind, test.operation)
		full := reflect.New(reflect.TypeOf(test.value))
		populated(full.Elem())
		requireMatchesOutputSchema(t, schema, full.Interface())
	}
}

func githubDiscussionComment(at time.Time) github.DiscussionComment {
	return github.DiscussionComment{ID: 11, HTMLURL: "https://github.com/o/r/pull/9#issuecomment-11", Body: "LGTM",
		User: github.User{ID: 3, Login: "omnara[bot]", Type: "Bot"}, CreatedAt: at, UpdatedAt: at}
}

func githubReviewComment(comment github.DiscussionComment, line *int) github.ReviewComment {
	return github.ReviewComment{DiscussionComment: comment, PullRequestURL: "https://api.github.com/repos/o/r/pulls/9",
		PullRequestReviewID: 12, InReplyToID: 10, CommitID: "abc", OriginalCommitID: "abc", Path: "main.go",
		DiffHunk: "@@ -1 +1 @@", Line: line, OriginalLine: line, Side: "RIGHT"}
}

func githubPendingReviewComment() github.PendingReviewComment {
	return github.PendingReviewComment{ID: 13, ReviewID: 12, State: "PENDING", HTMLURL: "https://github.com/o/r/pull/9"}
}

func githubReviewReceipt(state string) github.ReviewReceipt {
	return github.ReviewReceipt{ID: 12, State: state, CommitID: "abc", HTMLURL: "https://github.com/o/r/pull/9"}
}

func githubDiscardedReview() github.DiscardedReview {
	return github.DiscardedReview{ID: 12, Deleted: true}
}

func githubResolvedThread() github.ResolvedReviewThread {
	return github.ResolvedReviewThread{ID: "PRRT_kw", IsResolved: true}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}
