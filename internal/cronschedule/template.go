package cronschedule

import (
	"fmt"
	"strings"
	"text/template"
	"time"
)

func MessageData(name string, firedAt time.Time, lastFiredAt *time.Time) map[string]any {
	lastFired := ""
	if lastFiredAt != nil {
		lastFired = lastFiredAt.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"trigger": map[string]any{
			"name":          name,
			"fired_at":      firedAt.UTC().Format(time.RFC3339),
			"last_fired_at": lastFired,
		},
	}
}

func ValidateMessageTemplate(messageTemplate string) error {
	_, err := RenderMessage(messageTemplate, MessageData("sample", time.Time{}, nil))
	return err
}

func RenderMessage(messageTemplate string, data map[string]any) (string, error) {
	parsed, err := template.New("cron_message").Option("missingkey=error").Parse(messageTemplate)
	if err != nil {
		return "", fmt.Errorf("invalid message template: %w", err)
	}
	var rendered strings.Builder
	if err := parsed.Execute(&rendered, data); err != nil {
		return "", fmt.Errorf("invalid message template: %w", err)
	}
	return rendered.String(), nil
}
