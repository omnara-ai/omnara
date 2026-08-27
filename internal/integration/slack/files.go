package slack

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

type File struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Title              string `json:"title"`
	Mimetype           string `json:"mimetype"`
	Size               int64  `json:"size"`
	URLPrivate         string `json:"url_private"`
	URLPrivateDownload string `json:"url_private_download"`
	FileAccess         string `json:"file_access"`
}

type FileDownloadOptions struct {
	MaxFiles         int
	MaxFileBytes     int
	MaxTotalBytes    int
	MaxFilenameBytes int
	AcceptMediaType  func(string) bool
	DefaultFilename  func(string, string) string
}

const (
	EventFileStatusStored  = "stored"
	EventFileStatusSkipped = "skipped"
)

type EventFileResult struct {
	Ordinal           *int   `json:"ordinal,omitempty"`
	FileID            string `json:"id,omitempty"`
	Name              string `json:"name,omitempty"`
	Title             string `json:"title,omitempty"`
	Mimetype          string `json:"mimetype,omitempty"`
	DeclaredSizeBytes int64  `json:"declared_size_bytes,omitempty"`
	FileAccess        string `json:"file_access,omitempty"`
	Content           []byte `json:"-"`
	ContentType       string `json:"content_type,omitempty"`
	Filename          string `json:"filename,omitempty"`
	SizeBytes         int    `json:"size_bytes,omitempty"`
	Status            string `json:"status,omitempty"`
	SkipReason        string `json:"reason,omitempty"`
	Error             string `json:"error,omitempty"`
	Count             int    `json:"count,omitempty"`
}

const AttachmentOnlyMessageText = "Files for the previous Slack message."

type fileInfoResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	File  File   `json:"file"`
}

func DownloadEventFiles(
	ctx context.Context,
	config OAuthConfig,
	token string,
	files []File,
	options FileDownloadOptions,
) ([]EventFileResult, error) {
	if len(files) == 0 {
		return nil, nil
	}
	result := []EventFileResult{}
	totalBytes := 0
	limit := len(files)
	if options.MaxFiles > 0 && limit > options.MaxFiles {
		limit = options.MaxFiles
	}
	for ordinal, file := range files[:limit] {
		download, storedBytes, err := downloadEventFile(
			ctx,
			config,
			token,
			file,
			ordinal,
			totalBytes,
			options,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, download)
		if len(download.Content) != 0 && download.ContentType != "" {
			totalBytes += storedBytes
		}
	}
	if options.MaxFiles > 0 && len(files) > options.MaxFiles {
		result = append(result, EventFileResult{
			Status:     EventFileStatusSkipped,
			SkipReason: "too_many_attachments",
			Count:      len(files) - options.MaxFiles,
		})
	}
	return result, nil
}

func downloadEventFile(
	ctx context.Context,
	config OAuthConfig,
	token string,
	file File,
	ordinal int,
	totalBytes int,
	options FileDownloadOptions,
) (EventFileResult, int, error) {
	eventFile := eventFileResult(file, ordinal)
	hydrated, result, err := HydrateFileInfo(ctx, config, token, file)
	if err != nil {
		return EventFileResult{}, 0, err
	}
	if apiResultRetryable(result) {
		return EventFileResult{}, 0, apiResultError("hydrate slack file", result)
	}
	if result != (APIResult{}) {
		eventFile.Status = EventFileStatusSkipped
		eventFile.SkipReason = apiResultReason(result, "unavailable")
		eventFile.Error = result.Message
		return eventFile, 0, nil
	}
	file = mergeFile(file, hydrated)
	eventFile = eventFileResult(file, ordinal)
	if file.Size > int64(options.MaxFileBytes) || file.Size > int64(options.MaxTotalBytes-totalBytes) {
		eventFile.Status = EventFileStatusSkipped
		eventFile.SkipReason = "too_large"
		return eventFile, 0, nil
	}
	fileURL := privateFileURL(file)
	if fileURL == "" {
		eventFile.Status = EventFileStatusSkipped
		eventFile.SkipReason = "missing_url"
		return eventFile, 0, nil
	}
	remainingBytes := options.MaxTotalBytes - totalBytes
	if remainingBytes <= 0 {
		eventFile.Status = EventFileStatusSkipped
		eventFile.SkipReason = "too_large"
		return eventFile, 0, nil
	}
	downloadLimit := options.MaxFileBytes
	if remainingBytes < downloadLimit {
		downloadLimit = remainingBytes
	}
	content, responseContentType, result, err := DownloadFile(
		ctx,
		config,
		token,
		fileURL,
		int64(downloadLimit),
	)
	if err != nil {
		return EventFileResult{}, 0, err
	}
	if apiResultRetryable(result) {
		return EventFileResult{}, 0, apiResultError("download slack file", result)
	}
	if result != (APIResult{}) {
		eventFile.Status = EventFileStatusSkipped
		eventFile.SkipReason = apiResultReason(result, "download_failed")
		eventFile.Error = result.Message
		return eventFile, 0, nil
	}
	if len(content) == 0 {
		eventFile.Status = EventFileStatusSkipped
		eventFile.SkipReason = "empty"
		return eventFile, 0, nil
	}
	contentType := attachmentMediaType(file, responseContentType, content, options.AcceptMediaType)
	if contentType == "" {
		eventFile.Status = EventFileStatusSkipped
		eventFile.SkipReason = "unsupported_media_type"
		return eventFile, 0, nil
	}
	filename := attachmentFilename(
		file,
		contentType,
		options.DefaultFilename,
		options.MaxFilenameBytes,
	)
	eventFile.Content = content
	eventFile.ContentType = contentType
	eventFile.Filename = filename
	eventFile.SizeBytes = len(content)
	return eventFile, len(content), nil
}

