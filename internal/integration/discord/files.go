package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	MaxFiles       = 10
	MaxFileBytes   = 8 * 1024 * 1024
	MaxUploadBytes = 25 * 1024 * 1024
)

type Upload struct {
	Filename string
	Content  []byte
}

type uploadMetadata struct {
	ID       int    `json:"id"`
	Filename string `json:"filename"`
}

type Download struct {
	Attachment Attachment
	Content    []byte
}

func safeFilename(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && utf8.ValidString(name) &&
		!strings.ContainsAny(name, "/\\\x00\r\n")
}

func encodeMessage(payload messagePayload, files []Upload) ([]byte, string, error) {
	if len(files) > MaxFiles {
		return nil, "", errors.New("too many discord files")
	}
	var total int
	for i, file := range files {
		if !safeFilename(file.Filename) || len(file.Content) > MaxFileBytes {
			return nil, "", errors.New("invalid discord upload or file too large")
		}
		total += len(file.Content)
		if total > MaxUploadBytes {
			return nil, "", errors.New("discord upload exceeds byte limit")
		}
		payload.Attachments = append(payload.Attachments, uploadMetadata{ID: i, Filename: file.Filename})
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, "", errors.New("invalid discord message")
	}
	if len(files) == 0 {
		return data, "application/json", nil
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("payload_json", string(data)); err != nil {
		return nil, "", err
	}
	for i, file := range files {
		part, err := w.CreateFormFile(fmt.Sprintf("files[%d]", i), file.Filename)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(file.Content); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	if body.Len() > MaxUploadBytes {
		return nil, "", errors.New("discord upload exceeds byte limit")
	}
	return body.Bytes(), w.FormDataContentType(), nil
}

// DownloadAttachment refreshes metadata from the scoped message so expired CDN
// URLs are not persisted authority. CDN requests never carry bot credentials.
func (c *Client) DownloadAttachment(
	ctx context.Context, scope Scope, messageID, attachmentID string,
) (Download, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if !validID(attachmentID) {
		return Download{}, errors.New("invalid discord attachment ID")
	}
	message, err := c.GetMessage(ctx, scope, messageID)
	if err != nil {
		return Download{}, err
	}
	for _, attachment := range message.Attachments {
		if attachment.ID != attachmentID {
			continue
		}
		if attachment.Size < 0 || attachment.Size > MaxFileBytes || !safeFilename(attachment.Filename) ||
			!validAttachmentURL(attachment.URL, message.ChannelID, attachment.ID) {
			return Download{}, errors.New("invalid discord attachment or file too large")
		}
		data, err := c.do(ctx, http.MethodGet, attachment.URL, "", nil, false, false, MaxFileBytes)
		if err != nil {
			return Download{}, err
		}
		if int64(len(data)) != attachment.Size {
			return Download{}, invalidResponse(false)
		}
		return Download{Attachment: attachment, Content: data}, nil
	}
	return Download{}, &APIError{Code: ScopeMismatch}
}

func validAttachmentURL(raw, channelID, attachmentID string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Port() != "" ||
		(u.Host != "cdn.discordapp.com" && u.Host != "media.discordapp.net") {
		return false
	}
	parts := strings.Split(u.EscapedPath(), "/")
	return len(parts) == 5 && parts[1] == "attachments" && parts[2] == channelID &&
		parts[3] == attachmentID && parts[4] != "" && parts[4] != "." && parts[4] != ".."
}
