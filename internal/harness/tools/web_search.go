package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/textutil"
	"github.com/omnara-ai/omnara/internal/webaccess"
	"github.com/omnara-ai/omnara/internal/webaccess/telemlineage"
)

const (
	webSearchDefaultResults     = 5
	webSearchMaxResults         = 20
	webSearchInlineSnippetChars = 600
)

type webSearchRequest struct {
	Query      string          `json:"query"`
	NumResults json.RawMessage `json:"num_results,omitempty"`
	Recency    json.RawMessage `json:"recency,omitempty"`
	Domains    json.RawMessage `json:"domains,omitempty"`
}

type resolvedWebSearchRequest struct {
	Query      string
	NumResults int
	Recency    string
	Domains    []string
}

func validateWebSearchInput(input json.RawMessage) error {
	_, err := resolveWebSearchRequest(input)
	return err
}

func resolveWebSearchRequest(raw json.RawMessage) (resolvedWebSearchRequest, error) {
	var input webSearchRequest
	if err := decodeSingleStrictJSON(raw, &input, "web_search request"); err != nil {
		return resolvedWebSearchRequest{}, fmt.Errorf("parse web_search request: %w", err)
	}
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return resolvedWebSearchRequest{}, errors.New("query is required")
	}
	resolved := resolvedWebSearchRequest{Query: query, NumResults: webSearchDefaultResults}
	if len(input.NumResults) != 0 {
		var numResults *int
		if err := json.Unmarshal(input.NumResults, &numResults); err != nil {
			return resolvedWebSearchRequest{}, fmt.Errorf("parse num_results: %w", err)
		}
		if numResults == nil {
			return resolvedWebSearchRequest{}, errors.New("num_results cannot be null")
		}
		if *numResults < 1 || *numResults > webSearchMaxResults {
			return resolvedWebSearchRequest{}, fmt.Errorf(
				"num_results must be between 1 and %d",
				webSearchMaxResults,
			)
		}
		resolved.NumResults = *numResults
	}
	if len(input.Recency) != 0 {
		var recency *string
		if err := json.Unmarshal(input.Recency, &recency); err != nil {
			return resolvedWebSearchRequest{}, fmt.Errorf("parse recency: %w", err)
		}
		if recency == nil {
			return resolvedWebSearchRequest{}, errors.New("recency cannot be null")
		}
		switch *recency {
		case "day", "week", "month", "year":
			resolved.Recency = *recency
		default:
			return resolvedWebSearchRequest{}, fmt.Errorf(
				"recency must be one of day, week, month, year (got %q)",
				*recency,
			)
		}
	}
	if len(input.Domains) != 0 {
		var domains []string
		if err := json.Unmarshal(input.Domains, &domains); err != nil {
			return resolvedWebSearchRequest{}, fmt.Errorf("parse domains: %w", err)
		}
		for _, domain := range domains {
			if strings.TrimSpace(domain) == "" || strings.TrimSpace(domain) == "-" {
				return resolvedWebSearchRequest{}, errors.New("domains entries cannot be blank")
			}
		}
		resolved.Domains = domains
	}
	return resolved, nil
}

func runWebSearch(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	resolved, err := resolveWebSearchRequest(call.Call.Input)
	if err != nil {
		return failWebTool(
			webaccess.ErrorCodeProviderFailed,
			err.Error(),
			false,
			err,
		)
	}
	if call.Executor.WebSearch == nil {
		message := "web search is not configured on this deployment"
		return failWebTool(
			webaccess.ErrorCodeSearchUnavailable,
			message,
			false,
			errors.New(message),
		)
	}
	request := webaccess.SearchRequest{
		Query:      resolved.Query,
		NumResults: resolved.NumResults,
		Recency:    resolved.Recency,
		Domains:    resolved.Domains,
	}
	// Only Telem links searches by agent lineage, so other providers skip the
	// store reads.
	if _, telem := call.Executor.WebSearch.(webaccess.TelemProvider); telem {
		request.Lineage = webSearchLineage(ctx, call)
	}
	response, err := call.Executor.WebSearch.Search(ctx, request)
	if err != nil {
		if providerErr, ok := webaccess.AsProviderError(err); ok {
			return failWebTool(
				providerErr.Code,
				providerErr.Message,
				providerErr.Retryable,
				providerErr,
			)
		}
		return failWebTool(
			webaccess.ErrorCodeProviderFailed,
			err.Error(),
			false,
			err,
		)
	}
	content, err := webSearchToolResultContent(resolved.Query, response)
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(content), nil
}

