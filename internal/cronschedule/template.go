package cronschedule

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/template"
	"time"
)

const (
	MaxMessageTemplateBytes = 64 * 1024
	maxRenderedMessageBytes = 64 * 1024
	maxPrintfWidth          = 1024
	renderTimeout           = time.Second
)

var templateFuncs = template.FuncMap{
	"print":   boundedFormatFunc(fmt.Sprint),
	"println": boundedFormatFunc(fmt.Sprintln),
	"printf": func(format string, args ...any) (string, error) {
		if err := validatePrintfFormat(format); err != nil {
			return "", err
		}
		return boundFormatOutput(fmt.Sprintf(format, args...))
	},
}

func boundedFormatFunc(format func(...any) string) func(...any) (string, error) {
	return func(args ...any) (string, error) {
		return boundFormatOutput(format(args...))
	}
}

func boundFormatOutput(formatted string) (string, error) {
	if len(formatted) > maxRenderedMessageBytes {
		return "", errors.New("formatted value exceeds size limit")
	}
	return formatted, nil
}

func validatePrintfFormat(format string) error {
	for i := 0; i < len(format); {
		if format[i] != '%' {
			i++
			continue
		}
		i++
		for i < len(format) && strings.IndexByte("+-# 0", format[i]) >= 0 {
			i++
		}
		next, err := validatePrintfBound(format, i)
		if err != nil {
			return err
		}
		i = next
		if i < len(format) && format[i] == '.' {
			next, err := validatePrintfBound(format, i+1)
			if err != nil {
				return err
			}
			i = next
		}
	}
	return nil
}

func validatePrintfBound(format string, start int) (int, error) {
	if start < len(format) && format[start] == '*' {
		return 0, errors.New("star width and precision specifiers are not supported")
	}
	end := start
	for end < len(format) && format[end] >= '0' && format[end] <= '9' {
		end++
	}
	if end == start {
		return end, nil
	}
	width, err := strconv.Atoi(format[start:end])
	if err != nil || width > maxPrintfWidth {
		return 0, fmt.Errorf("width and precision specifiers must be at most %d", maxPrintfWidth)
	}
	return end, nil
}

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
	parsed, err := template.New("cron_message").
		Option("missingkey=error").
		Funcs(templateFuncs).
		Parse(messageTemplate)
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
