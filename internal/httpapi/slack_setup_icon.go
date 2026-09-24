package httpapi

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/slack"
)

const (
	maxSlackSetupIconBytes        = 5 * 1024 * 1024
	maxSlackSetupRequestBodyBytes = 8 * 1024 * 1024
)

func slackSetupAppIcon(body openapi.CreateSlackSetupRequest) (slack.AppIcon, error) {
	if body.Icon == nil {
		return slack.DefaultAppIcon(), nil
	}
	data := strings.TrimSpace(body.Icon.DataBase64)
	if data == "" {
		return slack.AppIcon{}, errors.New("icon data is required")
	}
	content, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return slack.AppIcon{}, errors.New("icon data must be standard base64")
	}
	if len(content) > maxSlackSetupIconBytes {
		return slack.AppIcon{}, fmt.Errorf("icon must be %d MB or smaller", maxSlackSetupIconBytes/(1024*1024))
	}
	filename := ""
	if body.Icon.Filename != nil {
		filename = *body.Icon.Filename
	}
	icon, err := slack.NewAppIcon(filename, content)
	if err != nil {
		return slack.AppIcon{}, err
	}
	return icon, nil
}