// webSearchLineage is best effort: the search runs with whatever lineage
// loads, down to the ids the call already has.
func webSearchLineage(ctx context.Context, call asyncToolContext) telemlineage.Lineage {
	self := telemlineage.Call{
		AgentID:            call.Turn.AgentID,
		ModelCallContextID: call.Turn.ModelCallContextID,
		ToolCallID:         call.ToolCallID,
	}
	if call.Executor.Store == nil {
		return telemlineage.Lineage{Call: self}
	}
	return loadWebSearchLineage(ctx, call.Executor.Store, call.Turn.ProjectID, self, call.Executor.logger())
}

// loadWebSearchLineage completes the call's own ids, the agent's goal and the
// chain of parent spawn_agent calls. Each part degrades on its own: a failure
// drops only that part and is logged.
func loadWebSearchLineage(
	ctx context.Context,
	store *storage.Store,
	projectID uuid.UUID,
	self telemlineage.Call,
	log *slog.Logger,
) telemlineage.Lineage {
	warn := func(part string, err error) {
		log.Warn("load web search lineage", "part", part, "agent_id", self.AgentID, "error", err)
	}
	lineage := telemlineage.Lineage{Call: self}
	if call, err := completeLineageCall(ctx, store, projectID, self); err != nil {
		warn("call", err)
	} else {
		lineage.Call = call
	}
	goal, err := lineageGoal(ctx, store, projectID, self.AgentID)
	if err != nil {
		warn("goal", err)
	}
	lineage.Goal = goal
	ancestors, err := lineageAncestors(ctx, store, projectID, self.AgentID)
	if err != nil {
		warn("ancestors", err)
	}
	lineage.Ancestors = ancestors
	return lineage
}

// lineageAncestors walks up the agent tree through each parent's spawn_agent
// call and returns the chain root first.
func lineageAncestors(
	ctx context.Context,
	store *storage.Store,
	projectID, agentID uuid.UUID,
) ([]telemlineage.Call, error) {
	var ancestors []telemlineage.Call
	for range agentconfig.MaxSubagentDepth {
		agent, err := store.Execution().GetAgentInProject(ctx, projectID, agentID)
		if err != nil {
			return nil, fmt.Errorf("get agent: %w", err)
		}
		if agent.ParentAgentID == uuid.Nil {
			break
		}
		spawnID, err := spawnToolCallID(agent)
		if err != nil {
			return nil, err
		}
		spawn, err := completeLineageCall(ctx, store, projectID, telemlineage.Call{
			AgentID:    agent.ParentAgentID,
			ToolCallID: spawnID,
		})
		if err != nil {
			return nil, err
		}
		ancestors = append(ancestors, spawn)
		agentID = agent.ParentAgentID
	}
	slices.Reverse(ancestors)
	return ancestors, nil
}

const (
	lineageGoalMaxRunes = 1000
	lineageGoalPageSize = 20
)

// lineageGoal is the agent's task: the spawn_agent task for a subagent, and
// the text of the first message a top-level agent received.
func lineageGoal(ctx context.Context, store *storage.Store, projectID, agentID uuid.UUID) (string, error) {
	agent, err := store.Execution().GetAgentInProject(ctx, projectID, agentID)
	if err != nil {
		return "", fmt.Errorf("get agent: %w", err)
	}
	var goal string
	if agent.ParentAgentID != uuid.Nil {
		spawnID, err := spawnToolCallID(agent)
		if err != nil {
			return "", err
		}
		spawn, err := store.Execution().GetToolCall(ctx, projectID, agent.ParentAgentID, spawnID)
		if err != nil {
			return "", fmt.Errorf("get spawn tool call: %w", err)
		}
		request, err := resolveSpawnAgentRequest(spawn.Input)
		if err != nil {
			return "", err
		}
		goal = request.Task
	} else {
		var after int64
		for {
			page, err := store.Execution().ListAgentEventsForRead(ctx, projectID, agentID, after, lineageGoalPageSize)
			if err != nil {
				return "", fmt.Errorf("list agent events: %w", err)
			}
			text, resolved := firstContentInputText(page)
			if resolved {
				goal = text
				break
			}
			if len(page) < lineageGoalPageSize {
				break
			}
			after = page[len(page)-1].Sequence
		}
	}
	return textutil.TruncateRunes(strings.TrimSpace(goal), lineageGoalMaxRunes), nil
}

