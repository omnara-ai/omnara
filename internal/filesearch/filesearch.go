// Package filesearch holds the search_files argument grammar and result
// rules shared by Omnara searches, which run ripgrep, and machine searches,
// which omnarad runs in Go with Search.
package filesearch

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/textutil"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const (
	LineBytes          = 256
	entryOverheadBytes = 64
)

// ErrResultLimit stops a search once its result is full.
var ErrResultLimit = errors.New("search result limit reached")

// Options are the parsed ripgrep-style search_files args.
type Options struct {
	Patterns   []string
	Fixed      bool
	IgnoreCase bool
	Word       bool
	Line       bool
	Invert     bool
	Multiline  bool
	// Mode is "" for matching lines, "-l" for paths, or "-c" for counts.
	Mode   string
	Before int
	After  int
}

// ParseArgs validates search_files args and returns their options.
func ParseArgs(args []string) (Options, error) {
	var options Options
	patternBytes := 0
	if len(args) == 0 || len(args) > toolcatalog.SearchMaxArgs {
		return options, errors.New("args must contain between 1 and 64 arguments")
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-i":
			options.IgnoreCase = true
		case "-F":
			options.Fixed = true
		case "-w":
			options.Word = true
		case "-x":
			options.Line = true
		case "-v":
			options.Invert = true
		case "-U":
			options.Multiline = true
		case "-l", "-c":
			if options.Mode != "" && options.Mode != arg {
				return options, errors.New("-l and -c cannot be combined")
			}
			options.Mode = arg
		case "-e", "-A", "-B", "-C":
			i++
			if i >= len(args) {
				return options, fmt.Errorf("%s requires a value", arg)
			}
			value := args[i]
			if arg == "-e" {
				patternBytes += len(value)
				if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
					return options, errors.New("patterns must be nonempty UTF-8 text without NUL bytes")
				}
				options.Patterns = append(options.Patterns, value)
				continue
			}
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || n > toolcatalog.SearchMaxContextLines {
				return options, errors.New("context must be between 0 and 5 lines")
			}
			if arg != "-A" {
				options.Before = n
			}
			if arg != "-B" {
				options.After = n
			}
		default:
			return options, fmt.Errorf("unsupported search argument %q; supply patterns with -e", arg)
		}
	}
	if len(options.Patterns) == 0 || patternBytes > toolcatalog.SearchMaxPatternBytes {
		return options, errors.New("supply -e patterns totaling 1–1024 UTF-8 bytes")
	}
	return options, nil
}

type Line struct {
	Path       string `json:"path"`
	LineNumber int    `json:"line_number"`
	EndLine    int    `json:"end_line"`
	Text       string `json:"text"`
	IsMatch    bool   `json:"is_match"`
}

type File struct {
	Path  string  `json:"path"`
	Count *uint64 `json:"count,omitempty"`
}

type Result struct {
	Lines            []Line `json:"lines,omitempty"`
	Files            []File `json:"files,omitempty"`
	MatchCount       int    `json:"match_count"`
	Truncated        bool   `json:"truncated"`
	IncompleteReason string `json:"incomplete_reason,omitempty"`
}

// Collector builds a search result within the match limit and the one-page
// response budget.
type Collector struct {
	Mode       string
	Limit      int
	OffsetLine int
	Result     Result
	used       int
}

// AddFile records a matching path, with its count in -c mode.
func (c *Collector) AddFile(path string, count *uint64) error {
	if c.Result.MatchCount >= c.Limit || c.used+len(path)+entryOverheadBytes > toolcatalog.FilePageBytes {
		return ErrResultLimit
	}
	c.Result.Files = append(c.Result.Files, File{Path: path, Count: count})
	c.Result.MatchCount++
	c.used += len(path) + entryOverheadBytes
	return nil
}

// AddLine records a matching or context line; text may span lines for
// multiline matches, and matchStart and matchEnd locate the first match in it.
func (c *Collector) AddLine(path string, lineNumber int, text string, matchStart, matchEnd int, isMatch bool) error {
	if lineNumber < c.OffsetLine {
		return nil
	}
	if isMatch && c.Result.MatchCount >= c.Limit {
		return ErrResultLimit
	}
	line := Line{Path: path, LineNumber: lineNumber, EndLine: lineNumber + strings.Count(text, "\n"),
		Text: snippet(text, matchStart, matchEnd), IsMatch: isMatch}
	size := len(line.Path) + len(line.Text) + entryOverheadBytes
	if c.used+size > toolcatalog.FilePageBytes {
		return ErrResultLimit
	}
	c.Result.Lines = append(c.Result.Lines, line)
	c.used += size
	if isMatch {
		c.Result.MatchCount++
	}
	return nil
}

// MarkIncomplete flags the result as truncated, keeping the first reason.
func (c *Collector) MarkIncomplete(reason string) {
	c.Result.Truncated = true
	if c.Result.IncompleteReason == "" {
		c.Result.IncompleteReason = reason
	}
}

// Finish turns the error that stopped a search into truncation when the
// search stopped at a limit.
func (c *Collector) Finish(err error) (Result, error) {
	switch {
	case errors.Is(err, ErrResultLimit):
		c.MarkIncomplete("search result limit reached; narrow the search")
	case errors.Is(err, context.DeadlineExceeded):
		c.MarkIncomplete("search resource limit reached; narrow the search")
	case err != nil:
		return Result{}, err
	}
	return c.Result, nil
}

func snippet(text string, matchStart, matchEnd int) string {
	matchStart, matchEnd = min(matchStart, len(text)), min(matchEnd, len(text))
	if !utf8.ValidString(text) {
		matchStart, matchEnd = len(strings.ToValidUTF8(text[:matchStart], "�")),
			len(strings.ToValidUTF8(text[:matchEnd], "�"))
		text = strings.ToValidUTF8(text, "�")
	}
	if len(text) > LineBytes {
		const marker = "…"
		limit := LineBytes - 2*len(marker)
		for matchStart > 0 && matchStart < len(text) && !utf8.RuneStart(text[matchStart]) {
			matchStart--
		}
		matchLength := min(matchEnd-matchStart, limit)
		start := max(0, matchStart-(limit-matchLength)/2)
		start = min(start, len(text)-limit)
		for start < matchStart && !utf8.RuneStart(text[start]) {
			start++
		}
		text = text[start:]
		snippet := textutil.TruncateBytes(text, limit)
		if len(snippet) < len(text) {
			snippet += marker
		}
		if start > 0 {
			snippet = marker + snippet
		}
		return snippet
	}
	return text
}
