package agentexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

const (
	processNotificationScope       = "process_notification"
	processNotificationActorUserID = "process_notifications"
	processNotificationActorName   = "Process notifications"
	processNotificationTailBytes   = 2 * 1024
	processNotificationTailLines   = 20
	processNotificationLabelChars  = 200
)

// notifyProcessExit tells the agent that a process it left running in the
// background has ended. It is a steering input, so it does not cancel open
// questions or permission requests; it waits for them like any other input.
func (h *Handle) notifyProcessExit(
	ctx context.Context,
	row executiondb.ReadExecutionProcessOwnerRow,
	observed json.RawMessage,
) error {
	identity, err := executiondb.New().ReadExecutionAgentIdentity(ctx, h.unit.DB(),
		executiondb.ReadExecutionAgentIdentityParams{ProjectID: h.route.ProjectID, ID: h.route.AgentID})
	if err != nil {
		return err
	}
	if identity.State == "archived" {
		return nil
	}
	processID, err := publicid.Encode(publicid.KindProcess, row.ID)
	if err != nil {
		return err
	}
	actorID, err := h.processNotificationActor(ctx, row)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(map[string]any{
		processNotificationScope: map[string]any{"kind": "exit", "process_id": processID},
	})
	if err != nil {
		return err
	}
	_, err = h.ReceiveContent(ctx, ReceiveContentInput{
		capacityExempt:   true,
		ActorID:          actorID,
		DeliveryMode:     "steering",
		IdempotencyScope: processNotificationScope,
		IdempotencyKey:   "exit:" + row.ID.String(),
		Metadata:         metadata,
		Content:          []Content{{Kind: "text", Text: renderProcessExit(processID, row, observed)}},
	})
	return err
}

func (h *Handle) processNotificationActor(
	ctx context.Context,
	row executiondb.ReadExecutionProcessOwnerRow,
) (uuid.UUID, error) {
	tenant, err := publicid.Encode(publicid.KindOrganization, row.OrgID)
	if err != nil {
		return uuid.Nil, err
	}
	q := dbsqlc.New(h.unit.DB())
	actor, err := q.GetActorByIdentity(ctx, dbsqlc.GetActorByIdentityParams{
		ProjectID:        h.route.ProjectID,
		Provider:         "omnara",
		ProviderTenantID: &tenant,
		ProviderUserID:   processNotificationActorUserID,
	})
	if err == nil {
		return actor.ID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	created, err := q.UpsertActorIdentity(ctx, dbsqlc.UpsertActorIdentityParams{
		ProjectID:        h.route.ProjectID,
		Provider:         "omnara",
		ProviderTenantID: &tenant,
		ProviderUserID:   processNotificationActorUserID,
		DisplayName:      processNotificationActorName,
		Metadata:         json.RawMessage(`{}`),
	})
	if err != nil {
		return uuid.Nil, err
	}
	return created.ID, nil
}

type processExitObservation struct {
	Output     *string `json:"output"`
	NextCursor *int64  `json:"next_cursor"`
	Truncated  bool    `json:"truncated"`
}

func renderProcessExit(
	processID string,
	row executiondb.ReadExecutionProcessOwnerRow,
	observed json.RawMessage,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Background process %s (`%s`) %s", processID,
		truncateRunes(row.ExecutionSpec.Label(), processNotificationLabelChars), processEndPhrase(row))
	if row.SourceStartedAt != nil && row.SourceEndedAt != nil {
		fmt.Fprintf(&b, " after %s", row.SourceEndedAt.Sub(*row.SourceStartedAt).Round(time.Second))
	}
	b.WriteString(".")
	var output processExitObservation
	if len(observed) == 0 || json.Unmarshal(observed, &output) != nil || output.Output == nil {
		b.WriteString(" Use read_process to read its output.")
		return b.String()
	}
	if *output.Output == "" {
		b.WriteString(" It produced no output.")
		return b.String()
	}
	if output.NextCursor != nil {
		fmt.Fprintf(&b, " Output: %s.", formatBytes(*output.NextCursor))
	}
	if output.Truncated {
		b.WriteString(" Use read_process to read it.")
		return b.String()
	}
	tail, complete := outputTail(*output.Output)
	if complete {
		b.WriteString("\n\n")
	} else {
		b.WriteString(" Last lines (use read_process for the rest):\n\n")
	}
	fence := codeFence(tail)
	b.WriteString(fence + "\n" + strings.TrimRight(tail, "\n") + "\n" + fence)
	return b.String()
}

func processEndPhrase(row executiondb.ReadExecutionProcessOwnerRow) string {
	reason := strings.TrimSpace(row.StateReasonMessage)
	if reason == "" {
		reason = storeutil.ValueOrZero(row.StateReasonCode)
	}
	switch row.State {
	case "exited":
		if row.ExitCode != nil {
			return fmt.Sprintf("exited with code %d", *row.ExitCode)
		}
		return "exited"
	case "killed":
		if row.ExitSignal != "" {
			return "was killed by " + row.ExitSignal
		}
		return "was killed"
	case "failed":
		if reason != "" {
			return "failed (" + reason + ")"
		}
		return "failed"
	default:
		if reason != "" {
			return "ended in an unknown state (" + reason + ")"
		}
		return "ended in an unknown state"
	}
}

// outputTail returns the last lines of output, and whether they are all of it.
func outputTail(output string) (string, bool) {
	tail := output
	if len(tail) > processNotificationTailBytes {
		tail = tail[len(tail)-processNotificationTailBytes:]
		for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
			tail = tail[1:]
		}
	}
	trimmed := strings.TrimRight(tail, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) > processNotificationTailLines {
		lines = lines[len(lines)-processNotificationTailLines:]
	} else if len(tail) < len(output) && len(lines) > 1 {
		lines = lines[1:]
	}
	tail = strings.Join(lines, "\n")
	return tail, len(tail) == len(strings.TrimRight(output, "\n"))
}

func codeFence(text string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

func formatBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d bytes", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	}
}

func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit]) + "…"
}
