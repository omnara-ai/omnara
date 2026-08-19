package cronschedule

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
	"time"
)

const (
	MaxMessageTemplateBytes = 64 * 1024
	maxRenderedMessageBytes = 64 * 1024
	maxPrintfWidth          = 1024
	renderTimeout           = time.Second
)

var templateFuncs = template.FuncMap{
	"html":     boundedFormatFunc(template.HTMLEscaper),
	"js":       boundedFormatFunc(template.JSEscaper),
	"print":    boundedFormatFunc(fmt.Sprint),
	"println":  boundedFormatFunc(fmt.Sprintln),
	"printf":   boundedPrintf,
	"urlquery": boundedFormatFunc(template.URLQueryEscaper),
}

func boundedFormatFunc(format func(...any) string) func(...any) (string, error) {
	return func(args ...any) (string, error) {
		if err := validateFormattingInput(args); err != nil {
			return "", err
		}
		return boundFormatOutput(format(args...))
	}
}

func boundedPrintf(format string, args ...any) (string, error) {
	if err := validatePrintfFormat(format); err != nil {
		return "", err
	}
	if err := validateFormattingInput(args); err != nil {
		return "", err
	}
	return boundFormatOutput(fmt.Sprintf(format, args...))
}

// validateFormattingInput prevents variadic formatting functions from joining
// many individually bounded strings into one large allocation before
// boundFormatOutput can inspect the result. Count repeated arguments each time
// they are passed so aliases cannot bypass the limit.
func validateFormattingInput(args []any) error {
	remaining := maxRenderedMessageBytes
	for _, arg := range args {
		var formatted string
		if value, ok := arg.(string); ok {
			formatted = value
		} else {
			formatted = fmt.Sprint(arg)
		}
		if len(formatted) > remaining {
			return errors.New("formatted arguments exceed size limit")
		}
		remaining -= len(formatted)
	}
	return nil
}

func boundFormatOutput(formatted string) (string, error) {
	if len(formatted) > maxRenderedMessageBytes {
		return "", errors.New("formatted value exceeds size limit")
	}
	return formatted, nil
}

func validatePrintfFormat(format string) error {
	remaining := maxRenderedMessageBytes
	for i := 0; i < len(format); {
		if format[i] != '%' {
			i++
			continue
		}
		i++
		for i < len(format) && strings.IndexByte("+-# 0", format[i]) >= 0 {
			i++
		}
		if i < len(format) && format[i] == '[' {
			return errors.New("explicit argument indexes are not supported")
		}
		next, bound, err := validatePrintfBound(format, i)
		if err != nil {
			return err
		}
		if bound > remaining {
			return errors.New("cumulative width and precision exceed size limit")
		}
		remaining -= bound
		i = next
		if i < len(format) && format[i] == '.' {
			i++
			if i < len(format) && format[i] == '[' {
				return errors.New("explicit argument indexes are not supported")
			}
			next, bound, err := validatePrintfBound(format, i)
			if err != nil {
				return err
			}
			if bound > remaining {
				return errors.New("cumulative width and precision exceed size limit")
			}
			remaining -= bound
			i = next
		}
		if i < len(format) && format[i] == '[' {
			return errors.New("explicit argument indexes are not supported")
		}
		if i < len(format) {
			i++
		}
	}
	return nil
}

func validatePrintfBound(format string, start int) (int, int, error) {
	if start < len(format) && format[start] == '*' {
		return 0, 0, errors.New("star width and precision specifiers are not supported")
	}
	end := start
	for end < len(format) && format[end] >= '0' && format[end] <= '9' {
		end++
	}
	if end == start {
		return end, 0, nil
	}
	width, err := strconv.Atoi(format[start:end])
	if err != nil || width > maxPrintfWidth {
		return 0, 0, fmt.Errorf(
			"width and precision specifiers must be at most %d",
			maxPrintfWidth,
		)
	}
	return end, width, nil
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
	for _, associated := range parsed.Templates() {
		if containsTemplateInvocation(associated.Tree.Root) {
			return "", errors.New(
				"invalid message template: template invocations are not supported",
			)
		}
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

// containsTemplateInvocation rejects {{template}} and {{block}} execution.
// text/template permits recursive calls up to a large fixed depth, and the
// render timeout cannot cancel the goroutine executing them.
func containsTemplateInvocation(node parse.Node) bool {
	switch node := node.(type) {
	case *parse.ListNode:
		if node == nil {
			return false
		}
		for _, child := range node.Nodes {
			if containsTemplateInvocation(child) {
				return true
			}
		}
	case *parse.IfNode:
		return containsTemplateInvocation(node.List) ||
			(node.ElseList != nil && containsTemplateInvocation(node.ElseList))
	case *parse.RangeNode:
		return containsTemplateInvocation(node.List) ||
			(node.ElseList != nil && containsTemplateInvocation(node.ElseList))
	case *parse.TemplateNode:
		return true
	case *parse.WithNode:
		return containsTemplateInvocation(node.List) ||
			(node.ElseList != nil && containsTemplateInvocation(node.ElseList))
	}
	return false
}
