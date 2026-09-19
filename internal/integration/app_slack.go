package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/integration/slack"
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

type SlackInboxConnections interface {
	GetIntegrationConnection(
		context.Context,
		uuid.UUID,
		uuid.UUID,
	) (integrationstore.IntegrationConnectionRecord, error)
	GetConversationDisplayName(
		context.Context, uuid.UUID, uuid.UUID, integrationstore.ConversationAddress,
	) (string, error)
}

type SlackInboxActors interface {
	ListActorDisplayNames(context.Context, uuid.UUID, string, string, []string) (map[string]string, error)
}

type SlackAppInboxProvider struct {
	config      slack.OAuthConfig
	secrets     SlackInboxSecrets
	connections SlackInboxConnections
	actors      SlackInboxActors
}

func NewSlackAppInboxProvider(
	config slack.OAuthConfig,
	secrets SlackInboxSecrets,
	connections SlackInboxConnections,
	actors SlackInboxActors,
) *SlackAppInboxProvider {
	return &SlackAppInboxProvider{config: config, secrets: secrets, connections: connections, actors: actors}
}

// requestAccess resolves project-owned or granted credentials without holding a
// transaction during key unwrapping or provider I/O. The captured version and
// connection revision fence every subsequent request, including file hydration.
func (p *SlackAppInboxProvider) requestAccess(
	ctx context.Context,
	connection integrationstore.IntegrationConnectionRecord,
) (slack.OAuthConfig, string, func(context.Context) error, error) {
	if p.secrets == nil || p.connections == nil {
		return slack.OAuthConfig{}, "", nil, fmt.Errorf("slack secret and connection resolvers are required")
	}
	checkConnection := func(ctx context.Context) error {
		latest, err := p.connections.GetIntegrationConnection(ctx, connection.ProjectID, connection.ID)
		if err != nil {
			return err
		}
		if latest.State != integrationstore.IntegrationConnectionStateActive || latest.Provider != "slack" ||
			latest.ID != connection.ID || latest.OrgID != connection.OrgID || latest.ProjectID != connection.ProjectID ||
			latest.CredentialSecretID != connection.CredentialSecretID || !latest.UpdatedAt.Equal(connection.UpdatedAt) {
			return fmt.Errorf("slack connection changed: %w", storeerr.ErrUnauthorized)
		}
		return nil
	}
	if err := checkConnection(ctx); err != nil {
		return slack.OAuthConfig{}, "", nil, err
	}
	record, err := p.secrets.ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
		OrgID:     connection.OrgID,
		ProjectID: connection.ProjectID,
		SecretID:  connection.CredentialSecretID,
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
			connection.OrgID,
			connection.ProjectID,
			connection.CredentialSecretID,
		)
		if err != nil {
			return err
		}
		if access.Secret.Kind != secrets.KindSlackAppCredentials || record.CurrentVersionID == uuid.Nil ||
			access.Secret.CurrentVersionID != record.CurrentVersionID {
			return fmt.Errorf("slack credential changed: %w", storeerr.ErrUnauthorized)
		}
		return checkConnection(ctx)
	}
	if err := check(ctx); err != nil {
		return slack.OAuthConfig{}, "", nil, err
	}
	config := p.config
	config.HTTPClient = slack.WithRequestCheck(config.HTTPClient, check)
	return config, credentials.BotToken, check, nil
}

// NormalizeSlackAppEvent is pure. HTTP verifies signatures before capture; this
// boundary rechecks receipt/account identity and excludes bot/remote mutations.
// Root human messages are listener events, but only mentions trigger a launcher.
// Both message and app_mention callbacks use the same semantic message identity.
func NormalizeSlackAppEvent(
	connection integrationstore.IntegrationConnectionRecord,
	payload []byte,
) (AppEvent, bool, error) {
	envelope, identity, ok, err := slackInboxEnvelope(connection, payload)
	if err != nil || !ok {
		return AppEvent{}, ok, err
	}
	event := envelope.Event
	mentioned := event.Type == "app_mention" || slackMentionsUser(event.Text, identity.BotUserID)
	scope := appdefinition.SlackScope{ChannelID: event.Channel}
	if event.ChannelType == "im" || strings.HasPrefix(event.Channel, "D") {
		// A direct message explicitly addresses the bot, preserving Slack's
		// existing per-DM conversation behavior without requiring mention syntax.
		mentioned = true
	} else {
		scope.ThreadTS = event.ThreadTS
		if scope.ThreadTS == "" {
			scope.ThreadTS = event.TS
		}
	}
	result := AppEvent{
		Event: appdefinition.Event{
			Scope:     appdefinition.Scope{Slack: &scope},
			Kind:      "message",
			Mentioned: mentioned,
		},
		SemanticKey: "slack:message:" + connection.ProviderTenantID + ":" + event.Channel + ":" + event.TS,
		Actor: executionstore.ActorParams{
			Provider:         "slack",
			ProviderTenantID: connection.ProviderTenantID,
			ProviderUserID:   event.User,
		},
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
		return AppEvent{}, false, err
	}
	result.ContentBlocks, err = json.Marshal([]map[string]any{{"type": "text", "text": strings.TrimSpace(event.Text)}})
	return result, true, err
}

