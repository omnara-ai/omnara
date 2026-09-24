package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strings"

	_ "embed"
)

const (
	appIconMinPixels = 512
	appIconMaxPixels = 2000
)

//go:embed assets/default-app-icon.png
var defaultAppIconBytes []byte

type AppIcon struct {
	Filename    string
	ContentType string
	Content     []byte
}

type appIconResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func DefaultAppIcon() AppIcon {
	return AppIcon{
		Filename:    "default-app-icon.png",
		ContentType: "image/png",
		Content:     defaultAppIconBytes,
	}
}

func NewAppIcon(filename string, content []byte) (AppIcon, error) {
	if len(content) == 0 {
		return AppIcon{}, errors.New("icon data is required")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		return AppIcon{}, errors.New("icon must be a valid PNG or JPEG image")
	}
	var contentType string
	switch format {
	case "png":
		contentType = "image/png"
	case "jpeg":
		contentType = "image/jpeg"
	default:
		return AppIcon{}, errors.New("icon must be a PNG or JPEG image")
	}
	if config.Width != config.Height {
		return AppIcon{}, errors.New("icon must be square")
	}
	if config.Width < appIconMinPixels || config.Width > appIconMaxPixels {
		return AppIcon{}, fmt.Errorf(
			"icon dimensions must be between %dx%d and %dx%d pixels",
			appIconMinPixels,
			appIconMinPixels,
			appIconMaxPixels,
			appIconMaxPixels,
		)
	}
	filename = strings.TrimSpace(filepath.Base(filename))
	if filename == "" || filename == "." {
		if contentType == "image/jpeg" {
			filename = "app-icon.jpg"
		} else {
			filename = "app-icon.png"
		}
	}
	return AppIcon{
		Filename:    filename,
		ContentType: contentType,
		Content:     content,
	}, nil
}

func SetAppIcon(
	ctx context.Context,
	config OAuthConfig,
	appConfigurationToken string,
	appID string,
	icon AppIcon,
) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("token", appConfigurationToken); err != nil {
		return err
	}
	if err := writer.WriteField("app_id", appID); err != nil {
		return err
	}
	header := make(textproto.MIMEHeader)
	header.Set(
		"Content-Disposition",
		mime.FormatMediaType("form-data", map[string]string{
			"name":     "file",
			"filename": icon.Filename,
		}),
	)
	header.Set("Content-Type", icon.ContentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := part.Write(icon.Content); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpointURL(config.APIURL, "apps.icon.set"),
		&body,
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	resp, err := httpClientWithoutRedirects(config.HTTPClient).Do(req)
	if err != nil {
		return fmt.Errorf("slack app icon set failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := readResponseBody(resp.Body, manifestResponseMaxBytes)
	if err != nil {
		return fmt.Errorf("read slack app icon set response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return slackStatusError("slack app icon set", resp.StatusCode, responseBody)
	}
	var out appIconResponse
	if err := json.Unmarshal(responseBody, &out); err != nil {
		return fmt.Errorf("decode slack app icon set response: %w", err)
	}
	if !out.OK {
		if out.Error == "" {
			out.Error = "unknown_error"
		}
		return fmt.Errorf("slack app icon set rejected: %s", out.Error)
	}
	return nil
}
