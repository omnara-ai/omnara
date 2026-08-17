package cronschedule

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/template"
	"time"
)

const (
	MaxMessageTemplateBytes = 64 * 1024
	maxRenderedMessageBytes = 64 * 1024
	renderTimeout           = time.Second
)

type limitedWriter struct {
	w io.Writer
	n int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.n {
		n, err := l.w.Write(p[:l.n])
		l.n -= int64(n)
		if err != nil {
			return n, err
		}
		return n, errors.New("rendered message exceeds size limit")
	}
	n, err := l.w.Write(p)
	l.n -= int64(n)
	return n, err
}

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
	if len(messageTemplate) > MaxMessageTemplateBytes {
		return "", fmt.Errorf(
			"invalid message template: source exceeds %d bytes",
			MaxMessageTemplateBytes,
		)
	}
	parsed, err := template.New("cron_message").Option("missingkey=error").Parse(messageTemplate)
	if err != nil {
		return "", fmt.Errorf("invalid message template: %w", err)
	}
	var rendered strings.Builder
	execution := make(chan error, 1)
	go func() {
		execution <- parsed.Execute(&limitedWriter{w: &rendered, n: maxRenderedMessageBytes}, data)
	}()
	timer := time.NewTimer(renderTimeout)
	defer timer.Stop()
	select {
	case err := <-execution:
		if err != nil {
			return "", fmt.Errorf("invalid message template: %w", err)
		}
	case <-timer.C:
		return "", errors.New("invalid message template: rendering timed out")
	}
	return rendered.String(), nil
}
