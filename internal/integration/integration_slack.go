package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type SlackInboxSecrets interface {
	ReadProjectAvailableSecretPayload(
		context.Context,
		secretstore.ReadProjectAvailableSecretPayloadInput,
	) (secretstore.SecretPayloadRecord, error)
	GetProjectAvailableSecret(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		uuid.UUID,
	) (secretstore.ProjectSecretAccessRecord, error)
}

type SlackInboxIntegrations interface {
	GetProjectIntegration(
		context.Context,
		uuid.UUID,
		uuid.UUID,
	) (integrationstore.ProjectIntegrationRecord, error)
	GetConversationDisplayName(
		context.Context, uuid.UUID, uuid.UUID, integrationstore.ConversationAddress,
	) (string, error)
}

type SlackInboxActors interface {
	ListActorDisplayNames(context.Context, uuid.UUID, string, string, []string) (map[string]string, error)
}

type SlackIntegrationInboxProvider struct {
	config       slack.OAuthConfig
	secrets      SlackInboxSecrets
	integrations SlackInboxIntegrations
	actors       SlackInboxActors
}

func NewSlackIntegrationInboxProvider(
	config slack.OAuthConfig,
	secrets SlackInboxSecrets,
	integrations SlackInboxIntegrations,
	actors SlackInboxActors,
) *SlackIntegrationInboxProvider {
	return &SlackIntegrationInboxProvider{config: config, secrets: secrets, integrations: integrations, actors: actors}
}

func (p *SlackIntegrationInboxProvider) requestAccess(
	ctx context.Context,
	integrationSetup integrationstore.ProjectIntegrationRecord,
) (slack.OAuthConfig, string, func(context.Context) error, error) {
	if p.secrets == nil || p.integrations == nil {
		return slack.OAuthConfig{}, "", nil, fmt.Errorf("slack secret and integration resolvers are required")
	}
	checkIntegrationSetup := func(ctx context.Context) error {
		latest, err := p.integrations.GetProjectIntegration(ctx, integrationSetup.ProjectID, integrationSetup.ID)
		if err != nil {
			return err
		}
		if latest.State != integrationstore.ProjectIntegrationStateActive || latest.Provider != "slack" ||
			latest.ID != integrationSetup.ID || latest.OrgID != integrationSetup.OrgID ||
			latest.ProjectID != integrationSetup.ProjectID ||
			latest.CredentialSecretID != integrationSetup.CredentialSecretID ||
			latest.SetupRevision != integrationSetup.SetupRevision {
			return fmt.Errorf("slack integration setup changed: %w", storeerr.ErrUnauthorized)
		}
		return nil
	}
	if err := checkIntegrationSetup(ctx); err != nil {
		return slack.OAuthConfig{}, "", nil, err
	}
	record, err := p.secrets.ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
		OrgID:     integrationSetup.OrgID,
		ProjectID: integrationSetup.ProjectID,
		SecretID:  integrationSetup.CredentialSecretID,
		Kind:      secrets.KindSlackAppCredentials,
	})
	if err != nil {
		return slack.OAuthConfig{}, "", nil, err
	}
	credentials, err := slack.AppCredentialsFromPayload(record.Payload)
	if err != nil {
		return slack.OAuthConfig{}, "", nil, err
	}
	check := func(ctx context.Context) error {
		access, err := p.secrets.GetProjectAvailableSecret(
			ctx,
			integrationSetup.OrgID,
			integrationSetup.ProjectID,
			integrationSetup.CredentialSecretID,
		)
		if err != nil {
			return err
		}
		if access.Secret.Kind != secrets.KindSlackAppCredentials || record.CurrentVersionID == uuid.Nil ||
			access.Secret.CurrentVersionID != record.CurrentVersionID {
			return fmt.Errorf("slack credential changed: %w", storeerr.ErrUnauthorized)
		}
		return checkIntegrationSetup(ctx)
	}
	if err := check(ctx); err != nil {
		return slack.OAuthConfig{}, "", nil, err
	}
	config := p.config
	config.HTTPClient = slack.WithRequestCheck(config.HTTPClient, check)
	identity, err := slack.ParseInstallIdentity(integrationSetup.ProviderIdentity)
	if err != nil {
		return slack.OAuthConfig{}, "", nil, err
	}
	if err := slack.CheckIdentity(ctx, config, credentials.BotToken, slack.Identity{
		AppID:       integrationSetup.ProviderAccountRef,
		WorkspaceID: integrationSetup.ProviderTenantID,
		BotUserID:   identity.BotUserID,
	}); err != nil {
		return slack.OAuthConfig{}, "", nil, err
	}
	return config, credentials.BotToken, check, nil
}

