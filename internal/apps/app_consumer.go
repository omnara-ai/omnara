package apps

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
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type AppInboxFile struct {
	Content     []byte
	ContentType string
	Filename    string
}

type AppInboxExpansion struct {
	Events []AppEvent
	Files  map[string]AppInboxFile
}

type AppInboxProvider interface {
	Expand(context.Context, appstore.ProjectAppRecord, []byte) (AppInboxExpansion, error)
	DownloadFile(context.Context, appstore.ProjectAppRecord, []byte, string) (AppInboxFile, error)
}

// ErrAppInboundPermanent marks a provider-confirmed inaccessible inbound target
// before routing is frozen. It does not classify stored app or recipient authority.
var ErrAppInboundPermanent = errors.New("permanent app inbound failure")

// AppInboxRoutingProvider checks a single conversational event before fetching
// identity, context or files. The callback freezes an empty plan for an unrouted
// event; implementations must stop expansion when it returns false or an error.
type AppInboxRoutingProvider interface {
	ExpandRouted(
		context.Context, appstore.ProjectAppRecord, []byte, func(AppEvent) (bool, error),
	) (AppInboxExpansion, error)
}

type AppArtifactUploader interface {
	PreparedArtifactUploaded(context.Context, uuid.UUID, artifactstore.PreparedArtifact) (bool, error)
	UploadPreparedArtifact(context.Context, uuid.UUID, artifactstore.PreparedArtifact, []byte) error
}

type AppCanceledInteractionPresenter interface {
	DismissCanceled(context.Context, uuid.UUID, uuid.UUID, []uuid.UUID) error
}

type AppInboxConsumer struct {
	Log *slog.Logger

	presenter AppCanceledInteractionPresenter
	router    *AppRouter
	inbox     AppRoutingStore
	artifacts AppArtifactUploader
	providers map[string]AppInboxProvider
	launchers *AppLaunchWorkflow
	scheduled map[appdefinition.Type]AppScheduledHandler
}

func NewAppInboxConsumer(
	router *AppRouter,
	inbox AppRoutingStore,
	artifacts AppArtifactUploader,
	providers map[string]AppInboxProvider,
	presenter AppCanceledInteractionPresenter,
	launchers *AppLaunchWorkflow,
	options ...AppInboxConsumerOption,
) *AppInboxConsumer {
	snapshot := make(map[string]AppInboxProvider, len(providers))
	for provider, adapter := range providers {
		snapshot[provider] = adapter
	}
	consumer := &AppInboxConsumer{
		router: router, inbox: inbox, artifacts: artifacts, providers: snapshot, presenter: presenter, launchers: launchers,
	}
	for _, option := range options {
		option(consumer)
	}
	return consumer
}