// firstContentInputText returns the text of the first content input among
// events in sequence order, and whether one was found. A first message
// without text, such as a file alone, gives no goal: later events are never
// read, so a follow-up message cannot become the goal.
func firstContentInputText(records []executionstore.AgentEventReadRecord) (string, bool) {
	for _, event := range records {
		if event.EventKind != string(events.KindAgentInput) || event.InputKind != "content" {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(event.ContentBlocks, &blocks) != nil {
			return "", true
		}
		var texts []string
		for _, block := range blocks {
			if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
				texts = append(texts, block.Text)
			}
		}
		return strings.TrimSpace(strings.Join(texts, "\n")), true
	}
	return "", false
}

func spawnToolCallID(agent executionstore.AgentRecord) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimPrefix(agent.IdempotencyKey, spawnIdempotencyKeyPrefix))
	if err != nil {
		return uuid.Nil, fmt.Errorf("subagent %s has no spawn tool call: %w", agent.ID, err)
	}
	return id, nil
}

// completeLineageCall fills the tool call's model call, creation time and
// context window (the latest compaction checkpoint).
func completeLineageCall(
	ctx context.Context,
	store *storage.Store,
	projectID uuid.UUID,
	call telemlineage.Call,
) (telemlineage.Call, error) {
	record, err := store.Execution().GetToolCall(ctx, projectID, call.AgentID, call.ToolCallID)
	if err != nil {
		return telemlineage.Call{}, fmt.Errorf("get tool call: %w", err)
	}
	call.ModelCallContextID = record.ModelCallContextID
	call.CreatedAt = record.CreatedAt
	modelCall, found, err := store.Execution().GetModelCallContext(ctx, projectID, call.AgentID, call.ModelCallContextID)
	if err != nil {
		return telemlineage.Call{}, fmt.Errorf("get model call context: %w", err)
	}
	if !found {
		return telemlineage.Call{}, fmt.Errorf("model call context %s not found", call.ModelCallContextID)
	}
	checkpoint, found, err := store.Execution().GetLatestApplicableContextCheckpoint(
		ctx, projectID, call.AgentID, modelCall.InputEventSequence,
	)
	if err != nil {
		return telemlineage.Call{}, fmt.Errorf("get context checkpoint: %w", err)
	}
	if found {
		call.WindowID = checkpoint.ID
	}
	return call, nil
}

func webSearchToolResultContent(
	query string,
	response webaccess.SearchResponse,
) (toolResultContent, error) {
	var rendered strings.Builder
	fmt.Fprintf(&rendered, "Web search results for %q:\n", query)
	if len(response.Results) == 0 {
		rendered.WriteString("No results found. Try a different query.")
	}
	for index, result := range response.Results {
		fmt.Fprintf(&rendered, "\n%d. %s\n   %s\n", index+1, result.Title, result.URL)
		snippet := strings.TrimSpace(result.Snippet)
		if snippet != "" {
			if len(snippet) > webSearchInlineSnippetChars {
				snippet = textutil.TruncateRunes(snippet, webSearchInlineSnippetChars) + "…"
			}
			fmt.Fprintf(&rendered, "   %s\n", snippet)
		}
	}
	structured := map[string]any{
		"query":    query,
		"provider": response.Provider,
		"results":  response.Results,
	}
	structuredPart, err := structuredToolResultPart(structured)
	if err != nil {
		return toolResultContent{}, err
	}
	return newToolResultContent(
		textToolResultPart(rendered.String()),
		structuredPart,
	), nil
}