func NormalizeSlackIntegrationEvent(
	integrationSetup integrationstore.ProjectIntegrationRecord,
	payload []byte,
) (IntegrationEvent, bool, error) {
	envelope, identity, ok, err := slackInboxEnvelope(integrationSetup, payload)
	if err != nil || !ok {
		return IntegrationEvent{}, ok, err
	}
	event := envelope.Event
	mentioned := event.Type == "app_mention" || slackMentionsUser(event.Text, identity.BotUserID)
	scope := integrationdefinition.SlackScope{ChannelID: event.Channel}
	if event.ChannelType == "im" || strings.HasPrefix(event.Channel, "D") {
		mentioned = true
	} else {
		scope.ThreadTS = event.ThreadTS
		if scope.ThreadTS == "" {
			scope.ThreadTS = event.TS
		}
	}
	integrationActor, err := executionstore.IntegrationActorParams(integrationSetup.ID, event.User, nil)
	if err != nil {
		return IntegrationEvent{}, false, err
	}
	result := IntegrationEvent{
		Event: integrationdefinition.Event{
			Scope:     integrationdefinition.Scope{Slack: &scope},
			Kind:      "message",
			Mentioned: mentioned,
		},
		SemanticKey:            "slack:message:" + integrationSetup.ProviderTenantID + ":" + event.Channel + ":" + event.TS,
		Actor:                  integrationActor,
		DeliveryMode:           executionstore.DeliveryModeSteering,
		CancelOpenInteractions: true,
	}
	key, sibling := slack.InputIdempotencyKeyPair(envelope)
	result.SemanticKey = key
	result.Sibling = &executionstore.InboxMessageSibling{Key: sibling}
	if len(event.Files) > 0 {
		result.Sibling.AttachmentNotice = "Files for the previous Slack message."
	}
	if err := result.Event.Validate(); err != nil {
		return IntegrationEvent{}, false, err
	}
	result.ContentBlocks, err = json.Marshal([]map[string]any{{"type": "text", "text": strings.TrimSpace(event.Text)}})
	return result, true, err
}

func slackInboxEnvelope(
	integrationSetup integrationstore.ProjectIntegrationRecord,
	payload []byte,
) (slack.EventsEnvelope, slack.InstallIdentity, bool, error) {
	if integrationSetup.Provider != "slack" {
		return slack.EventsEnvelope{}, slack.InstallIdentity{}, false, storeerr.ErrUnauthorized
	}
	envelope, err := slack.DecodeEventsEnvelope(payload)
	if err != nil {
		return envelope, slack.InstallIdentity{}, false, err
	}
	identity, err := slack.ParseInstallIdentity(integrationSetup.ProviderIdentity)
	if err != nil {
		return envelope, identity, false, err
	}
	if !slack.ValidateEnvelopeIdentity(
		slack.Identity{
			AppID:       integrationSetup.ProviderAccountRef,
			WorkspaceID: integrationSetup.ProviderTenantID,
			BotUserID:   identity.BotUserID,
		},
		envelope,
	) {
		return envelope, identity, false, storeerr.ErrUnauthorized
	}
	if !slack.EventCallbackEnvelope(envelope) {
		return envelope, identity, false, nil
	}
	if !slack.ValidateRuntimeBotAuthorization(
		slack.Identity{
			AppID:       integrationSetup.ProviderAccountRef,
			WorkspaceID: integrationSetup.ProviderTenantID,
			BotUserID:   identity.BotUserID,
		},
		envelope,
	) {
		return envelope, identity, false, storeerr.ErrUnauthorized
	}
	event := envelope.Event
	if !slack.ConversationalMessage(event) {
		return envelope, identity, false, nil
	}
	if slack.RemoteUserEvent(integrationSetup.ProviderTenantID, event) || slack.BotOrSelfEvent(identity.BotUserID, event) {
		return envelope, identity, false, nil
	}
	if event.TS == "" || event.Channel == "" || envelope.EventID == "" {
		return envelope, identity, false, fmt.Errorf("slack message lacks a durable identity")
	}
	return envelope, identity, true, nil
}

func slackMentionsUser(text, user string) bool {
	for {
		_, remaining, ok := strings.Cut(text, "<@")
		if !ok {
			return false
		}
		token, rest, ok := strings.Cut(remaining, ">")
		if !ok {
			return false
		}
		id, _, _ := strings.Cut(token, "|")
		if id == user {
			return true
		}
		text = rest
	}
}