func slackInboxEnvelope(
	connection integrationstore.IntegrationConnectionRecord,
	payload []byte,
) (slack.EventsEnvelope, slack.InstallIdentity, bool, error) {
	if connection.Provider != "slack" {
		return slack.EventsEnvelope{}, slack.InstallIdentity{}, false, storeerr.ErrUnauthorized
	}
	envelope, err := slack.DecodeEventsEnvelope(payload)
	if err != nil {
		return envelope, slack.InstallIdentity{}, false, err
	}
	identity, err := slack.ParseInstallIdentity(connection.ProviderIdentity)
	if err != nil {
		return envelope, identity, false, err
	}
	if !slack.ValidateEnvelopeIdentity(
		slack.Identity{
			AppID:       connection.ProviderAccountRef,
			WorkspaceID: connection.ProviderTenantID,
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
			AppID:       connection.ProviderAccountRef,
			WorkspaceID: connection.ProviderTenantID,
			BotUserID:   identity.BotUserID,
		},
		envelope,
	) {
		return envelope, identity, false, storeerr.ErrUnauthorized
	}
	event := envelope.Event
	if event.Type != "message" && event.Type != "app_mention" {
		return envelope, identity, false, nil
	}
	if event.Subtype != "" && event.Subtype != "file_share" {
		return envelope, identity, false, nil
	}
	if slack.RemoteUserEvent(connection.ProviderTenantID, event) || slack.BotOrSelfEvent(identity.BotUserID, event) {
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

func (p *SlackAppInboxProvider) Expand(
	ctx context.Context,
	connection integrationstore.IntegrationConnectionRecord,
	payload []byte,
) (AppInboxExpansion, error) {
	normalized, ok, err := NormalizeSlackAppEvent(connection, payload)
	if err != nil || !ok {
		return AppInboxExpansion{}, err
	}
	envelope, identity, _, err := slackInboxEnvelope(connection, payload)
	if err != nil {
		return AppInboxExpansion{}, err
	}
	config, token, check, err := p.requestAccess(ctx, connection)
	if err != nil {
		return AppInboxExpansion{}, err
	}
	event := envelope.Event
	kind, ref, _ := normalized.Event.Scope.Conversation()
	route := slack.InboundRoute{ProviderRefKind: kind, ProviderRef: ref, AppendOnly: !normalized.Event.Mentioned}
	// Labels/history are best effort presentation. File failures retain the
	// existing bounded skip/retry classification and precede plan publication.
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
			connection.ProjectID,
			"slack",
			connection.ProviderTenantID,
			slack.ReferencedUserIDs(event, history),
		)
	}
	channelName, _ := p.connections.GetConversationDisplayName(
		enrichCtx,
		connection.ProjectID,
		connection.ID,
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
			BotDisplayName:         connection.ProviderAgentDisplayName,
			StoredUserDisplayNames: stored,
		},
	)
	// Best-effort presentation failures must not hide revoked authority.
	if err := check(ctx); err != nil {
		return AppInboxExpansion{}, err
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
		return AppInboxExpansion{}, err
	}
	if err := check(ctx); err != nil {
		return AppInboxExpansion{}, err
	}
	files := map[string]AppInboxFile{}
	for i := range downloads {
		file := &downloads[i]
		if len(file.Content) == 0 || file.ContentType == "" {
			continue
		}
		if file.FileID == "" {
			return AppInboxExpansion{}, fmt.Errorf("downloaded Slack attachment has no stable file ID")
		}
		id, err := uuid.NewV7()
		if err != nil {
			return AppInboxExpansion{}, err
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
			AppPlannedFile{ArtifactID: id, ProviderFileID: file.FileID, Expected: &expected},
		)
		files[file.FileID] = AppInboxFile{Content: file.Content, ContentType: file.ContentType, Filename: file.Filename}
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
		return AppInboxExpansion{}, err
	}
	normalized.ContentBlocks, err = json.Marshal(blocks)
	if err != nil {
		return AppInboxExpansion{}, err
	}
	return AppInboxExpansion{Events: []AppEvent{normalized}, Files: files}, nil
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

func (p *SlackAppInboxProvider) DownloadFile(
	ctx context.Context,
	connection integrationstore.IntegrationConnectionRecord,
	payload []byte,
	fileID string,
) (AppInboxFile, error) {
	envelope, _, ok, err := slackInboxEnvelope(connection, payload)
	if err != nil {
		return AppInboxFile{}, err
	}
	if !ok {
		return AppInboxFile{}, fmt.Errorf("receipt is not a Slack message")
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
		return AppInboxFile{}, fmt.Errorf("file is not in the captured Slack message")
	}
	config, token, check, err := p.requestAccess(ctx, connection)
	if err != nil {
		return AppInboxFile{}, err
	}
	downloads, err := slack.DownloadEventFiles(ctx, config, token, []slack.File{*selected}, slackInboxFileOptions())
	if err != nil {
		return AppInboxFile{}, err
	}
	if err := check(ctx); err != nil {
		return AppInboxFile{}, err
	}
	if len(downloads) != 1 || len(downloads[0].Content) == 0 {
		return AppInboxFile{}, fmt.Errorf("frozen Slack attachment is unavailable")
	}
	file := downloads[0]
	return AppInboxFile{Content: file.Content, ContentType: file.ContentType, Filename: file.Filename}, nil
}

// Acknowledgement follows committed admission. Slack's already_reacted result
// is idempotent, and an unavailable presentation never retries accepted input.
func (p *SlackAppInboxProvider) acknowledge(
	ctx context.Context,
	connection integrationstore.IntegrationConnectionRecord,
	payload []byte,
	results []AppSlotAdmission,
) error {
	created := false
	for _, result := range results {
		created = created || (result.Launch != nil && result.Launch.Created) ||
			(result.Input != nil && result.Input.Created)
	}
	if !created {
		return nil
	}
	envelope, _, ok, err := slackInboxEnvelope(connection, payload)
	if err != nil || !ok {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	config, token, _, err := p.requestAccess(ctx, connection)
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
