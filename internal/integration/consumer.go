package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type IntegrationInboxFile struct {
	Content     []byte
	ContentType string
	Filename    string
}

type IntegrationInboxExpansion struct {
	Event *IntegrationEvent
	Files map[string]IntegrationInboxFile
}

type IntegrationInboxProvider interface {
	Expand(context.Context, integrationstore.IntegrationRecord, []byte) (IntegrationInboxExpansion, error)
	DownloadFile(context.Context, integrationstore.IntegrationRecord, []byte, string) (IntegrationInboxFile, error)
}

// ErrIntegrationInboundPermanent must not classify temporary credential or recipient-authority failures.
var ErrIntegrationInboundPermanent = errors.New("permanent integration inbound failure")

// IntegrationInboxRoutingProvider checks a single conversational event before fetching
// identity, context or files. The callback freezes an empty plan for an unrouted
// event; implementations must stop expansion when it returns false or an error.
type IntegrationInboxRoutingProvider interface {
	ExpandRouted(
		context.Context, integrationstore.IntegrationRecord, []byte, func(IntegrationEvent) (bool, error),
	) (IntegrationInboxExpansion, error)
}

type IntegrationArtifactUploader interface {
	PreparedArtifactUploaded(context.Context, uuid.UUID, artifactstore.PreparedArtifact) (bool, error)
	UploadPreparedArtifact(context.Context, uuid.UUID, artifactstore.PreparedArtifact, []byte) error
}

type IntegrationCanceledInteractionPresenter interface {
	DismissCanceled(context.Context, uuid.UUID, uuid.UUID, []uuid.UUID) error
}

type IntegrationInboxConsumer struct {
	Log           *slog.Logger
	presenter     IntegrationCanceledInteractionPresenter
	router        *IntegrationRouter
	inbox         IntegrationRoutingStore
	artifacts     IntegrationArtifactUploader
	providers     map[integrationdefinition.Provider]IntegrationInboxProvider
	launchers     *IntegrationLaunchWorkflow
	scheduled     map[integrationdefinition.Kind]IntegrationScheduledHandler
	stateHandlers map[integrationdefinition.Kind]IntegrationStateHandler
}

func NewIntegrationInboxConsumer(
	router *IntegrationRouter,
	inbox IntegrationRoutingStore,
	artifacts IntegrationArtifactUploader,
	providers map[integrationdefinition.Provider]IntegrationInboxProvider,
	presenter IntegrationCanceledInteractionPresenter,
	launchers *IntegrationLaunchWorkflow,
	options ...IntegrationInboxConsumerOption,
) *IntegrationInboxConsumer {
	snapshot := make(map[integrationdefinition.Provider]IntegrationInboxProvider, len(providers))
	for provider, adapter := range providers {
		snapshot[provider] = adapter
	}
	consumer := &IntegrationInboxConsumer{
		router: router, inbox: inbox, artifacts: artifacts, providers: snapshot, presenter: presenter, launchers: launchers,
	}
	for _, option := range options {
		option(consumer)
	}
	return consumer
}

func (c *IntegrationInboxConsumer) Consume(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
) ([]IntegrationRecipientAdmission, error) {
	receipt, err := c.inbox.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return nil, err
	}
	if receipt.State == integrationstore.IntegrationInboxCompleted {
		return c.consumeEvent(ctx, lease, receipt, integrationstore.IntegrationRecord{}, nil)
	}
	err = c.inbox.WithIntegrationInboxLease(
		ctx,
		lease,
		func(work *integrationstore.IntegrationInboxLeaseTx) error { receipt = work.Receipt(); return nil },
	)
	if err != nil {
		return nil, err
	}
	integrationSetup, err := c.inbox.GetIntegrationByID(ctx, receipt.IntegrationID)
	if err != nil {
		return nil, err
	}
	if integrationSetup.ProjectID != lease.ProjectID ||
		integrationSetup.State != integrationstore.IntegrationStateActive {
		return nil, storeerr.ErrUnauthorized
	}
	if receipt.Source == integrationstore.IntegrationInboxSourceScheduled {
		return c.consumeScheduled(ctx, lease, receipt, integrationSetup)
	}
	if receipt.Source == integrationstore.IntegrationInboxSourceState {
		handler := c.stateHandlers[integrationSetup.IntegrationKind]
		if handler == nil {
			return nil, fmt.Errorf("%w: no state handler for integration %s",
				ErrIntegrationInboundPermanent, integrationSetup.IntegrationKind)
		}
		return handler(ctx, receipt, integrationSetup, func(
			event *IntegrationEvent, payload []byte,
		) ([]IntegrationRecipientAdmission, error) {
			receipt.Payload = payload
			return c.consumeEvent(ctx, lease, receipt, integrationSetup, &IntegrationInboxExpansion{Event: event})
		})
	}
	return c.consumeEvent(ctx, lease, receipt, integrationSetup, nil)
}