func (c *AppInboxConsumer) Consume(
	ctx context.Context,
	lease appstore.AppInboxLease,
) ([]AppSlotAdmission, error) {
	log := c.Log
	if log == nil {
		log = slog.Default()
	}
	receipt, err := c.inbox.GetAppInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return nil, err
	}
	var plan AppInboxPlan
	if artifacts, ok := c.artifacts.(AppInboxArtifactCleaner); ok {
		defer func() {
			if !appInboxPlanHasArtifacts(plan) {
				return
			}
			if err := CleanupTerminalAppInboxArtifacts(
				context.WithoutCancel(ctx), c.inbox, artifacts, lease.ProjectID, lease.ReceiptID,
			); err != nil {
				log.WarnContext(ctx, "App inbox artifact cleanup failed",
					"receipt_id", lease.ReceiptID, "app_id", receipt.AppID, "error", err)
			}
		}()
	}
	if receipt.State == appstore.AppInboxCompleted {
		plan, err = decodeAppInboxPlan(receipt.Plan)
		if err != nil {
			return nil, err
		}
		return c.router.Admit(ctx, lease)
	}
	err = c.inbox.WithAppInboxLease(
		ctx,
		lease,
		func(work *appstore.AppInboxLeaseTx) error { receipt = work.Receipt(); return nil },
	)
	if err != nil {
		return nil, err
	}
	appSetup, err := c.inbox.GetProjectAppByID(ctx, receipt.AppID)
	if err != nil {
		return nil, err
	}
	if appSetup.ProjectID != lease.ProjectID ||
		appSetup.State != appstore.ProjectAppStateActive {
		return nil, storeerr.ErrUnauthorized
	}
	if receipt.Source == appstore.AppInboxSourceScheduled {
		return c.consumeScheduled(ctx, lease, receipt, appSetup)
	}
	adapter := c.providers[appSetup.Provider]
	var expansion AppInboxExpansion
	if len(receipt.Plan) == 0 {
		if adapter == nil {
			return nil, fmt.Errorf("no inbox consumer for provider %s", appSetup.Provider)
		}
		if len(receipt.Events) != 0 {
			if err := json.Unmarshal(receipt.Events, &expansion.Events); err != nil {
				return nil, fmt.Errorf("decode decided app events: %w", err)
			}
		} else {
			unrouted := false
			if provider, ok := adapter.(AppInboxRoutingProvider); ok {
				expansion, err = provider.ExpandRouted(ctx, appSetup, receipt.Payload, func(event AppEvent) (bool, error) {
					var err error
					unrouted, err = c.router.freezeEmptyIfUnrouted(ctx, lease, appSetup, event)
					return !unrouted, err
				})
			} else {
				expansion, err = adapter.Expand(ctx, appSetup, receipt.Payload)
			}
			if err != nil {
				return nil, err
			}
			if unrouted {
				return c.router.Admit(ctx, lease)
			}
			if c.launchers == nil {
				return nil, fmt.Errorf("app launcher workflow is required")
			}
			expansion.Events, err = c.launchers.Decide(ctx, lease, receipt, appSetup, expansion.Events)
			if err != nil {
				return nil, err
			}
		}
		if _, err = c.router.Freeze(ctx, lease, expansion.Events); err != nil {
			return nil, err
		}
		receipt, err = c.inbox.GetAppInbox(ctx, lease.ProjectID, lease.ReceiptID)
		if err != nil {
			return nil, err
		}
	}
	plan, err = decodeAppInboxPlan(receipt.Plan)
	if err != nil {
		return nil, err
	}
	var progress map[string]appInboxSlotProgress
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
			appstore.ProjectAppRecord,
			[]byte,
			appdefinition.Scope,
			func(context.Context) error,
		) error
	}); ok {
		type conversation struct {
			scope      appdefinition.Scope
			recipients []string
		}
		conversations := map[appstore.ConversationAddress]conversation{}
		for _, key := range keys {
			if progress[key].Committed != nil {
				continue
			}
			scope := plan[key].Scope
			if err := scope.Validate(appSetup.Provider); err != nil {
				return nil, err
			}
			kind, ref, err := scope.Conversation()
			if err != nil {
				return nil, err
			}
			address := appstore.ConversationAddress{Kind: kind, Ref: ref}
			group := conversations[address]
			group.scope = scope
			group.recipients = append(group.recipients, key)
			conversations[address] = group
		}
		for address, group := range conversations {
			checkRecipients := func(ctx context.Context, settleAll bool) error {
				authorized := false
				var failures []error
				for _, key := range group.recipients {
					if err := c.router.execution.CheckInboxConversationAuthority(ctx, lease, key, address); err == nil {
						if !settleAll {
							return nil
						}
						authorized = true
					} else if !errors.Is(err, executionstore.ErrInboxRecipientSettled) {
						failures = append(failures, err)
					}
				}
				if authorized {
					return nil
				}
				if len(failures) == 0 {
					return executionstore.ErrInboxRecipientSettled
				}
				return errors.Join(failures...)
			}
			// Settle every archived recipient once before per-request authority checks.
			if err := checkRecipients(ctx, true); err != nil {
				if errors.Is(err, executionstore.ErrInboxRecipientSettled) {
					continue
				}
				return nil, err
			}
			authority := func(ctx context.Context) error { return checkRecipients(ctx, false) }
			if err := preparer.PrepareConversation(ctx, appSetup, receipt.Payload, group.scope, authority); err != nil &&
				!errors.Is(err, executionstore.ErrInboxRecipientSettled) {
				return nil, err
			}
		}
		// Authority checks may settle archived recipients while preparing siblings.
		receipt, err = c.inbox.GetAppInbox(ctx, lease.ProjectID, lease.ReceiptID)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(receipt.Progress, &progress); err != nil {
			return nil, err
		}
	}
	cache := expansion.Files
	if cache == nil {
		cache = map[string]AppInboxFile{}
	}
	preparationFailures := make(map[string]error)
	for _, key := range keys {
		slot := plan[key]
		if len(slot.Files) == 0 || len(progress[key].Prepared) != 0 || progress[key].Committed != nil {
			continue
		}
		prepared, err := c.prepareFiles(ctx, adapter, appSetup, receipt.Payload, slot, cache)
		if err == nil {
			err = c.router.Prepare(ctx, lease, key, prepared)
		}
		if err != nil {
			preparationFailures[key] = err
		}
	}
	results, err := c.router.Admit(ctx, lease)
	for _, result := range results {
		if result.Input != nil && result.Input.Skipped == executionstore.InboxInputSkipAgentArchived {
			delete(preparationFailures, result.Slot)
		}
	}
	var failures []error
	for _, key := range keys {
		if prepareErr := preparationFailures[key]; prepareErr != nil {
			failures = append(failures, fmt.Errorf("prepare slot %s: %w", key, prepareErr))
		}
	}
	if err != nil {
		failures = append(failures, err)
	}
	created := slices.ContainsFunc(results, func(result AppSlotAdmission) bool {
		return (result.Launch != nil && result.Launch.Created) || (result.Input != nil && result.Input.Created)
	})
	if acknowledger, ok := adapter.(interface {
		acknowledge(context.Context, appstore.ProjectAppRecord, []byte) error
	}); ok && created {
		if feedbackErr := acknowledger.acknowledge(ctx, appSetup, receipt.Payload); feedbackErr != nil {
			log.WarnContext(
				ctx,
				"App input acknowledgement failed",
				"receipt_id",
				lease.ReceiptID,
				"app_id",
				appSetup.ID,
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
					log.WarnContext(
						ctx,
						"Canceled interaction dismissal failed",
						"receipt_id",
						lease.ReceiptID,
						"app_id",
						appSetup.ID,
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
	appSetup appstore.ProjectAppRecord,
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
				content, err = adapter.DownloadFile(ctx, appSetup, payload, file.ProviderFileID)
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
