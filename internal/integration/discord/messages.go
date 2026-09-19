package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"unicode/utf8"
)

func (c *Client) GetChannel(ctx context.Context, id string) (Channel, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	return c.channel(ctx, id)
}

func (c *Client) channel(ctx context.Context, id string) (Channel, error) {
	if !validID(id) {
		return Channel{}, errors.New("invalid discord channel ID")
	}
	var channel Channel
	if err := c.json(ctx, http.MethodGet, "/channels/"+id, nil, &channel, false); err != nil {
		return Channel{}, err
	}
	if channel.ID != id || (channel.GuildID != "" && !validID(channel.GuildID)) {
		return Channel{}, invalidResponse(false)
	}
	return channel, nil
}

// GetScopedChannel resolves a destination after validating its parent and guild.
// Callers use the returned provider facts when guild_id was omitted in config.
func (c *Client) GetScopedChannel(ctx context.Context, scope Scope) (Channel, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	return c.prepare(ctx, scope)
}

func (c *Client) prepare(ctx context.Context, scope Scope) (Channel, error) {
	if !scope.valid() {
		return Channel{}, errors.New("invalid discord scope")
	}
	channel, err := c.channel(ctx, scope.destination())
	if err != nil {
		return Channel{}, err
	}
	if scope.GuildID != "" && channel.GuildID != scope.GuildID {
		return Channel{}, &APIError{Code: ScopeMismatch}
	}
	if scope.ThreadID != "" {
		if !channel.IsThread() || channel.ParentID != scope.ChannelID || !validID(channel.GuildID) {
			return Channel{}, &APIError{Code: ScopeMismatch}
		}
	} else if channel.IsThread() || (channel.Type != 0 && channel.Type != 1 && channel.Type != 5) {
		return Channel{}, &APIError{Code: ScopeMismatch}
	}
	if (channel.Type == 1) != (channel.GuildID == "") {
		return Channel{}, invalidResponse(false)
	}
	return channel, nil
}

func (c *Client) GetMessage(ctx context.Context, scope Scope, messageID string) (Message, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if !validID(messageID) {
		return Message{}, errors.New("invalid discord message ID")
	}
	channel, err := c.prepare(ctx, scope)
	if err != nil {
		return Message{}, err
	}
	return c.message(ctx, channel, messageID)
}

func (c *Client) message(ctx context.Context, channel Channel, id string) (Message, error) {
	var message Message
	if err := c.json(ctx, http.MethodGet, "/channels/"+channel.ID+"/messages/"+id,
		nil, &message, false); err != nil {
		return Message{}, err
	}
	if !messageInChannel(message, channel) || message.ID != id {
		return Message{}, invalidResponse(false)
	}
	return message, nil
}

func messageInChannel(message Message, channel Channel) bool {
	return validID(message.ID) && message.ChannelID == channel.ID && validID(message.Author.ID) &&
		(message.GuildID == "" || message.GuildID == channel.GuildID)
}

type PageOptions struct {
	Before string
	Limit  int
}

type MessagePage struct {
	Messages []Message `json:"messages"`
	// NextBefore is only a bounded cursor, never a provider URL. A full page may
	// require one more read to discover the end of history.
	NextBefore string `json:"next_before,omitempty"`
}