func (p *SlackIntegrationInboxProvider) Expand(
	ctx context.Context,
	integrationSetup integrationstore.ProjectIntegrationRecord,
	payload []byte,
) (IntegrationInboxExpansion, error) {
	return p.ExpandRouted(ctx, integrationSetup, payload, nil)
}

func (p *SlackIntegrationInboxProvider) ExpandRouted(
	ctx context.Context, integrationSetup integrationstore.ProjectIntegrationRecord, payload []byte,
	routeEvent func(IntegrationEvent) (bool, error),
) (IntegrationInboxExpansion, error) {
	normalized, ok, err := NormalizeSlackIntegrationEvent(integrationSetup, payload)
	if err != nil || !ok {
		return IntegrationInboxExpansion{}, err
	}
	if routeEvent != nil {
		if routed, err := routeEvent(normalized); err != nil || !routed {
			return IntegrationInboxExpansion{}, err
		}
	}
	envelope, identity, _, err := slackInboxEnvelope(integrationSetup, payload)
	if err != nil {
		return IntegrationInboxExpansion{}, err
	}
	config, token, check, err := p.requestAccess(ctx, integrationSetup)
	if err != nil {
		return IntegrationInboxExpansion{}, err
	}
	event := envelope.Event
	kind, ref, _ := normalized.Event.Scope.Conversation()
	route := slack.InboundRoute{ProviderRefKind: kind, ProviderRef: ref, AppendOnly: !normalized.Event.Mentioned}
	enrichCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	history, status, _ := slack.FetchRecentContextMessages(
		enrichCtx,
		config,
		token,
		event,
		route,
		normalized.Event.Mentioned,
	)
	var stored map[string]string
	if p.actors != nil {
		stored, _ = p.actors.ListActorDisplayNames(
			enrichCtx,
			integrationSetup.ProjectID,
			normalized.Actor.Provider,
			normalized.Actor.ProviderTenantID,
			slack.ReferencedUserIDs(event, history),
		)
	}
	channelName, _ := p.integrations.GetConversationDisplayName(
		enrichCtx,
		integrationSetup.ProjectID,
		integrationSetup.ID,
		integrationstore.ConversationAddress{Kind: kind, Ref: ref},
	)
	labels, _, _ := slack.ResolveDisplayLabels(
		enrichCtx,
		config,
		token,
		slack.DisplayLabelInput{ChannelDisplayName: channelName,
			Event:                  event,
			HistoryMessages:        history,
			BotUserID:              identity.BotUserID,
			BotDisplayName:         integrationSetup.ProviderAgentDisplayName,
			StoredUserDisplayNames: stored,
		},
	)
	if err := check(ctx); err != nil {
		return IntegrationInboxExpansion{}, err
	}
	normalized.DisplayName = labels.Channels[event.Channel]
	if name := labels.StoredDisplayName(event.User); name != "" {
		normalized.Actor.DisplayName = &name
	}
	text := labels.RenderText(strings.TrimSpace(event.Text))
	display := labels.RenderDisplayText(strings.TrimSpace(event.Text))
	var historyText string
	if status == "fetched" {
		historyText = slack.FormatRecentContext(history, event, labels)
		if historyText == "" {
			status = "empty"
		}
	}
	_, contextText := slack.ModelInputTextParts(event, route, normalized.Event.Mentioned, historyText, labels)
	blocks := []map[string]any{
		{"type": "text", "text": contextText, "metadata": map[string]string{"omnara_hidden": "true"}},
	}
	if text != "" {
		block := map[string]any{"type": "text", "text": text}
		if text != display && utf8.RuneCountInString(display) <= 512 {
			block["metadata"] = map[string]string{"omnara_display_text": display}
		}
		blocks = append(blocks, block)
	}
	downloads, err := slack.DownloadEventFiles(ctx, config, token, event.Files, slackInboxFileOptions())
	if err != nil {
		return IntegrationInboxExpansion{}, err
	}
	if err := check(ctx); err != nil {
		return IntegrationInboxExpansion{}, err
	}
	files := map[string]IntegrationInboxFile{}
	for i := range downloads {
		file := &downloads[i]
		if len(file.Content) == 0 || file.ContentType == "" {
			continue
		}
		if file.FileID == "" {
			return IntegrationInboxExpansion{}, fmt.Errorf("downloaded Slack attachment has no stable file ID")
		}
		id, err := uuid.NewV7()
		if err != nil {
			return IntegrationInboxExpansion{}, err
		}
		expected := artifactstore.PreparedArtifact{
			ID:          id,
			ContentType: file.ContentType,
			Filename:    file.Filename,
			Digest:      blobstore.ContentDigest(file.Content),
			SizeBytes:   int64(len(file.Content)),
		}
		normalized.Files = append(
			normalized.Files,
			IntegrationPlannedFile{ArtifactID: id, ProviderFileID: file.FileID, Expected: &expected},
		)
		files[file.FileID] = IntegrationInboxFile{
			Content:     file.Content,
			ContentType: file.ContentType,
			Filename:    file.Filename,
		}
		blocks = append(blocks, map[string]any{"type": "media_ref", "artifact_id": id.String()})
		file.Status = slack.EventFileStatusStored
	}
	if summary := slack.SkippedFileSummary(downloads); summary != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": "\n" + summary})
	}
	if len(event.Files) > 0 {
		neutral := event
		neutral.Text = "Files for the previous Slack message."
		notice, hidden := slack.ModelInputTextParts(
			neutral,
			slack.InboundRoute{ProviderRefKind: kind, ProviderRef: ref, AppendOnly: true},
			false,
			"",
			labels,
		)
		normalized.Sibling.AttachmentNotice = hidden + notice
		if summary := slack.SkippedFileSummary(downloads); summary != "" {
			normalized.Sibling.AttachmentNotice += "\n" + summary
		}
	}
	normalized.Metadata, err = slack.InboundEventMetadata("slack", envelope, route, status, downloads)
	if err != nil {
		return IntegrationInboxExpansion{}, err
	}
	normalized.ContentBlocks, err = json.Marshal(blocks)
	if err != nil {
		return IntegrationInboxExpansion{}, err
	}
	return IntegrationInboxExpansion{Events: []IntegrationEvent{normalized}, Files: files}, nil
}