func HydrateFileInfo(ctx context.Context, config OAuthConfig, token string, file File) (File, APIResult, error) {
	if file.ID == "" || (file.FileAccess != "check_file_info" && privateFileURL(file) != "") {
		return file, APIResult{}, nil
	}
	var out fileInfoResponse
	result, err := callFormAt(
		ctx,
		config.HTTPClient,
		config.APIURL,
		token,
		"files.info",
		url.Values{"file": {file.ID}},
		&out,
	)
	if err != nil {
		return File{}, APIResult{}, err
	}
	if result.RateLimited || result.TransientFailure || result.PermanentFailure || result.DeliveryUnknown {
		return File{}, result, nil
	}
	if !out.OK {
		return File{}, ErrorResult(out.Error), nil
	}
	if out.File.ID == "" {
		return file, APIResult{}, nil
	}
	return out.File, APIResult{}, nil
}

func DownloadFile(
	ctx context.Context,
	config OAuthConfig,
	token string,
	fileURL string,
	maxBytes int64,
) ([]byte, string, APIResult, error) {
	if maxBytes <= 0 {
		return nil, "", APIResult{
			PermanentFailure: true,
			Code:             "file_too_large",
			Message:          "slack file exceeds attachment limits",
		}, nil
	}
	parsed, err := url.Parse(strings.TrimSpace(fileURL))
	if err != nil || !allowedSlackFileURL(parsed, config.APIURL) {
		return nil, "", APIResult{PermanentFailure: true, Code: "invalid_file_url", Message: "invalid slack file url"}, nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, defaultToolTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", APIResult{PermanentFailure: true, Message: err.Error()}, nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClientWithoutRedirects(config.HTTPClient).Do(req)
	if err != nil {
		return nil, "", APIResult{DeliveryUnknown: true, Message: err.Error()}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, "", APIResult{
			RateLimited: true,
			RetryAfter:  retryAfter(resp.Header.Get("Retry-After")),
			Message:     "slack rate limited the request",
		}, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", APIResult{DeliveryUnknown: true, Message: err.Error()}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode >= 500 {
			return nil, "", APIResult{
				Code:             "transient_failure",
				TransientFailure: true,
				Message:          fmt.Sprintf("slack returned status %d", resp.StatusCode),
			}, nil
		}
		return nil, "", APIResult{
			Code:             "permanent_failure",
			PermanentFailure: true,
			Message:          fmt.Sprintf("slack returned status %d", resp.StatusCode),
		}, nil
	}
	if int64(len(body)) > maxBytes {
		return nil, "", APIResult{
			PermanentFailure: true,
			Code:             "file_too_large",
			Message:          "slack file exceeds attachment limits",
		}, nil
	}
	return body, resp.Header.Get("Content-Type"), APIResult{}, nil
}