func (c *Client) ListMessages(ctx context.Context, scope Scope, opts PageOptions) (MessagePage, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if opts.Limit == 0 {
		opts.Limit = 50
	}
	if opts.Limit < 1 || opts.Limit > 100 || (opts.Before != "" && !validID(opts.Before)) {
		return MessagePage{}, errors.New("invalid discord page")
	}
	channel, err := c.prepare(ctx, scope)
	if err != nil {
		return MessagePage{}, err
	}
	query := url.Values{"limit": {strconv.Itoa(opts.Limit)}}
	if opts.Before != "" {
		query.Set("before", opts.Before)
	}
	var page MessagePage
	if err := c.json(ctx, http.MethodGet, "/channels/"+channel.ID+"/messages?"+query.Encode(),
		nil, &page.Messages, false); err != nil {
		return MessagePage{}, err
	}
	if page.Messages == nil || len(page.Messages) > opts.Limit {
		return MessagePage{}, invalidResponse(false)
	}
	var previous uint64
	if opts.Before != "" {
		previous, _ = strconv.ParseUint(opts.Before, 10, 64)
	}
	for _, message := range page.Messages {
		id, _ := strconv.ParseUint(message.ID, 10, 64)
		if !messageInChannel(message, channel) || (previous != 0 && id >= previous) {
			return MessagePage{}, invalidResponse(false)
		}
		previous = id
	}
	if len(page.Messages) == opts.Limit {
		page.NextBefore = page.Messages[len(page.Messages)-1].ID
	}
	return page, nil
}

type MessageArgs struct {
	Content string
	// Nonce must be stable for this logical send and unique across this bot's
	// destinations. Persist it with the caller's operation, not a retry timestamp.
	Nonce      string
	Files      []Upload
	Components []ActionRow
}

type messagePayload struct {
	Content         string           `json:"content"`
	Nonce           string           `json:"nonce,omitempty"`
	EnforceNonce    bool             `json:"enforce_nonce,omitempty"`
	AllowedMentions allowedMentions  `json:"allowed_mentions"`
	Components      []ActionRow      `json:"components"`
	Attachments     []uploadMetadata `json:"attachments,omitempty"`
}

type allowedMentions struct {
	Parse []string `json:"parse"`
}

func (c *Client) CreateMessage(ctx context.Context, scope Scope, args MessageArgs) (Message, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if args.Nonce == "" || len(args.Nonce) > 25 || !asciiToken(args.Nonce) ||
		!utf8.ValidString(args.Content) || utf8.RuneCountInString(args.Content) > 2000 ||
		(args.Content == "" && len(args.Files) == 0 && len(args.Components) == 0) {
		return Message{}, errors.New("invalid discord message or nonce")
	}
	if err := validateRows(args.Components, false); err != nil {
		return Message{}, err
	}
	if args.Components == nil {
		args.Components = []ActionRow{}
	}
	payload := messagePayload{Content: args.Content, Nonce: args.Nonce, EnforceNonce: true,
		AllowedMentions: allowedMentions{Parse: []string{}}, Components: args.Components}
	body, contentType, err := encodeMessage(payload, args.Files)
	if err != nil {
		return Message{}, err
	}
	channel, err := c.prepare(ctx, scope)
	if err != nil {
		return Message{}, err
	}
	data, err := c.do(ctx, http.MethodPost, c.base+"/channels/"+channel.ID+"/messages",
		contentType, body, true, true, ResponseMaxBytes)
	if err != nil {
		return Message{}, err
	}
	var message Message
	if json.Unmarshal(data, &message) != nil || !messageInChannel(message, channel) ||
		message.Author.ID != c.credentials.BotUserID {
		return Message{}, invalidResponse(true)
	}
	// Discord echoes a nonce for newly created and nonce-deduplicated messages.
	var nonce string
	if json.Unmarshal(message.Nonce, &nonce) != nil || nonce != args.Nonce {
		return Message{}, invalidResponse(true)
	}
	return message, nil
}

// EditMessage updates a confirmed bot presentation by ID. It does not scan
// history or automatically retry an uncertain edit.
func (c *Client) EditMessage(ctx context.Context, scope Scope, messageID, content string,
	components []ActionRow,
) (Message, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if !validID(messageID) || !utf8.ValidString(content) || utf8.RuneCountInString(content) > 2000 {
		return Message{}, errors.New("invalid discord message edit")
	}
	if err := validateRows(components, false); err != nil {
		return Message{}, err
	}
	channel, err := c.prepare(ctx, scope)
	if err != nil {
		return Message{}, err
	}
	if components == nil {
		components = []ActionRow{}
	}
	var result Message
	err = c.json(ctx, http.MethodPatch, "/channels/"+channel.ID+"/messages/"+messageID,
		messagePayload{Content: content, Components: components, AllowedMentions: allowedMentions{Parse: []string{}}},
		&result, false)
	if err != nil {
		return Message{}, err
	}
	if result.ID != messageID || !messageInChannel(result, channel) || result.Author.ID != c.credentials.BotUserID {
		return Message{}, invalidResponse(true)
	}
	return result, nil
}

