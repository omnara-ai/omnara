package slack

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUploadFile(t *testing.T) {
	content := []byte("artifact contents")
	requests := make([]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		switch r.URL.Path {
		case "/files.getUploadURLExternal":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse upload URL request: %v", err)
			}
			if r.Header.Get("Authorization") != "Bearer xoxb-test" ||
				r.Form.Get("filename") != "report.txt" ||
				r.Form.Get("length") != "17" {
				t.Fatalf("upload URL request auth=%q form=%v", r.Header.Get("Authorization"), r.Form)
			}
			writeSlackTestJSON(w, map[string]any{
				"ok":         true,
				"upload_url": "https://files.slack.com/upload/v1/test",
				"file_id":    "F123",
			})
		case "/upload/v1/test":
			if auth := r.Header.Get("Authorization"); auth != "" {
				t.Fatalf("file upload authorization = %q, want none", auth)
			}
			if r.Header.Get("Content-Type") != "application/octet-stream" || r.ContentLength != int64(len(content)) {
				t.Fatalf("file upload content type=%q length=%d", r.Header.Get("Content-Type"), r.ContentLength)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read file upload: %v", err)
			}
			if string(body) != string(content) {
				t.Fatalf("file upload body = %q", body)
			}
			w.WriteHeader(http.StatusOK)
		case "/files.completeUploadExternal":
			if r.Header.Get("Authorization") != "Bearer xoxb-test" {
				t.Fatalf("complete upload authorization = %q", r.Header.Get("Authorization"))
			}
			var payload struct {
				Files []struct {
					ID    string `json:"id"`
					Title string `json:"title"`
				} `json:"files"`
				ChannelID      string `json:"channel_id"`
				ThreadTS       string `json:"thread_ts"`
				InitialComment string `json:"initial_comment"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode completion request: %v", err)
			}
			if len(payload.Files) != 1 || payload.Files[0].ID != "F123" || payload.Files[0].Title != "report.txt" ||
				payload.ChannelID != "C123" || payload.ThreadTS != "111.222" ||
				payload.InitialComment != "here is the report" {
				t.Fatalf("completion payload = %+v", payload)
			}
			writeSlackTestJSON(w, map[string]any{"ok": true})
		default:
			t.Fatalf("unexpected Slack path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	fileID, result, err := UploadFile(
		context.Background(),
		slackTestClient(server),
		MessageTarget{Channel: "C123", ThreadTS: "111.222", BotToken: "xoxb-test"},
		"report.txt",
		content,
		allowFileUploadRequest,
	)
	if err != nil {
		t.Fatalf("upload file: %v", err)
	}
	if fileID != "F123" || result != (APIResult{}) {
		t.Fatalf("upload file id=%q result=%+v", fileID, result)
	}
	result, err = CompleteFileUpload(
		context.Background(),
		slackTestClient(server),
		MessageTarget{Channel: "C123", ThreadTS: "111.222", BotToken: "xoxb-test"},
		fileID,
		"report.txt",
		"here is the report",
	)
	if err != nil {
		t.Fatalf("complete file upload: %v", err)
	}
	if result != (APIResult{}) {
		t.Fatalf("completion result = %+v", result)
	}
	want := []string{"/files.getUploadURLExternal", "/upload/v1/test", "/files.completeUploadExternal"}
	if strings.Join(requests, ",") != strings.Join(want, ",") {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
}

func TestCompleteFileUploadServerErrorIsDeliveryUnknown(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files.completeUploadExternal" {
			t.Fatalf("unexpected Slack path %s", r.URL.Path)
		}
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	result, err := CompleteFileUpload(
		context.Background(),
		slackTestClient(server),
		MessageTarget{Channel: "C123", BotToken: "xoxb-test"},
		"F123",
		"report.txt",
		"here is the report",
	)
	if err != nil {
		t.Fatalf("complete file upload: %v", err)
	}
	if !result.DeliveryUnknown || result.TransientFailure {
		t.Fatalf("upload result = %+v, want delivery unknown", result)
	}
	if requests != 1 {
		t.Fatalf("completion requests = %d, want 1", requests)
	}
}

func TestUploadFileRejectsUnsafeUploadURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSlackTestJSON(w, map[string]any{
			"ok":         true,
			"upload_url": "https://example.com/upload",
			"file_id":    "F123",
		})
	}))
	defer server.Close()

	fileID, result, err := UploadFile(
		context.Background(),
		slackTestClient(server),
		MessageTarget{Channel: "C123", BotToken: "xoxb-test"},
		"report.txt",
		[]byte("artifact contents"),
		allowFileUploadRequest,
	)
	if err != nil {
		t.Fatalf("upload file: %v", err)
	}
	if fileID != "" || !result.PermanentFailure || result.Code != "invalid_file_url" {
		t.Fatalf("upload result = %+v", result)
	}
}

func TestUploadFileContentHidesSignedURLOnTransportFailure(t *testing.T) {
	result, err := uploadFileContent(
		context.Background(),
		&http.Client{Transport: failingUploadTransport{}},
		"https://files.slack.com/upload/v1/signed-secret",
		[]byte("artifact contents"),
	)
	if err != nil {
		t.Fatalf("upload file content: %v", err)
	}
	if !result.DeliveryUnknown || result.Message != "Slack file upload request failed." {
		t.Fatalf("upload result = %+v", result)
	}
}

func TestFilePreShareResultExplainsMissingScope(t *testing.T) {
	t.Parallel()
	result := filePreShareResult(ErrorResult("missing_scope"))
	if result.Code != "permanent_failure" || result.ProviderCode != "missing_scope" ||
		result.Message != "Slack integration must be reauthorized with files:write before it can send artifacts." {
		t.Fatalf("pre-share result = %+v", result)
	}
}

type failingUploadTransport struct{}

func (failingUploadTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network down")
}

func allowFileUploadRequest(context.Context) error {
	return nil
}

func TestDownloadEventFilesHydratesMissingPrivateURL(t *testing.T) {
	t.Parallel()
	content := []byte("png bytes")
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.info":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse files.info form: %v", err)
			}
			if r.Header.Get("Authorization") != "Bearer xoxb-token" || r.Form.Get("file") != "F_MISSING_URL" {
				t.Fatalf("unexpected files.info request auth=%q form=%v", r.Header.Get("Authorization"), r.Form)
			}
			writeSlackTestJSON(w, map[string]any{
				"ok": true,
				"file": map[string]any{
					"id":                   "F_MISSING_URL",
					"name":                 "pixel.png",
					"mimetype":             "image/png",
					"size":                 len(content),
					"url_private_download": server.URL + "/files/pixel.png",
				},
			})
		case "/files/pixel.png":
			if r.Header.Get("Authorization") != "Bearer xoxb-token" {
				t.Fatalf("file download authorization = %q", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(content)
		default:
			t.Fatalf("unexpected slack test path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := DownloadEventFiles(
		context.Background(),
		OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
		"xoxb-token",
		[]File{{ID: "F_MISSING_URL", Name: "stub"}},
		testFileDownloadOptions(),
	)
	if err != nil {
		t.Fatalf("download event files: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("downloaded files = %+v, want one file", result)
	}
	file := result[0]
	if file.FileID != "F_MISSING_URL" || file.ContentType != "image/png" ||
		file.Filename != "pixel.png" || string(file.Content) != string(content) {
		t.Fatalf("downloaded file = %+v", file)
	}
}

func TestDownloadEventFilesResultUsesSlackErrorCode(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.info":
			writeSlackTestJSON(w, map[string]any{"ok": false, "error": "file_not_found"})
		default:
			t.Fatalf("unexpected slack test path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := DownloadEventFiles(
		context.Background(),
		OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
		"xoxb-token",
		[]File{{ID: "F_MISSING_URL"}},
		testFileDownloadOptions(),
	)
	if err != nil {
		t.Fatalf("download event files: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("result = %+v, want one skipped file", result)
	}
	file := result[0]
	if file.Status != EventFileStatusSkipped || file.SkipReason != "file_not_found" {
		t.Fatalf("file status=%q reason=%q, want skipped file_not_found", file.Status, file.SkipReason)
	}
	if !strings.Contains(file.Error, "file_not_found") {
		t.Fatalf("file error = %q, want slack error detail", file.Error)
	}
}

func TestAttachmentFilenameFallsBackForNUL(t *testing.T) {
	t.Parallel()
	got := attachmentFilename(
		File{Name: "before\x00after.png"},
		"image/png",
		func(string, string) string { return "attachment.png" },
		255,
	)
	if got != "attachment.png" {
		t.Fatalf("attachment filename = %q, want fallback", got)
	}
}

func TestAttachmentMediaTypeRejectsInvalidUTF8Text(t *testing.T) {
	t.Parallel()
	got := attachmentMediaType(
		File{Name: "data.csv", Mimetype: "text/csv"},
		"text/csv",
		[]byte{0xff},
		func(mediaType string) bool { return mediaType == "text/csv" },
	)
	if got != "" {
		t.Fatalf("attachment media type = %q, want none", got)
	}
}

func TestAttachmentMediaTypePrefersUTF8CSVFilename(t *testing.T) {
	t.Parallel()
	got := attachmentMediaType(
		File{Name: "data.csv", Mimetype: "application/vnd.ms-excel"},
		"application/vnd.ms-excel",
		[]byte("name,value\nalpha,1\n"),
		func(mediaType string) bool {
			return mediaType == "text/csv" || mediaType == "application/vnd.ms-excel"
		},
	)
	if got != "text/csv" {
		t.Fatalf("attachment media type = %q, want text/csv", got)
	}
}

func TestDownloadEventFilesSkipsDeclaredOversize(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected slack test path %s", r.URL.Path)
	}))
	defer server.Close()

	result, err := DownloadEventFiles(
		context.Background(),
		OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
		"xoxb-token",
		[]File{{
			ID:                 "F_LARGE",
			Name:               "large.png",
			Mimetype:           "image/png",
			Size:               1025,
			URLPrivateDownload: server.URL + "/files/large.png",
		}},
		testFileDownloadOptions(),
	)
	if err != nil {
		t.Fatalf("download event files: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("result = %+v, want one skipped file", result)
	}
	file := result[0]
	if file.Status != EventFileStatusSkipped || file.SkipReason != "too_large" {
		t.Fatalf("file status=%q reason=%q, want skipped too_large", file.Status, file.SkipReason)
	}
}

func TestDownloadEventFilesSkipsDownloadedOversize(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files/large.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(make([]byte, 1025))
		default:
			t.Fatalf("unexpected slack test path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := DownloadEventFiles(
		context.Background(),
		OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
		"xoxb-token",
		[]File{{
			ID:                 "F_LARGE",
			Name:               "large.png",
			Mimetype:           "image/png",
			URLPrivateDownload: server.URL + "/files/large.png",
		}},
		testFileDownloadOptions(),
	)
	if err != nil {
		t.Fatalf("download event files: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("result = %+v, want one skipped file", result)
	}
	file := result[0]
	if file.Status != EventFileStatusSkipped || file.SkipReason != "file_too_large" {
		t.Fatalf("file status=%q reason=%q, want skipped file_too_large", file.Status, file.SkipReason)
	}
}

func TestDownloadEventFilesRecordsTooManyAttachments(t *testing.T) {
	t.Parallel()
	options := testFileDownloadOptions()
	options.MaxFiles = 1
	result, err := DownloadEventFiles(
		context.Background(),
		OAuthConfig{},
		"xoxb-token",
		[]File{
			{ID: "F1", Name: "one.png", Size: 1025, URLPrivateDownload: "https://files.slack.com/files-pri/T123-F1/one.png"},
			{ID: "F2", Name: "two.png"},
		},
		options,
	)
	if err != nil {
		t.Fatalf("download event files: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("result = %+v, want one skipped file and attachment count result", result)
	}
	if result[0].Status != EventFileStatusSkipped || result[0].SkipReason != "too_large" {
		t.Fatalf("first file status=%q reason=%q, want skipped too_large", result[0].Status, result[0].SkipReason)
	}
	if result[1].Status != EventFileStatusSkipped ||
		result[1].SkipReason != "too_many_attachments" ||
		result[1].Count != 1 {
		t.Fatalf("second file = %+v, want too_many_attachments count 1", result[1])
	}
}

func TestDownloadEventFilesReturnsErrorForRetryableFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		files   func(serverURL string) []File
		handler func(t *testing.T, w http.ResponseWriter, r *http.Request)
		wantErr string
	}{
		{
			name: "files info rate limited",
			files: func(string) []File {
				return []File{{ID: "F_RATE_LIMITED"}}
			},
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				if r.URL.Path != "/files.info" {
					t.Fatalf("unexpected slack test path %s", r.URL.Path)
				}
				w.WriteHeader(http.StatusTooManyRequests)
			},
			wantErr: "hydrate slack file",
		},
		{
			name: "download transient failure",
			files: func(serverURL string) []File {
				return []File{{
					ID:                 "F_TRANSIENT",
					Name:               "transient.png",
					Mimetype:           "image/png",
					URLPrivateDownload: serverURL + "/files/transient.png",
				}}
			},
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				if r.URL.Path != "/files/transient.png" {
					t.Fatalf("unexpected slack test path %s", r.URL.Path)
				}
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantErr: "download slack file",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				test.handler(t, w, r)
			}))
			defer server.Close()

			result, err := DownloadEventFiles(
				context.Background(),
				OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
				"xoxb-token",
				test.files(server.URL),
				testFileDownloadOptions(),
			)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, test.wantErr)
			}
			if len(result) != 0 {
				t.Fatalf("result = %+v, want none on retryable failure", result)
			}
		})
	}
}

func TestDownloadFileRejectsUnsafeURL(t *testing.T) {
	t.Parallel()
	tests := []string{
		"http://files.slack.com/files-pri/T123-F123/file.png",
		"https://example.com/file.png",
	}
	for _, fileURL := range tests {
		t.Run(fileURL, func(t *testing.T) {
			t.Parallel()
			content, contentType, result, err := DownloadFile(
				context.Background(),
				OAuthConfig{},
				"xoxb-token",
				fileURL,
				1024,
			)
			if err != nil {
				t.Fatalf("download file: %v", err)
			}
			if len(content) != 0 || contentType != "" || !result.PermanentFailure || result.Code != "invalid_file_url" {
				t.Fatalf("result content=%q contentType=%q api=%+v", content, contentType, result)
			}
		})
	}
}

func TestDownloadFileDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	content, contentType, result, err := DownloadFile(
		context.Background(),
		OAuthConfig{APIURL: source.URL, HTTPClient: source.Client()},
		"xoxb-token",
		source.URL+"/file",
		1024,
	)
	if err != nil {
		t.Fatalf("download file: %v", err)
	}
	if len(content) != 0 || contentType != "" || !result.PermanentFailure {
		t.Fatalf("result content=%q contentType=%q api=%+v", content, contentType, result)
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target requests = %d, want 0", targetRequests.Load())
	}
}

func testFileDownloadOptions() FileDownloadOptions {
	return FileDownloadOptions{
		MaxFiles:         20,
		MaxFileBytes:     1024,
		MaxTotalBytes:    1024,
		MaxFilenameBytes: 255,
		AcceptMediaType:  func(mediaType string) bool { return mediaType == "image/png" },
		DefaultFilename: func(filename, mediaType string) string {
			if filename != "" {
				return filename
			}
			return "attachment"
		},
	}
}