func (c *IntegrationInboxConsumer) consumeEvent(ctx context.Context, lease integrationstore.IntegrationInboxLease,
	receipt integrationstore.IntegrationInboxRecord, integrationSetup integrationstore.IntegrationRecord,
	expanded *IntegrationInboxExpansion) ([]IntegrationRecipientAdmission, error) {
	log := c.Log
	if log == nil {
		log = slog.Default()
	}
	var err error
	var plan IntegrationInboxPlan
	if artifacts, ok := c.artifacts.(IntegrationInboxArtifactCleaner); ok {
		defer func() {
			if !integrationInboxPlanHasArtifacts(plan) {
				return
			}
			if err := CleanupTerminalIntegrationInboxArtifacts(
				context.WithoutCancel(ctx), c.inbox, artifacts, lease.ProjectID, lease.ReceiptID,
			); err != nil {
				log.WarnContext(ctx, "Integration inbox artifact cleanup failed",
					"receipt_id", lease.ReceiptID, "integration_id", receipt.IntegrationID, "error", err)
			}
		}()
	}
	if receipt.State == integrationstore.IntegrationInboxCompleted {
		plan, err = decodeIntegrationInboxPlan(receipt.Plan)
		if err != nil {
			return nil, err
		}
		return c.router.Admit(ctx, lease, nil)
	}
	adapter := c.providers[integrationSetup.Provider]
	var expansion IntegrationInboxExpansion
	if expanded != nil {
		expansion = *expanded
	}
	if len(receipt.Plan) == 0 {
		var feedback *integrationLaunchFeedback
		if adapter == nil && expanded == nil {
			return nil, fmt.Errorf("no inbox consumer for provider %s", integrationSetup.Provider)
		}
		if expanded == nil {
			unrouted := false
			var unroutedEvent IntegrationEvent
			if provider, ok := adapter.(IntegrationInboxRoutingProvider); ok {
				expansion, err = provider.ExpandRouted(
					ctx,
					integrationSetup,
					receipt.Payload,
					func(event IntegrationEvent) (bool, error) {
						var err error
						unrouted, err = c.router.freezeEmptyIfUnrouted(ctx, lease, integrationSetup, event)
						unroutedEvent = event
						return !unrouted, err
					},
				)
			} else {
				expansion, err = adapter.Expand(ctx, integrationSetup, receipt.Payload)
			}
			if err != nil {
				return nil, err
			}
			if unrouted {
				if c.launchers != nil && unroutedEvent.Event.Mentioned &&
					integrationSetup.Provider != integrationdefinition.ProviderGitHub {
					shared, err := c.inbox.SharesProviderIdentity(ctx, integrationSetup)
					if err != nil {
						log.WarnContext(ctx, "check shared integration bot", "integration_id", integrationSetup.ID, "error", err)
					} else if !shared {
						c.launchers.launchUnavailable(ctx, IntegrationLaunchContext{
							Receipt: receipt, Integration: integrationSetup, Event: unroutedEvent,
						}, errIntegrationMentionUnrouted)
					}
				}
				return c.router.Admit(ctx, lease, nil)
			}
			if c.launchers == nil {
				return nil, fmt.Errorf("integration launcher workflow is required")
			}
			if expansion.Event != nil {
				expansion.Event, feedback, err = c.launchers.Decide(ctx, lease, receipt, integrationSetup, *expansion.Event)
				if err != nil {
					return nil, err
				}
			}
		}
		var expected *integrationstore.IntegrationRoutingCandidates
		if feedback != nil {
			expected = &feedback.input.Candidates
		}
		_, created, err := c.router.Freeze(ctx, lease, expansion.Event, expected)
		if err != nil {
			return nil, err
		}
		if created && feedback != nil {
			c.launchers.launchUnavailable(ctx, feedback.input, feedback.cause)
		}
		resolvedPayload := receipt.Payload
		receipt, err = c.inbox.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
		if err != nil {
			return nil, err
		}
		receipt.Payload = resolvedPayload
	}
	plan, err = decodeIntegrationInboxPlan(receipt.Plan)
	if err != nil {
		return nil, err
	}
	outcomes, err := c.router.execution.GetIntegrationInboxOutcomes(ctx, receipt)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(plan.Recipients))
	for key := range plan.Recipients {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if preparer, ok := adapter.(interface {
		PrepareConversation(
			context.Context,
			integrationstore.IntegrationRecord,
			[]byte,
			integrationdefinition.Scope,
			func(context.Context) error,
		) error
	}); ok {
		if len(keys) != 0 {
			scope := plan.Message.Scope
			if err := scope.Validate(integrationSetup.Provider); err != nil {
				return nil, err
			}
			kind, ref, err := scope.Conversation()
			if err != nil {
				return nil, err
			}
			address := integrationstore.ConversationAddress{Kind: kind, Ref: ref}
			checkRecipients := func(ctx context.Context) error {
				var failures []error
				for _, key := range keys {
					if outcomes[key] != executionstore.InboxRecipientPending {
						continue
					}
					if err := c.router.execution.CheckInboxConversationAuthority(ctx, lease, key, address); err == nil {
						return nil
					} else if !errors.Is(err, executionstore.ErrInboxRecipientSettled) {
						failures = append(failures, err)
					}
				}
				if len(failures) == 0 {
					return executionstore.ErrInboxRecipientSettled
				}
				return errors.Join(failures...)
			}
			if err := checkRecipients(ctx); err != nil {
				if !errors.Is(err, executionstore.ErrInboxRecipientSettled) {
					return nil, err
				}
			} else {
				err := preparer.PrepareConversation(ctx, integrationSetup, receipt.Payload, scope, checkRecipients)
				if err != nil && !errors.Is(err, executionstore.ErrInboxRecipientSettled) {
					return nil, err
				}
			}
		}
	}

	cache := expansion.Files
	if cache == nil {
		cache = map[string]IntegrationInboxFile{}
	}
	prepared := make(map[string][]artifactstore.PreparedArtifact)
	preparationFailures := make(map[string]error)
	for _, key := range keys {
		recipient := plan.Recipients[key]
		if len(recipient.ArtifactIDs) == 0 || outcomes[key] != executionstore.InboxRecipientPending {
			continue
		}
		files, err := c.prepareFiles(ctx, adapter, integrationSetup, receipt.Payload, *plan.Message, recipient, cache)
		if err == nil {
			prepared[key] = files
		}
		if err != nil {
			preparationFailures[key] = err
		}
	}
	results, err := c.router.Admit(ctx, lease, prepared)
	for _, result := range results {
		if result.Input != nil && result.Input.Skipped == executionstore.InboxInputSkipAgentArchived {
			delete(preparationFailures, result.Recipient)
		}
	}
	var failures []error
	for _, key := range keys {
		if prepareErr := preparationFailures[key]; prepareErr != nil {
			failures = append(failures, fmt.Errorf("prepare recipient %s: %w", key, prepareErr))
		}
	}
	if err != nil {
		failures = append(failures, err)
	}
	created := slices.ContainsFunc(results, func(result IntegrationRecipientAdmission) bool {
		return (result.Launch != nil && result.Launch.Created) || (result.Input != nil && result.Input.Created)
	})
	if acknowledger, ok := adapter.(interface {
		acknowledge(context.Context, integrationstore.IntegrationRecord, []byte) error
	}); ok && created {
		if feedbackErr := acknowledger.acknowledge(ctx, integrationSetup, receipt.Payload); feedbackErr != nil {
			log.WarnContext(
				ctx,
				"Integration input acknowledgement failed",
				"receipt_id",
				lease.ReceiptID,
				"integration_id",
				integrationSetup.ID,
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
					plan.Recipients[result.Recipient].AgentID,
					result.Input.CanceledInteractionIDs,
				); dismissErr != nil {
					log.WarnContext(
						ctx,
						"Canceled interaction dismissal failed",
						"receipt_id",
						lease.ReceiptID,
						"integration_id",
						integrationSetup.ID,
						"error",
						dismissErr,
					)
				}
			}
		}
	}
	return results, errors.Join(failures...)
}

