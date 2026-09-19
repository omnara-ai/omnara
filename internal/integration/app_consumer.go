package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type AppInboxFile struct {
	Content     []byte
	ContentType string
	Filename    string
}

type AppInboxExpansion struct {
	Events []AppEvent
	// Files are ephemeral downloaded bytes, never persisted in the inbox plan.
	Files map[string]AppInboxFile
}

// AppInboxProvider owns verified provider normalization and bounded file reads.
// It cannot mutate agents, plans or subscriptions. Credentials never enter output.
type AppInboxProvider interface {
	Expand(context.Context, integrationstore.IntegrationConnectionRecord, []byte) (AppInboxExpansion, error)
	DownloadFile(context.Context, integrationstore.IntegrationConnectionRecord, []byte, string) (AppInboxFile, error)
}

type AppArtifactUploader interface {
	PreparedArtifactUploaded(context.Context, uuid.UUID, artifactstore.PreparedArtifact) (bool, error)
	UploadPreparedArtifact(context.Context, uuid.UUID, artifactstore.PreparedArtifact, []byte) error
}

type AppCanceledInteractionPresenter interface {
	DismissCanceled(context.Context, uuid.UUID, uuid.UUID, []uuid.UUID) error
}

type AppInboxConsumer struct {
	presenter AppCanceledInteractionPresenter
	router    *AppRouter
	inbox     AppRoutingStore
	artifacts AppArtifactUploader
	providers map[string]AppInboxProvider
	launchers *AppLaunchWorkflow
}

// NewAppInboxConsumer requires an explicit presenter choice. Production callers
// supply InteractionPresenter; tests without interaction mirrors may pass nil.
func NewAppInboxConsumer(
	router *AppRouter,
	inbox AppRoutingStore,
	artifacts AppArtifactUploader,
	providers map[string]AppInboxProvider,
	presenter AppCanceledInteractionPresenter,
	launchers *AppLaunchWorkflow,
) *AppInboxConsumer {
	snapshot := make(map[string]AppInboxProvider, len(providers))
	for provider, adapter := range providers {
		snapshot[provider] = adapter
	}
	return &AppInboxConsumer{
		router: router, inbox: inbox, artifacts: artifacts, providers: snapshot, presenter: presenter, launchers: launchers,
	}
}

