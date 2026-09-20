package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

type inboxOperatorStore interface {
	ListIntegrationInbox(
		context.Context,
		integrationstore.ListIntegrationInboxInput,
	) (integrationstore.ListIntegrationInboxResult, error)
	GetIntegrationInbox(context.Context, uuid.UUID, uuid.UUID) (integrationstore.IntegrationInboxRecord, error)
	RetryFailedIntegrationInbox(context.Context, uuid.UUID, uuid.UUID) error
	DiscardFailedIntegrationInbox(context.Context, uuid.UUID, uuid.UUID) error
}

type inboxCommand struct {
	action               string
	projectID, receiptID uuid.UUID
	list                 integrationstore.ListIntegrationInboxInput
	reason               string
}

func runInboxCLI(ctx context.Context, args []string, output, diagnostics io.Writer) error {
	if len(args) == 0 || args[0] != "inbox" {
		return errors.New("usage: maintenance inbox list|show|retry|discard [flags]")
	}
	command, err := parseInboxCommand(args[1:], diagnostics)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	// Recovery needs database access, without Redis or a running maintenance
	// service. Discard may use the configured blob store after its commit.
	url := os.Getenv("OMNARA_DATABASE_URL")
	if strings.TrimSpace(url) == "" {
		return errors.New("OMNARA_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	pool, err := storage.Open(ctx, url, storage.WithDefaultApplicationName("omnara-maintenance-inbox"))
	if err != nil {
		return fmt.Errorf("open inbox database: %w", err)
	}
	defer pool.Close()
	if err := command.run(ctx, storage.NewStore(pool).Integrations(), output); err != nil {
		return err
	}
	if command.action == "discard" {
		if err := cleanupDiscardedInbox(ctx, pool, command.projectID, command.receiptID); err != nil {
			_, _ = fmt.Fprintf(diagnostics,
				"warning: discard remains committed for receipt %s; prepared-artifact cleanup failed: %v; "+
					"retry the same discard command while the receipt is retained\n", command.receiptID, err)
		}
	}
	return nil
}

func parseInboxCommand(args []string, diagnostics io.Writer) (inboxCommand, error) {
	if len(args) == 0 {
		return inboxCommand{}, errors.New("inbox action is required: list, show, retry or discard")
	}
	c := inboxCommand{action: args[0]}
	flags := flag.NewFlagSet("maintenance inbox "+c.action, flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	project := flags.String("project", "", "project public ID (required)")
	var app, state, after, receipt string
	limit := 50
	switch c.action {
	case "list":
		flags.StringVar(&app, "app", "", "app public ID filter")
		flags.StringVar(&state, "state", "failed", "pending, processing, failed, completed, discarded or all")
		flags.IntVar(&limit, "limit", 50, "page size (1-100)")
		flags.StringVar(&after, "after", "", "continuation cursor from the previous page")
	case "show", "retry", "discard":
		flags.StringVar(&receipt, "receipt", "", "internal receipt UUID (required)")
		if c.action == "discard" {
			flags.StringVar(&c.reason, "reason", "", "operator reason (required; metadata only)")
		}
	default:
		return c, fmt.Errorf("unknown inbox action %q", c.action)
	}
	if err := flags.Parse(args[1:]); err != nil {
		return c, err
	}
	if flags.NArg() != 0 {
		return c, errors.New("unexpected positional arguments")
	}
	var err error
	c.projectID, err = publicid.Decode(publicid.KindProject, *project)
	if err != nil {
		return c, fmt.Errorf("invalid --project: %w", err)
	}
	if c.action != "list" {
		c.receiptID, err = uuid.Parse(receipt)
		if err != nil || c.receiptID == uuid.Nil {
			return c, errors.New("--receipt must be a nonzero UUID")
		}
		if c.action == "discard" && (strings.TrimSpace(c.reason) == "" || len(c.reason) > 1024) {
			return c, errors.New("--reason must contain 1-1024 bytes of operator metadata")
		}
		return c, nil
	}
	c.list = integrationstore.ListIntegrationInboxInput{ProjectID: c.projectID, Limit: limit}
	if limit < 1 || limit > integrationstore.IntegrationInboxMaxBatch {
		return c, errors.New("--limit must be between 1 and 100")
	}
	switch state {
	case "all":
	case "pending", "processing", "failed", "completed", "discarded":
		c.list.State = integrationstore.IntegrationInboxState(state)
	default:
		return c, errors.New("invalid --state")
	}
	if app != "" {
		c.list.AppID, err = publicid.Decode(publicid.KindProjectApp, app)
		if err != nil {
			return c, fmt.Errorf("invalid --app: %w", err)
		}
	}
	if after != "" {
		if len(after) > 1024 {
			return c, errors.New("invalid --after cursor")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(after)
		if err != nil || json.Unmarshal(decoded, &c.list.After) != nil || !c.list.After.Set ||
			c.list.After.ID == uuid.Nil || c.list.After.CreatedAt.IsZero() {
			return c, errors.New("invalid --after cursor")
		}
	}
	return c, nil
}

type inboxReceiptView struct {
	ID          uuid.UUID                               `json:"receipt_id"`
	ProjectID   string                                  `json:"project_id"`
	AppID       string                                  `json:"app_id"`
	ReceiptKey  string                                  `json:"receipt_key"`
	State       integrationstore.IntegrationInboxState  `json:"state"`
	Source      integrationstore.IntegrationInboxSource `json:"source,omitempty"`
	Scheduled   *inboxScheduledPublicationView          `json:"scheduled,omitempty"`
	Attempts    int                                     `json:"attempt_count"`
	CreatedAt   time.Time                               `json:"created_at"`
	UpdatedAt   time.Time                               `json:"updated_at"`
	AvailableAt time.Time                               `json:"available_at"`
	TerminalAt  *time.Time                              `json:"terminal_at,omitempty"`
	LastError   string                                  `json:"last_error,omitempty"`
	Slots       []inboxSlotView                         `json:"slots,omitempty"`
}

// An attempt without a known root is uncertain, not proof of non-delivery.
// A known root confirms publication evidence, not agent launch or report delivery.
// Expose only these facts; never encode the root address or raw preparation.
type inboxScheduledPublicationView struct {
	AttemptedAt *time.Time `json:"attempted_at,omitempty"`
	RootKnown   bool       `json:"root_known"`
}

// Deliberately project only identity references and stage presence. Never encode
// a raw receipt, plan, compiled config, provider payload, message or preparation.
type inboxSlotView struct {
	Key          string                              `json:"key"`
	AgentID      *uuid.UUID                          `json:"agent_id,omitempty"`
	BaseConfigID *uuid.UUID                          `json:"base_config_id,omitempty"`
	ArtifactIDs  []uuid.UUID                         `json:"artifact_ids,omitempty"`
	Selection    *integrationstore.InboxAppSelection `json:"selection,omitempty"`
	Prepared     bool                                `json:"prepared"`
	Committed    bool                                `json:"committed"`
}

func inboxView(summary integrationstore.IntegrationInboxSummary) (inboxReceiptView, error) {
	project, err := publicid.Encode(publicid.KindProject, summary.ProjectID)
	if err != nil {
		return inboxReceiptView{}, err
	}
	app, err := publicid.Encode(publicid.KindProjectApp, summary.AppID)
	if err != nil {
		return inboxReceiptView{}, err
	}
	return inboxReceiptView{ID: summary.ID, ProjectID: project, AppID: app,
		ReceiptKey: summary.ReceiptKey, State: summary.State, Attempts: summary.AttemptCount,
		CreatedAt: summary.CreatedAt, UpdatedAt: summary.UpdatedAt, AvailableAt: summary.AvailableAt,
		TerminalAt: summary.CompletedAt, LastError: summary.LastError}, nil
}

func (c inboxCommand) run(ctx context.Context, store inboxOperatorStore, output io.Writer) error {
	encoder := json.NewEncoder(output)
	if c.action == "list" {
		page, err := store.ListIntegrationInbox(ctx, c.list)
		if err != nil {
			return err
		}
		result := struct {
			Receipts []inboxReceiptView `json:"receipts"`
			Next     string             `json:"next,omitempty"`
		}{Receipts: make([]inboxReceiptView, 0, len(page.Receipts))}
		for _, record := range page.Receipts {
			view, err := inboxView(record)
			if err != nil {
				return err
			}
			result.Receipts = append(result.Receipts, view)
		}
		if page.HasMore {
			cursor, err := json.Marshal(page.Next)
			if err != nil {
				return err
			}
			result.Next = base64.RawURLEncoding.EncodeToString(cursor)
		}
		return encoder.Encode(result)
	}
	if c.action == "show" {
		record, err := store.GetIntegrationInbox(ctx, c.projectID, c.receiptID)
		if err != nil {
			return err
		}
		view, err := inboxView(record.IntegrationInboxSummary)
		if err != nil {
			return err
		}
		view.Source = record.Source
		if record.Source == integrationstore.IntegrationInboxSourceScheduledLaunch {
			preparation, err := record.ScheduledPreparation()
			if err != nil {
				// Decode errors may echo private preparation values.
				return errors.New("decode scheduled publication state")
			}
			view.Scheduled = &inboxScheduledPublicationView{
				AttemptedAt: preparation.AttemptedAt,
				RootKnown:   preparation.Root != nil,
			}
		}
		var slots map[string]inboxSlotView
		if len(record.Plan) != 0 {
			if err := json.Unmarshal(record.Plan, &slots); err != nil {
				return fmt.Errorf("decode inbox identities: %w", err)
			}
		}
		var progress map[string]map[string]json.RawMessage
		if err := json.Unmarshal(record.Progress, &progress); err != nil {
			return fmt.Errorf("decode inbox progress: %w", err)
		}
		for key, slot := range slots {
			slot.Key = key
			_, slot.Prepared = progress[key]["prepared"]
			_, slot.Committed = progress[key]["committed"]
			view.Slots = append(view.Slots, slot)
		}
		slices.SortFunc(view.Slots, func(a, b inboxSlotView) int { return strings.Compare(a.Key, b.Key) })
		return encoder.Encode(view)
	}
	var err error
	alreadyDiscarded := false
	if c.action == "retry" {
		err = store.RetryFailedIntegrationInbox(ctx, c.projectID, c.receiptID)
	} else {
		record, readErr := store.GetIntegrationInbox(ctx, c.projectID, c.receiptID)
		if readErr != nil {
			return readErr
		}
		alreadyDiscarded = record.State == integrationstore.IntegrationInboxDiscarded
		if !alreadyDiscarded {
			err = store.DiscardFailedIntegrationInbox(ctx, c.projectID, c.receiptID)
		}
	}
	if err != nil {
		return err
	}
	project, err := publicid.Encode(publicid.KindProject, c.projectID)
	if err != nil {
		return err
	}
	return encoder.Encode(struct {
		Action           string    `json:"action"`
		ProjectID        string    `json:"project_id"`
		ReceiptID        uuid.UUID `json:"receipt_id"`
		Reason           string    `json:"reason,omitempty"`
		AlreadyDiscarded bool      `json:"already_discarded,omitempty"`
	}{c.action, project, c.receiptID, c.reason, alreadyDiscarded})
}
