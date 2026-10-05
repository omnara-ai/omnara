package tools

import "errors"

var ErrActionRequired = errors.New("tool call requires permission")

var errFileToolUnavailable = errors.New("operation unavailable")