func slackInboxFileOptions() slack.FileDownloadOptions {
	return slack.FileDownloadOptions{
		MaxFiles:         20,
		MaxFileBytes:     10 * 1024 * 1024,
		MaxTotalBytes:    int(modelcontext.MaxResolvedMediaBytes),
		MaxFilenameBytes: 255,
		AcceptMediaType:  modelcontext.IsAttachmentMedia,
		DefaultFilename:  modelcontext.MediaFilename,
	}
}

func (p *SlackIntegrationInboxProvider) DownloadFile(
	ctx context.Context,
	integrationSetup integrationstore.ProjectIntegrationRecord,
	payload []byte,
	fileID string,
) (IntegrationInboxFile, error) {
	envelope, _, ok, err := slackInboxEnvelope(integrationSetup, payload)
	if err != nil {
		return IntegrationInboxFile{}, err
	}
	if !ok {
		return IntegrationInboxFile{}, fmt.Errorf("receipt is not a Slack message")
	}
	var selected *slack.File
	for _, file := range envelope.Event.Files {
		if file.ID == fileID {
			snapshot := file
			selected = &snapshot
			break
		}
	}
	if selected == nil {
		return IntegrationInboxFile{}, fmt.Errorf("file is not in the captured Slack message")
	}
	config, token, check, err := p.requestAccess(ctx, integrationSetup)
	if err != nil {
		return IntegrationInboxFile{}, err
	}
	downloads, err := slack.DownloadEventFiles(ctx, config, token, []slack.File{*selected}, slackInboxFileOptions())
	if err != nil {
		return IntegrationInboxFile{}, err
	}
	if err := check(ctx); err != nil {
		return IntegrationInboxFile{}, err
	}
	if len(downloads) != 1 || len(downloads[0].Content) == 0 {
		return IntegrationInboxFile{}, fmt.Errorf("frozen Slack attachment is unavailable")
	}
	file := downloads[0]
	return IntegrationInboxFile{Content: file.Content, ContentType: file.ContentType, Filename: file.Filename}, nil
}

func (p *SlackIntegrationInboxProvider) acknowledge(
	ctx context.Context,
	integrationSetup integrationstore.ProjectIntegrationRecord,
	payload []byte,
) error {
	envelope, _, ok, err := slackInboxEnvelope(integrationSetup, payload)
	if err != nil || !ok {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	config, token, _, err := p.requestAccess(ctx, integrationSetup)
	if err != nil {
		return err
	}
	result, err := slack.AddReaction(
		ctx,
		config,
		token,
		envelope.Event.Channel,
		envelope.Event.TS,
		slack.InboundReaction,
	)
	return slackFeedbackError(result, err)
}