// EnsureThread reuses the selected thread or creates/reconciles the unique
// thread attached to the source message. No arbitrary standalone threads.
func (c *Client) EnsureThread(ctx context.Context, scope Scope, messageID, name string) (Channel, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if !validID(messageID) || !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 ||
		utf8.RuneCountInString(name) > 100 {
		return Channel{}, errors.New("invalid discord thread")
	}
	parent, err := c.prepare(ctx, scope)
	if err != nil {
		return Channel{}, err
	}
	if parent.IsThread() {
		return parent, nil
	}
	if parent.Type != 0 && parent.Type != 5 {
		return Channel{}, errors.New("discord thread requires guild text channel")
	}
	if _, err := c.message(ctx, parent, messageID); err != nil {
		return Channel{}, err
	}
	threadScope := Scope{GuildID: parent.GuildID, ChannelID: parent.ID, ThreadID: messageID}
	thread, err := c.prepare(ctx, threadScope)
	if err == nil {
		return thread, nil
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		return Channel{}, err
	}
	var result Channel
	err = c.json(ctx, http.MethodPost, "/channels/"+parent.ID+"/messages/"+messageID+"/threads",
		struct {
			Name string `json:"name"`
		}{name}, &result, false)
	if err != nil {
		if errors.As(err, &apiErr) && (apiErr.Code == DeliveryUnknown || apiErr.ProviderCode == 160004) {
			if existing, readErr := c.prepare(ctx, threadScope); readErr == nil {
				return existing, nil
			}
		}
		return Channel{}, err
	}
	if result.ID != messageID || result.ParentID != parent.ID || result.GuildID != parent.GuildID ||
		!result.IsThread() {
		return Channel{}, invalidResponse(true)
	}
	return result, nil
}

type Identity struct {
	ApplicationID string `json:"application_id"`
	BotUserID     string `json:"bot_user_id"`
}

// DiscoverIdentity resolves customer bot credentials during connection setup.
// IDs may be omitted initially; any supplied identity must match independently.
// The returned facts can then be stored as immutable connection identity.
func DiscoverIdentity(ctx context.Context, config Config) (Identity, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	client, err := newClient(config)
	if err != nil {
		return Identity{}, err
	}
	identity, err := client.identity(ctx)
	if err != nil {
		return Identity{}, err
	}
	if (config.Credentials.ApplicationID != "" && config.Credentials.ApplicationID != identity.ApplicationID) ||
		(config.Credentials.BotUserID != "" && config.Credentials.BotUserID != identity.BotUserID) {
		return Identity{}, &APIError{Code: ScopeMismatch}
	}
	return identity, nil
}

// CheckIdentity verifies the configured application and bot user independently.
func (c *Client) CheckIdentity(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	identity, err := c.identity(ctx)
	if err != nil {
		return err
	}
	if identity.BotUserID != c.credentials.BotUserID || identity.ApplicationID != c.credentials.ApplicationID {
		return &APIError{Code: ScopeMismatch}
	}
	return nil
}

func (c *Client) identity(ctx context.Context) (Identity, error) {
	var bot User
	if err := c.json(ctx, http.MethodGet, "/users/@me", nil, &bot, false); err != nil {
		return Identity{}, err
	}
	var app struct {
		ID string `json:"id"`
	}
	if err := c.json(ctx, http.MethodGet, "/applications/@me", nil, &app, false); err != nil {
		return Identity{}, err
	}
	if !validID(bot.ID) || !bot.Bot || !validID(app.ID) {
		return Identity{}, invalidResponse(false)
	}
	return Identity{ApplicationID: app.ID, BotUserID: bot.ID}, nil
}