func (c *IntegrationInboxConsumer) prepareFiles(
	ctx context.Context,
	adapter IntegrationInboxProvider,
	integrationSetup integrationstore.IntegrationRecord,
	payload []byte,
	message executionstore.InboxMessage,
	recipient IntegrationInboxRecipient,
	cache map[string]IntegrationInboxFile,
) ([]artifactstore.PreparedArtifact, error) {
	if c.artifacts == nil {
		return nil, fmt.Errorf("artifact uploader is required")
	}
	prepared := make([]artifactstore.PreparedArtifact, 0, len(recipient.ArtifactIDs))
	_, files, err := message.RecipientContent(recipient.ArtifactIDs)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if file.Expected == nil || file.Expected.ID != file.ArtifactID {
			return nil, fmt.Errorf("file content was not pinned in the frozen plan")
		}
		expected := *file.Expected
		present, err := c.artifacts.PreparedArtifactUploaded(ctx, recipient.AgentID, expected)
		if err != nil {
			return nil, err
		}
		if !present {
			content, found := cache[file.ProviderFileID]
			if !found {
				if adapter == nil {
					return nil, fmt.Errorf("file download provider is unavailable")
				}
				content, err = adapter.DownloadFile(ctx, integrationSetup, payload, file.ProviderFileID)
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
			if err = c.artifacts.UploadPreparedArtifact(ctx, recipient.AgentID, expected, content.Content); err != nil {
				return nil, err
			}
		}
		prepared = append(prepared, expected)
	}
	return prepared, nil
}
