package discord

import (
	"encoding/json"
	"strconv"
)

func validID(id string) bool {
	if id == "" || id[0] == '0' {
		return false
	}
	for _, ch := range id {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	_, err := strconv.ParseUint(id, 10, 64)
	return err == nil
}

type User struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
	Bot        bool   `json:"bot"`
}

type Channel struct {
	ID             string          `json:"id"`
	GuildID        string          `json:"guild_id,omitempty"`
	ParentID       string          `json:"parent_id,omitempty"`
	Type           int             `json:"type"`
	Name           string          `json:"name,omitempty"`
	ThreadMetadata *ThreadMetadata `json:"thread_metadata,omitempty"`
}

type ThreadMetadata struct {
	Archived            bool `json:"archived"`
	Locked              bool `json:"locked"`
	AutoArchiveDuration int  `json:"auto_archive_duration"`
}

func (c Channel) IsThread() bool { return c.Type == 10 || c.Type == 11 || c.Type == 12 }

type Attachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size"`
	URL         string `json:"url"`
}

type Message struct {
	ID          string          `json:"id"`
	ChannelID   string          `json:"channel_id"`
	GuildID     string          `json:"guild_id,omitempty"`
	Author      User            `json:"author"`
	Content     string          `json:"content"`
	Timestamp   string          `json:"timestamp"`
	Type        int             `json:"type"`
	WebhookID   string          `json:"webhook_id,omitempty"`
	Mentions    []User          `json:"mentions"`
	Attachments []Attachment    `json:"attachments"`
	Thread      *Channel        `json:"thread,omitempty"`
	Nonce       json.RawMessage `json:"nonce,omitempty"`
}

// Scope keeps the parent channel separate from its optional selected thread.
// GuildID is a Discord server ID, optional only when authoring did not capture it
// (or for a DM). A thread is always checked against the selected parent channel.
type Scope struct {
	GuildID   string `json:"guild_id,omitempty"`
	ChannelID string `json:"channel_id"`
	ThreadID  string `json:"thread_id,omitempty"`
}

func (s Scope) valid() bool {
	return validID(s.ChannelID) && (s.GuildID == "" || validID(s.GuildID)) &&
		(s.ThreadID == "" || (validID(s.ThreadID) && s.ThreadID != s.ChannelID))
}

func (s Scope) destination() string {
	if s.ThreadID != "" {
		return s.ThreadID
	}
	return s.ChannelID
}