func allowedSlackFileURL(parsed *url.URL, apiURL string) bool {
	if parsed == nil || parsed.Host == "" {
		return false
	}
	if apiURL != "" {
		if api, err := url.Parse(apiURL); err == nil && api.Scheme != "" && api.Host != "" &&
			strings.EqualFold(parsed.Scheme, api.Scheme) &&
			strings.EqualFold(parsed.Host, api.Host) {
			return true
		}
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, suffix := range []string{"slack.com", "slack-edge.com", "slack-files.com"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

func eventFileResult(file File, ordinal int) EventFileResult {
	return EventFileResult{
		Ordinal:           &ordinal,
		FileID:            file.ID,
		Name:              file.Name,
		Title:             file.Title,
		Mimetype:          file.Mimetype,
		DeclaredSizeBytes: file.Size,
		FileAccess:        file.FileAccess,
	}
}

func mergeFile(original, hydrated File) File {
	if hydrated.ID == "" {
		hydrated.ID = original.ID
	}
	if hydrated.Name == "" {
		hydrated.Name = original.Name
	}
	if hydrated.Title == "" {
		hydrated.Title = original.Title
	}
	if hydrated.Mimetype == "" {
		hydrated.Mimetype = original.Mimetype
	}
	if hydrated.Size == 0 {
		hydrated.Size = original.Size
	}
	if hydrated.URLPrivate == "" {
		hydrated.URLPrivate = original.URLPrivate
	}
	if hydrated.URLPrivateDownload == "" {
		hydrated.URLPrivateDownload = original.URLPrivateDownload
	}
	if hydrated.FileAccess == "" {
		hydrated.FileAccess = original.FileAccess
	}
	return hydrated
}

func privateFileURL(file File) string {
	if strings.TrimSpace(file.URLPrivateDownload) != "" {
		return strings.TrimSpace(file.URLPrivateDownload)
	}
	return strings.TrimSpace(file.URLPrivate)
}

func attachmentMediaType(
	file File,
	responseContentType string,
	content []byte,
	accept func(string) bool,
) string {
	candidates := []string{
		file.Mimetype,
		mime.TypeByExtension(filepath.Ext(file.Name)),
		mime.TypeByExtension(filepath.Ext(file.Title)),
		responseContentType,
		http.DetectContentType(content),
	}
	for _, candidate := range candidates {
		mediaType := normalizeMediaType(candidate)
		if accept == nil {
			if mediaType != "" {
				return mediaType
			}
			continue
		}
		if accept(mediaType) {
			return mediaType
		}
	}
	return ""
}

func normalizeMediaType(value string) string {
	mediaType := strings.ToLower(strings.TrimSpace(value))
	if mediaType == "" {
		return ""
	}
	if parsed, _, err := mime.ParseMediaType(mediaType); err == nil {
		mediaType = parsed
	}
	if mediaType == "image/jpg" {
		return "image/jpeg"
	}
	return mediaType
}

func attachmentFilename(
	file File,
	contentType string,
	defaultFilename func(string, string) string,
	maxBytes int,
) string {
	filename := strings.TrimSpace(file.Name)
	if filename == "" {
		filename = strings.TrimSpace(file.Title)
	}
	if filename == "" {
		filename = strings.TrimSpace(file.ID)
	}
	filename = attachmentDefaultFilename(filename, contentType, defaultFilename)
	if strings.IndexByte(filename, 0) >= 0 || (maxBytes > 0 && len(filename) > maxBytes) {
		return attachmentDefaultFilename("", contentType, defaultFilename)
	}
	return filename
}

func attachmentDefaultFilename(
	filename string,
	contentType string,
	defaultFilename func(string, string) string,
) string {
	if defaultFilename != nil {
		return defaultFilename(filename, contentType)
	}
	if filename != "" {
		return filename
	}
	return "attachment"
}

func apiResultRetryable(result APIResult) bool {
	return result.RateLimited || result.TransientFailure || result.DeliveryUnknown
}

func apiResultError(action string, result APIResult) error {
	if result.Message != "" {
		return fmt.Errorf("%s: %s", action, result.Message)
	}
	if result.Code != "" {
		return fmt.Errorf("%s: %s", action, result.Code)
	}
	return fmt.Errorf("%s failed", action)
}

func apiResultReason(result APIResult, fallback string) string {
	if result.ProviderCode != "" {
		return result.ProviderCode
	}
	if result.Code != "" && result.Code != "permanent_failure" {
		return result.Code
	}
	return fallback
}

func SkippedFileSummary(files []EventFileResult) string {
	if len(files) == 0 {
		return ""
	}
	lines := make([]string, 0, len(files))
	for _, file := range files {
		if file.Status == EventFileStatusStored {
			continue
		}
		lines = append(lines, "- "+skippedEventFileSummaryLine(file))
	}
	if len(lines) == 0 {
		return ""
	}
	return "Slack files not included:\n" + strings.Join(lines, "\n")
}

func skippedEventFileSummaryLine(file EventFileResult) string {
	name := file.Filename
	if name == "" {
		name = file.Name
	}
	if name == "" {
		name = file.Title
	}
	if name == "" {
		name = file.FileID
	}
	if name == "" {
		name = "attachment"
	}
	reason := file.SkipReason
	if reason == "" {
		reason = file.Status
	}
	if reason == "" {
		reason = "unavailable"
	}
	return name + " skipped: " + strings.ReplaceAll(reason, "_", " ")
}
