package log

import (
	"io"
	"log/slog"
	"strings"
)

// NewJSONHandler returns the service JSON log handler with Omnara's standard
// field normalization and secret scrubbing.
func NewJSONHandler(w io.Writer, opts *slog.HandlerOptions) slog.Handler {
	return slog.NewJSONHandler(w, jsonHandlerOptionsWith(false, opts))
}

func jsonHandlerOptions(dropTime bool) *slog.HandlerOptions {
	return jsonHandlerOptionsWith(dropTime, nil)
}

func jsonHandlerOptionsWith(dropTime bool, opts *slog.HandlerOptions) *slog.HandlerOptions {
	next := &slog.HandlerOptions{}
	if opts != nil {
		*next = *opts
	}
	replace := next.ReplaceAttr
	next.ReplaceAttr = func(groups []string, attr slog.Attr) slog.Attr {
		if replace != nil {
			attr = replace(groups, attr)
			if attr.Equal(slog.Attr{}) {
				return attr
			}
		}
		attr = scrubAttr(attr)
		switch attr.Key {
		case slog.TimeKey:
			if dropTime {
				return slog.Attr{}
			}
		case slog.LevelKey:
			if level, ok := attr.Value.Any().(slog.Level); ok {
				attr.Value = slog.StringValue(strings.ToLower(level.String()))
			}
		case slog.MessageKey:
			attr.Key = "message"
		}
		return attr
	}
	return next
}

func scrubAttr(attr slog.Attr) slog.Attr {
	if attr.Value.Kind() == slog.KindString {
		attr.Value = slog.StringValue(ScrubLogString(attr.Value.String()))
		return attr
	}
	if attr.Value.Kind() == slog.KindAny {
		switch value := attr.Value.Any().(type) {
		case error:
			attr.Value = slog.StringValue(ScrubLogString(value.Error()))
		case string:
			attr.Value = slog.StringValue(ScrubLogString(value))
		}
	}
	return attr
}