// Consume processes one already-claimed receipt. The worker owns claiming,
// bounded retry/failure scheduling and machine provisioning from returned launch
// results. No provider or blob I/O runs inside a transaction. Frozen work skips
// expansion, so config edits and provider enrichment cannot rewrite a retry.
func (c *AppInboxConsumer) Consume(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
) ([]AppSlotAdmission, error) {
	receipt, err := c.inbox.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return nil, err
	}
	if receipt.State == integrationstore.IntegrationInboxCompleted {
		return c.router.Admit(ctx, lease)
	}
	// Fence before any external work; Freeze/Prepare/admission fence again after
	// external work, where a slow download may have outlived the lease.
	err = c.inbox.WithIntegrationInboxLease(
		ctx,
		lease,
		func(work *integrationstore.IntegrationInboxLeaseTx) error { receipt = work.Receipt(); return nil },
	)
	if err != nil {
		return nil, err
	}
	connection, err := c.inbox.GetIntegrationConnectionByID(ctx, receipt.ConnectionID)
	if err != nil {
		return nil, err
	}
	if connection.ProjectID != lease.ProjectID ||
		connection.State != integrationstore.IntegrationConnectionStateActive {
		return nil, storeerr.ErrUnauthorized
	}
	adapter := c.providers[connection.Provider]
	var expansion AppInboxExpansion
	if len(receipt.Plan) == 0 {
		if adapter == nil {
			return nil, fmt.Errorf("no inbox consumer for provider %s", connection.Provider)
		}
		if _, ok := adapter.(*SlackAppInboxProvider); ok && len(receipt.Events) == 0 {
			event, eligible, err := NormalizeSlackAppEvent(connection, receipt.Payload)
			if err != nil {
				return nil, err
			}
			if eligible {
				empty, err := c.router.freezeEmptyIfUnrouted(ctx, lease, connection, event)
				if err != nil {
					return nil, err
				}
				if empty {
					return c.router.Admit(ctx, lease)
				}
			}
		}
		if len(receipt.Events) != 0 {
			// Only the trusted app handoff writes Events. The provider payload is
			// retained separately for attachment reads and thread preparation.
			if err := json.Unmarshal(receipt.Events, &expansion.Events); err != nil {
				return nil, fmt.Errorf("decode decided app events: %w", err)
			}
		} else {
			expansion, err = adapter.Expand(ctx, connection, receipt.Payload)
			if err != nil {
				return nil, err
			}
			if c.launchers == nil {
				return nil, fmt.Errorf("app launcher workflow is required")
			}
			expansion.Events, err = c.launchers.Decide(ctx, lease, receipt, connection, expansion.Events)
			if err != nil {
				return nil, err
			}
		}
		if _, err = c.router.Freeze(ctx, lease, expansion.Events); err != nil {
			if len(receipt.Events) != 0 && errors.Is(err, ErrAppLaunchUnavailable) &&
				c.launchers != nil && c.launchers.OnUnavailable != nil {
				err = errors.Join(err, c.launchers.OnUnavailable(ctx, connection, expansion.Events))
			}
			return nil, err
		}
		// Re-read durable preparation and the actual winning plan, never retain
		// speculative identities from a competing worker's expansion.
		receipt, err = c.inbox.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
		if err != nil {
			return nil, err
		}
	}
	plan, err := decodeAppInboxPlan(receipt.Plan)
	if err != nil {
		return nil, err
	}
	var progress map[string]struct {
		Prepared  json.RawMessage `json:"prepared"`
		Committed json.RawMessage `json:"committed"`
	}
	if err = json.Unmarshal(receipt.Progress, &progress); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(plan))
	for key := range plan {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if preparer, ok := adapter.(interface {
		PrepareConversation(
			context.Context,
			integrationstore.IntegrationConnectionRecord,
			[]byte,
			appdefinition.DiscordScope,
			func(context.Context) error,
		) error
	}); ok {
		scopes := map[appdefinition.DiscordScope][]string{}
		for _, key := range keys {
			if len(progress[key].Committed) != 0 {
				continue
			}
			scope := plan[key].Scope
			if scope.Discord == nil {
				return nil, fmt.Errorf("discord plan lacks frozen conversation scope")
			}
			if err := scope.Validate(appdefinition.ProviderDiscord); err != nil {
				return nil, err
			}
			scopes[*scope.Discord] = append(scopes[*scope.Discord], key)
		}
		for scope, recipients := range scopes {
			kind, ref, err := (appdefinition.Scope{Discord: &scope}).Conversation()
			if err != nil {
				return nil, err
			}
			address := integrationstore.ConversationAddress{Kind: kind, Ref: ref}
			authority := func(ctx context.Context) error {
				var failures []error
				for _, key := range recipients {
					if err := c.router.execution.CheckInboxConversationAuthority(ctx, lease, key, address); err == nil {
						return nil
					} else {
						failures = append(failures, err)
					}
				}
				return errors.Join(failures...)
			}
			if err := preparer.PrepareConversation(ctx, connection, receipt.Payload, scope, authority); err != nil {
				return nil, err
			}
		}
	}
	cache := expansion.Files
	if cache == nil {
		cache = map[string]AppInboxFile{}
	}
	var failures []error
	for _, key := range keys {
		slot := plan[key]
		if len(slot.Files) == 0 || len(progress[key].Prepared) != 0 || len(progress[key].Committed) != 0 {
			continue
		}
		prepared, err := c.prepareFiles(ctx, adapter, connection, receipt.Payload, slot, cache)
		if err == nil {
			err = c.router.Prepare(ctx, lease, key, prepared)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("prepare slot %s: %w", key, err))
		}
	}
	results, err := c.router.Admit(ctx, lease)
	if err != nil {
		failures = append(failures, err)
	}
	if slackProvider, ok := adapter.(*SlackAppInboxProvider); ok {
		c.notifySlackLaunchFailure(ctx, lease, connection, receipt.Payload, slackProvider, err)
		if feedbackErr := slackProvider.acknowledge(ctx, connection, receipt.Payload, results); feedbackErr != nil {
			slog.WarnContext(
				ctx,
				"Slack input acknowledgement failed",
				"receipt_id",
				lease.ReceiptID,
				"connection_id",
				connection.ID,
				"error",
				feedbackErr,
			)
		}
	}
	if c.presenter != nil {
		for _, result := range results {
			if result.Input != nil && len(result.Input.CanceledInteractionIDs) > 0 {
				if dismissErr := c.presenter.DismissCanceled(
					ctx,
					lease.ProjectID,
					plan[result.Slot].AgentID,
					result.Input.CanceledInteractionIDs,
				); dismissErr != nil {
					slog.WarnContext(
						ctx,
						"Canceled interaction dismissal failed",
						"receipt_id",
						lease.ReceiptID,
						"connection_id",
						connection.ID,
						"error",
						dismissErr,
					)
				}
			}
		}
	}
	return results, errors.Join(failures...)
}

func (c *AppInboxConsumer) prepareFiles(
	ctx context.Context,
	adapter AppInboxProvider,
	connection integrationstore.IntegrationConnectionRecord,
	payload []byte,
	slot AppInboxSlot,
	cache map[string]AppInboxFile,
) ([]artifactstore.PreparedArtifact, error) {
	if c.artifacts == nil {
		return nil, fmt.Errorf("artifact uploader is required")
	}
	prepared := make([]artifactstore.PreparedArtifact, 0, len(slot.Files))
	for _, file := range slot.Files {
		if file.Expected == nil || file.Expected.ID != file.ArtifactID {
			return nil, fmt.Errorf("file content was not pinned in the frozen plan")
		}
		expected := *file.Expected
		present, err := c.artifacts.PreparedArtifactUploaded(ctx, slot.AgentID, expected)
		if err != nil {
			return nil, err
		}
		if !present {
			content, found := cache[file.ProviderFileID]
			if !found {
				if adapter == nil {
					return nil, fmt.Errorf("file download provider is unavailable")
				}
				content, err = adapter.DownloadFile(ctx, connection, payload, file.ProviderFileID)
				if err != nil {
					return nil, err
				}
				cache[file.ProviderFileID] = content
			}
			if content.ContentType != expected.ContentType || content.Filename != expected.Filename ||
				int64(len(content.Content)) != expected.SizeBytes ||
				blobstore.ContentDigest(content.Content) != expected.Digest {
				return nil, storeerr.ErrIdempotencyConflict
			}
			if err = c.artifacts.UploadPreparedArtifact(ctx, slot.AgentID, expected, content.Content); err != nil {
				return nil, err
			}
		}
		prepared = append(prepared, expected)
	}
	return prepared, nil
}
