package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type DiscordInboxSecrets interface {
	ReadProjectAvailableSecretPayload(context.Context, secretstore.ReadProjectAvailableSecretPayloadInput) (
		secretstore.SecretPayloadRecord, error,
	)
	GetProjectAvailableSecret(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (
		secretstore.ProjectSecretAccessRecord, error,
	)
}

type DiscordInboxApps interface {
	GetProjectApp(context.Context, uuid.UUID, uuid.UUID) (integrationstore.ProjectAppRecord, error)
}

type DiscordAppInboxProvider struct {
	config  discord.Config
	secrets DiscordInboxSecrets
	apps    DiscordInboxApps
}

// NewDiscordAppInboxProvider uses config only for trusted HTTP settings and an
// optional additional BeforeRequest check. Credentials always come from the
// project's live secret reference. Runtime/checkpoint ownership stays elsewhere.
func NewDiscordAppInboxProvider(
	config discord.Config, secrets DiscordInboxSecrets, apps DiscordInboxApps,
) *DiscordAppInboxProvider {
	config.Credentials = discord.Credentials{}
	return &DiscordAppInboxProvider{config: config, secrets: secrets, apps: apps}
}

type DiscordEventMetadata struct {
	GuildID         string             `json:"guild_id"`
	SourceChannelID string             `json:"source_channel_id"`
	ChannelID       string             `json:"channel_id"`
	ThreadID        string             `json:"thread_id"`
	MessageID       string             `json:"message_id"`
	Timestamp       string             `json:"timestamp,omitempty"`
	ThreadStarter   bool               `json:"thread_starter,omitempty"`
	Files           []DiscordEventFile `json:"files,omitempty"`
}

type DiscordEventFile struct {
	ID     string `json:"id,omitempty"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Count  int    `json:"count,omitempty"`
}

// NormalizeDiscordAppEvent accepts only a trusted, durably captured Dispatch and
// current REST channel metadata. Guild identity must be present in the dispatch
// and agree with the lookup; it is never inferred from saved app application ID.
// Root messages require a native bot mention. Replies identify exactly one
// thread; the router must require an exact thread subscription for ordinary replies.
func NormalizeDiscordAppEvent(
	appSetup integrationstore.ProjectAppRecord, raw []byte, channel discord.Channel,
) (AppEvent, bool, error) {
	message, ok, err := discordInboxMessage(appSetup, raw)
	if err != nil || !ok {
		return AppEvent{}, ok, err
	}
	scope, err := discordInboxMessageScope(message.Message, channel)
	if err != nil {
		return AppEvent{}, false, err
	}
	starter := scope.ThreadID == ""
	if starter {
		if !message.MentionsBot {
			return AppEvent{}, false, nil
		}
		// Discord documents that a thread created from a message has that
		// message's ID. This pins the future scope without a premature POST.
		scope.ThreadID = message.Message.ID
	}
	name := message.Message.Author.GlobalName
	if name == "" {
		name = message.Message.Author.Username
	}
	if name == "" {
		name = message.Message.Author.ID
	}
	appActor, err := executionstore.AppActorParams(appSetup.ID, message.Message.Author.ID, &name)
	if err != nil {
		return AppEvent{}, false, err
	}
	result := AppEvent{
		Event: appdefinition.Event{Kind: "message", Mentioned: message.MentionsBot,
			Scope: appdefinition.Scope{Discord: &appdefinition.DiscordScope{
				GuildID: scope.GuildID, ChannelID: scope.ChannelID, ThreadID: scope.ThreadID,
			}}},
		SemanticKey:  "discord:message:" + appSetup.ProviderTenantID + ":" + message.Message.ID,
		DisplayName:  channel.Name,
		Actor:        appActor,
		DeliveryMode: executionstore.DeliveryModeSteering, CancelOpenInteractions: true,
	}
	if result.DisplayName == "" {
		result.DisplayName = channel.ID
	}
	if err := result.Event.Validate(); err != nil {
		return AppEvent{}, false, err
	}
	result.ContentBlocks, err = json.Marshal([]map[string]string{{"type": "text", "text": message.Message.Content}})
	if err != nil {
		return AppEvent{}, false, err
	}
	result.Metadata, err = json.Marshal(DiscordEventMetadata{
		GuildID: scope.GuildID, SourceChannelID: message.Message.ChannelID,
		ChannelID: scope.ChannelID, ThreadID: scope.ThreadID, MessageID: message.Message.ID,
		Timestamp: message.Message.Timestamp, ThreadStarter: starter,
	})
	return result, err == nil, err
}

func discordInboxMessage(
	appSetup integrationstore.ProjectAppRecord, raw []byte,
) (discord.MessageEvent, bool, error) {
	if appSetup.Provider != "discord" || appSetup.State != integrationstore.ProjectAppStateActive ||
		!discordInboxID(appSetup.ProviderTenantID) || !discordInboxID(appSetup.ProviderAccountRef) {
		return discord.MessageEvent{}, false, storeerr.ErrUnauthorized
	}
	var dispatch discord.Dispatch
	if len(raw) > integrationstore.IntegrationInboxMaxPayloadBytes || !utf8.Valid(raw) ||
		!strings.HasPrefix(strings.TrimSpace(string(raw)), "{") || json.Unmarshal(raw, &dispatch) != nil ||
		dispatch.Type == "" || dispatch.Sequence < 0 {
		return discord.MessageEvent{}, false, fmt.Errorf("invalid discord inbox dispatch")
	}
	message, ok, err := discord.NormalizeMessage(dispatch, appSetup.ProviderAccountRef)
	if err != nil || !ok {
		return message, ok, err
	}
	if message.Self || message.Automated || (message.Message.Type != 0 && message.Message.Type != 19) ||
		message.Message.GuildID == "" {
		// This transport subscribes to guild messages, not DMs or ephemeral
		// system messages. Missing guild facts never become a guild-wide route.
		return discord.MessageEvent{}, false, nil
	}
	return message, true, nil
}

func discordInboxID(value string) bool {
	id, err := strconv.ParseUint(value, 10, 64)
	return err == nil && id != 0 && strconv.FormatUint(id, 10) == value
}

func discordInboxMessageScope(message discord.Message, channel discord.Channel) (discord.Scope, error) {
	if channel.ID != message.ChannelID || channel.GuildID != message.GuildID || !discordInboxID(channel.GuildID) {
		return discord.Scope{}, &discord.APIError{Code: discord.ScopeMismatch}
	}
	scope := discord.Scope{GuildID: message.GuildID, ChannelID: message.ChannelID}
	if channel.IsThread() {
		if !discordInboxID(channel.ParentID) || channel.ParentID == channel.ID {
			return discord.Scope{}, &discord.APIError{Code: discord.ScopeMismatch}
		}
		scope.ChannelID, scope.ThreadID = channel.ParentID, channel.ID
	} else if channel.Type != 0 && channel.Type != 5 {
		return discord.Scope{}, &discord.APIError{Code: discord.ScopeMismatch}
	}
	return scope, nil
}

func (p *DiscordAppInboxProvider) requestAccess(
	ctx context.Context, appSetup integrationstore.ProjectAppRecord, authority func(context.Context) error,
) (*discord.Client, func(context.Context) error, error) {
	if p.secrets == nil || p.apps == nil {
		return nil, nil, fmt.Errorf("discord secret and app resolvers are required")
	}
	checkAppSetup := func(ctx context.Context) error {
		latest, err := p.apps.GetProjectApp(ctx, appSetup.ProjectID, appSetup.ID)
		if err != nil {
			return err
		}
		if latest.State != integrationstore.ProjectAppStateActive || latest.Provider != "discord" ||
			latest.ID != appSetup.ID || latest.OrgID != appSetup.OrgID || latest.ProjectID != appSetup.ProjectID ||
			latest.ProviderTenantID != appSetup.ProviderTenantID ||
			latest.ProviderAccountRef != appSetup.ProviderAccountRef ||
			latest.CredentialSecretID != appSetup.CredentialSecretID || latest.SetupRevision != appSetup.SetupRevision {
			return storeerr.ErrUnauthorized
		}
		return nil
	}
	if err := checkAppSetup(ctx); err != nil {
		return nil, nil, err
	}
	credential, err := p.secrets.ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
		OrgID: appSetup.OrgID, ProjectID: appSetup.ProjectID, SecretID: appSetup.CredentialSecretID,
		Kind: secrets.KindGeneric,
	})
	if err != nil {
		return nil, nil, err
	}
	check := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		access, err := p.secrets.GetProjectAvailableSecret(
			ctx, appSetup.OrgID, appSetup.ProjectID, appSetup.CredentialSecretID,
		)
		if err != nil {
			return err
		}
		if access.Secret.Kind != secrets.KindGeneric || credential.CurrentVersionID == uuid.Nil ||
			access.Secret.CurrentVersionID != credential.CurrentVersionID {
			return storeerr.ErrUnauthorized
		}
		if err := checkAppSetup(ctx); err != nil {
			return err
		}
		if p.config.BeforeRequest != nil {
			if err := p.config.BeforeRequest(ctx); err != nil {
				return err
			}
		}
		if authority != nil {
			return authority(ctx)
		}
		return nil
	}
	if err := check(ctx); err != nil {
		return nil, nil, err
	}
	config := p.config
	config.Credentials = discord.Credentials{ApplicationID: appSetup.ProviderTenantID,
		BotUserID: appSetup.ProviderAccountRef, BotToken: credential.Payload[secrets.KeyValue]}
	config.BeforeRequest = check
	client, err := discord.NewClient(config)
	if err != nil {
		return nil, nil, err
	}
	// Generic secret rotation can replace the token without editing app
	// identity. Prove this token still belongs to both immutable identities.
	if err := client.CheckIdentity(ctx); err != nil {
		return nil, nil, err
	}
	return client, check, nil
}

// Expand is read-only at Discord. Attachments are pinned to bounded bytes/digests
// and ephemeral UUID placeholders. The existing consumer freezes per-recipient
// UUIDs, uploads via artifactstore and atomically admits artifact metadata/input.
func (p *DiscordAppInboxProvider) Expand(
	ctx context.Context, appSetup integrationstore.ProjectAppRecord, raw []byte,
) (AppInboxExpansion, error) {
	ctx, cancel := context.WithTimeout(ctx, discord.OperationTimeout)
	defer cancel()
	message, ok, err := discordInboxMessage(appSetup, raw)
	if err != nil || !ok {
		return AppInboxExpansion{}, err
	}
	client, check, err := p.requestAccess(ctx, appSetup, nil)
	if err != nil {
		return AppInboxExpansion{}, err
	}
	channel, err := client.GetChannel(ctx, message.Message.ChannelID)
	if err != nil {
		return AppInboxExpansion{}, err
	}
	event, ok, err := NormalizeDiscordAppEvent(appSetup, raw, channel)
	if err != nil || !ok {
		return AppInboxExpansion{}, err
	}
	scope, err := discordInboxMessageScope(message.Message, channel)
	if err != nil {
		return AppInboxExpansion{}, err
	}
	blocks := []map[string]string{}
	if message.Message.Content != "" {
		blocks = append(blocks, map[string]string{"type": "text", "text": message.Message.Content})
	}
	files := map[string]AppInboxFile{}
	var results []DiscordEventFile
	var total int64
	seen := map[string]bool{}
	for _, attachment := range message.Message.Attachments[:min(len(message.Message.Attachments), discord.MaxFiles)] {
		if !discordInboxID(attachment.ID) || seen[attachment.ID] {
			return AppInboxExpansion{}, fmt.Errorf("invalid or duplicate discord attachment identity")
		}
		seen[attachment.ID] = true
		result := DiscordEventFile{ID: attachment.ID, Status: "skipped"}
		switch {
		case attachment.Size <= 0:
			result.Reason = "empty_or_invalid_size"
		case attachment.Size > discord.MaxFileBytes || attachment.Size > modelcontext.MaxResolvedMediaBytes-total:
			result.Reason = "too_large"
		default:
			file, err := discordInboxDownload(ctx, client, scope, message.Message.ID, attachment)
			if err != nil {
				return AppInboxExpansion{}, err
			}
			total += int64(len(file.Content))
			if file.ContentType == "" {
				result.Reason = "unsupported_media_type"
				break
			}
			id, err := uuid.NewV7()
			if err != nil {
				return AppInboxExpansion{}, err
			}
			expected := artifactstore.PreparedArtifact{ID: id, Filename: file.Filename, ContentType: file.ContentType,
				Digest: blobstore.ContentDigest(file.Content), SizeBytes: int64(len(file.Content))}
			if err := expected.Validate(); err != nil {
				return AppInboxExpansion{}, err
			}
			event.Files = append(event.Files, AppPlannedFile{ArtifactID: id, ProviderFileID: attachment.ID, Expected: &expected})
			files[attachment.ID] = file
			blocks = append(blocks, map[string]string{"type": "media_ref", "artifact_id": id.String()})
			result.Status = "prepared"
		}
		results = append(results, result)
	}
	if len(message.Message.Attachments) > discord.MaxFiles {
		results = append(results, DiscordEventFile{Status: "skipped", Reason: "too_many_attachments",
			Count: len(message.Message.Attachments) - discord.MaxFiles})
	}
	for _, file := range results {
		if file.Status == "skipped" {
			blocks = append(blocks, map[string]string{"type": "text", "text": "Discord attachment omitted: " + file.Reason})
		}
	}
	if len(blocks) == 0 {
		return AppInboxExpansion{}, fmt.Errorf("discord message has no supported content; check message-content intent")
	}
	var metadata DiscordEventMetadata
	if err := json.Unmarshal(event.Metadata, &metadata); err != nil {
		return AppInboxExpansion{}, err
	}
	metadata.Files = results
	event.Metadata, err = json.Marshal(metadata)
	if err != nil {
		return AppInboxExpansion{}, err
	}
	event.ContentBlocks, err = json.Marshal(blocks)
	if err != nil {
		return AppInboxExpansion{}, err
	}
	if err := check(ctx); err != nil {
		return AppInboxExpansion{}, err
	}
	return AppInboxExpansion{Events: []AppEvent{event}, Files: files}, nil
}

func discordInboxDownload(
	ctx context.Context, client *discord.Client, scope discord.Scope, messageID string, captured discord.Attachment,
) (AppInboxFile, error) {
	download, err := client.DownloadAttachment(ctx, scope, messageID, captured.ID)
	if err != nil {
		return AppInboxFile{}, err
	}
	if download.Attachment.ID != captured.ID || download.Attachment.Size != captured.Size ||
		download.Attachment.Filename != captured.Filename {
		return AppInboxFile{}, storeerr.ErrIdempotencyConflict
	}
	contentType, _, _ := mime.ParseMediaType(download.Attachment.ContentType)
	if !modelcontext.IsAttachmentMedia(contentType) {
		contentType, _, _ = mime.ParseMediaType(http.DetectContentType(download.Content))
		if !modelcontext.IsAttachmentMedia(contentType) {
			contentType = ""
		}
	}
	return AppInboxFile{Content: download.Content, ContentType: contentType, Filename: download.Attachment.Filename}, nil
}

// DownloadFile rehydrates only a captured attachment ID, never an event URL.
// The consumer compares the returned bytes/metadata with the frozen digest.
func (p *DiscordAppInboxProvider) DownloadFile(
	ctx context.Context, appSetup integrationstore.ProjectAppRecord, raw []byte, fileID string,
) (AppInboxFile, error) {
	ctx, cancel := context.WithTimeout(ctx, discord.OperationTimeout)
	defer cancel()
	message, ok, err := discordInboxMessage(appSetup, raw)
	if err != nil {
		return AppInboxFile{}, err
	}
	if !ok || !discordInboxID(fileID) {
		return AppInboxFile{}, fmt.Errorf("receipt is not a discord message with a valid attachment")
	}
	var captured *discord.Attachment
	for _, file := range message.Message.Attachments[:min(len(message.Message.Attachments), discord.MaxFiles)] {
		if file.ID == fileID {
			if captured != nil || file.Size <= 0 || file.Size > discord.MaxFileBytes {
				return AppInboxFile{}, fmt.Errorf("invalid captured discord attachment")
			}
			captured = &file
		}
	}
	if captured == nil {
		return AppInboxFile{}, fmt.Errorf("attachment is not in the captured discord message")
	}
	client, check, err := p.requestAccess(ctx, appSetup, nil)
	if err != nil {
		return AppInboxFile{}, err
	}
	channel, err := client.GetChannel(ctx, message.Message.ChannelID)
	if err != nil {
		return AppInboxFile{}, err
	}
	scope, err := discordInboxMessageScope(message.Message, channel)
	if err != nil {
		return AppInboxFile{}, err
	}
	file, err := discordInboxDownload(ctx, client, scope, message.Message.ID, *captured)
	if err != nil {
		return AppInboxFile{}, err
	}
	if file.ContentType == "" {
		return AppInboxFile{}, fmt.Errorf("frozen discord attachment is no longer supported")
	}
	if err := check(ctx); err != nil {
		return AppInboxFile{}, err
	}
	return file, nil
}

// PrepareConversation runs only after the consumer freezes a nonempty plan and
// before admission. authority must recheck that receipt's lease and a current
// accepting recipient/launcher for frozenScope before EVERY HTTP attempt. The
// provider independently fences app/credential revisions. Never pass an
// unconditional success callback from a worker; an empty plan must not call this.
// Replays reuse Discord's one thread per source message. No thread is created
// during Expand, and existing thread replies cause no provider mutation here.
func (p *DiscordAppInboxProvider) PrepareConversation(
	ctx context.Context, appSetup integrationstore.ProjectAppRecord, raw []byte,
	frozenScope appdefinition.DiscordScope, authority func(context.Context) error,
) error {
	ctx, cancel := context.WithTimeout(ctx, discord.OperationTimeout)
	defer cancel()
	message, ok, err := discordInboxMessage(appSetup, raw)
	if err != nil {
		return err
	}
	if !ok || authority == nil {
		return fmt.Errorf("discord conversation preparation requires a message and live plan authority")
	}
	client, check, err := p.requestAccess(ctx, appSetup, authority)
	if err != nil {
		return err
	}
	channel, err := client.GetChannel(ctx, message.Message.ChannelID)
	if err != nil {
		return err
	}
	event, ok, err := NormalizeDiscordAppEvent(appSetup, raw, channel)
	if err != nil {
		return err
	}
	if !ok || *event.Event.Scope.Discord != frozenScope {
		return storeerr.ErrUnauthorized
	}
	if !channel.IsThread() {
		_, err = client.EnsureThread(ctx, discord.Scope{GuildID: frozenScope.GuildID, ChannelID: frozenScope.ChannelID},
			message.Message.ID, discordConversationName(appSetup))
		if err != nil {
			return err
		}
	}
	return check(ctx)
}

func discordConversationName(appSetup integrationstore.ProjectAppRecord) string {
	name := strings.TrimSpace(appSetup.ProviderAgentDisplayName)
	if name == "" {
		name = "Omnara"
	}
	runes := []rune(name + " conversation")
	return string(runes[:min(len(runes), 100)])
}
