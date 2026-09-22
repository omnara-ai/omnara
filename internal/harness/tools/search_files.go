package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/textutil"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const searchLineBytes = 256
const searchInitialBufferBytes = 64 * 1024
const searchEventBytes = 2 * 1024 * 1024
const searchStoreBatchSize = 32

var errSearchResultLimit = errors.New("search result limit reached")
var searchGlobEscaper = strings.NewReplacer("[", "\\[", "]", "\\]", "{", "\\{", "}", "\\}", " ", "\\ ")

type searchFilesRequest struct {
	Path       string   `json:"path"`
	Args       []string `json:"args"`
	Limit      int      `json:"limit,omitempty"`
	OffsetLine *int     `json:"offset_line,omitempty"`
	mode       string
	matcher    *regexp.Regexp
}

func validateSearchFilesInput(raw json.RawMessage) error {
	_, err := resolveSearchFilesRequest(raw)
	return err
}

func resolveSearchFilesRequest(raw json.RawMessage) (searchFilesRequest, error) {
	var input searchFilesRequest
	if err := decodeSingleStrictJSON(raw, &input, "search_files request"); err != nil {
		return input, err
	}
	var err error
	memory := strings.HasPrefix(input.Path, memorystore.Root+"/")
	if memory {
		input.matcher, err = storage.CompileFilePattern(input.Path)
		if err != nil {
			return input, err
		}
		if !strings.ContainsAny(input.Path, "*?") {
			if _, _, err := memorystore.ParsePath(input.Path); err != nil {
				return input, err
			}
		}
	} else if _, err := resolveArtifactPath(input.Path); err != nil {
		return input, errors.New("path must be a /memory/ path or glob, or one /artifacts/<artifact_id> path")
	}
	input.mode, err = parseSearchArgs(input.Args)
	if err != nil {
		return input, err
	}
	if input.Limit == 0 {
		input.Limit = toolcatalog.SearchDefaultMatches
	}
	if input.Limit < 1 || input.Limit > toolcatalog.SearchMaxMatches {
		return input, errors.New("limit must be between 1 and 100")
	}
	if input.OffsetLine != nil && (*input.OffsetLine < 1 ||
		input.mode != "" || strings.ContainsAny(input.Path, "*?")) {
		return input, errors.New(
			"offset_line must be positive and is only supported for exact-file content searches",
		)
	}
	if input.OffsetLine == nil {
		value := 1
		input.OffsetLine = &value
	}
	return input, nil
}

func parseSearchArgs(args []string) (string, error) {
	var mode string
	patterns, patternBytes := 0, 0
	if len(args) == 0 || len(args) > 64 {
		return mode, errors.New("args must contain between 1 and 64 arguments")
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-i", "-F", "-w", "-x", "-v", "-U":
			continue
		case "-l", "-c":
			if mode != "" && mode != arg {
				return mode, errors.New("-l and -c cannot be combined")
			}
			mode = arg
		case "-e", "-A", "-B", "-C":
			i++
			if i >= len(args) {
				return mode, fmt.Errorf("%s requires a value", arg)
			}
			value := args[i]
			if arg == "-e" {
				patterns++
				patternBytes += len(value)
				if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
					return mode, errors.New("patterns must be nonempty UTF-8 text without NUL bytes")
				}
				continue
			}
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || n > toolcatalog.SearchMaxContextLines {
				return mode, errors.New("context must be between 0 and 5 lines")
			}
		default:
			return mode, fmt.Errorf("unsupported search argument %q; supply patterns with -e", arg)
		}
	}
	if patterns == 0 || patternBytes > toolcatalog.SearchMaxPatternBytes {
		return mode, errors.New("supply -e patterns totaling 1–1024 UTF-8 bytes")
	}
	return mode, nil
}

type searchSource struct {
	path    string
	stores  []searchStore
	content []byte
}

type searchStore struct {
	name string
	root *os.File
}

func runSearchFilesAsync(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	input, err := resolveSearchFilesRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output := &searchOutput{input: input}
	searched := false
	if strings.HasPrefix(input.Path, memorystore.Root+"/") {
		var stores []searchStore
		closeStores := func() {
			for _, store := range stores {
				_ = store.root.Close()
			}
			stores = nil
		}
		defer closeStores()
		searchStores := func() error {
			err := output.search(ctx, searchSource{stores: stores})
			closeStores()
			return err
		}
		err = call.Executor.Store.Memories().VisitSearchStores(ctx, call.Turn.ProjectID, call.Turn.AgentID,
			input.Path, func(store memorystore.SearchStore) error {
				root, err := store.Root.Open(".")
				if err != nil {
					return err
				}
				stores = append(stores, searchStore{name: store.Name, root: root})
				searched = true
				if len(stores) == searchStoreBatchSize {
					return searchStores()
				}
				return nil
			})
		if err == nil && len(stores) > 0 {
			err = searchStores()
		}
		if err == nil && !searched && !strings.ContainsAny(input.Path, "*?") {
			return nil, storeerr.ErrNotFound
		}
	} else {
		id, _ := resolveArtifactPath(input.Path)
		content, _, loadErr := loadArtifactContent(ctx, call, id)
		if loadErr != nil {
			return nil, loadErr
		}
		searched = true
		err = output.search(ctx, searchSource{path: input.Path, content: content})
	}
	if err == nil && !searched {
		err = output.search(ctx, searchSource{})
	}
	if errors.Is(err, errSearchResultLimit) {
		output.result.Truncated = true
		if output.result.IncompleteReason == "" {
			output.result.IncompleteReason = "search result limit reached; narrow the search"
		}
	} else if errors.Is(err, context.DeadlineExceeded) {
		output.result.Truncated = true
		if output.result.IncompleteReason == "" {
			output.result.IncompleteReason = "search resource limit reached; narrow the search"
		}
	} else if err != nil {
		return nil, err
	}
	return completeFileTool(output.result)
}

type searchLine struct {
	Path       string `json:"path"`
	LineNumber int    `json:"line_number"`
	EndLine    int    `json:"end_line"`
	Text       string `json:"text"`
	IsMatch    bool   `json:"is_match"`
}

type searchFileResult struct {
	Path  string  `json:"path"`
	Count *uint64 `json:"count,omitempty"`
}

type searchResult struct {
	Lines            []searchLine       `json:"lines,omitempty"`
	Files            []searchFileResult `json:"files,omitempty"`
	MatchCount       int                `json:"match_count"`
	Truncated        bool               `json:"truncated"`
	IncompleteReason string             `json:"incomplete_reason,omitempty"`
}

type searchOutput struct {
	input  searchFilesRequest
	result searchResult
	used   int
}

func (p *searchOutput) search(ctx context.Context, source searchSource) error {
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := []string{"--no-config", "--no-mmap", "--threads", "1", "--color", "never",
		"--engine", "default", "--regex-size-limit", "10M", "--dfa-size-limit", "10M", "--encoding", "none"}
	args = append(args, p.input.Args...)
	if p.input.mode == "" {
		args = append(args, "--json")
	} else {
		args = append(args, "--with-filename")
	}
	command := exec.CommandContext(commandCtx, "rg")
	if len(source.stores) == 0 {
		command.Stdin = bytes.NewReader(source.content)
		command.Args = append(command.Args, append(args, "--", "-")...)
	} else {
		args = append(args, "--hidden", "--no-ignore",
			"--max-filesize", strconv.Itoa(daemonprotocol.MaxFileTransferBytes))
		var operands []string
		var roots []*os.File
		for i, store := range source.stores {
			fd := strconv.Itoa(i + 3)
			operand := fd + "/"
			if !strings.ContainsAny(p.input.Path, "*?") {
				_, name, _ := memorystore.ParsePath(p.input.Path)
				operand += name
			} else {
				for _, glob := range memorySearchGlobs(p.input.Path, store.name) {
					args = append(args, "--glob", "/"+fd+glob)
				}
			}
			operands = append(operands, operand)
			roots = append(roots, store.root)
		}
		args = append(append(args, "--"), operands...)
		var err error
		command, err = newFileExecCommand(commandCtx, "rg", roots, args...)
		if err != nil {
			return err
		}
	}
	command.WaitDelay = time.Second
	command.Env = []string{"LANG=C.UTF-8"}
	var stderr boundedStderrBuffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start ripgrep: %w", err)
	}
	reader := bufio.NewReaderSize(stdout, searchInitialBufferBytes)
	stream := searchStream{output: p, source: source}
	skipping := false
	for {
		data, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) {
			if reader.Size() < searchEventBytes {
				reader = bufio.NewReaderSize(io.MultiReader(bytes.NewReader(data), stdout), searchEventBytes)
				continue
			}
			skipping = true
			p.result.Truncated = true
			p.result.IncompleteReason = "oversized search events were skipped; results are incomplete"
			continue
		}
		if !skipping && len(data) > 0 {
			if err = stream.consume(bytes.TrimSuffix(data, []byte{'\n'})); err != nil {
				break
			}
		}
		skipping = false
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				err = readErr
			}
			break
		}
	}
	if err != nil {
		cancel()
		_ = stdout.Close()
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	var exit *exec.ExitError
	if waitErr != nil && !(errors.As(waitErr, &exit) && exit.ExitCode() == 1 && len(stderr.data) == 0) {
		return fmt.Errorf("ripgrep: %s (%w)", string(stderr.data), waitErr)
	}
	return nil
}

type searchEvent struct {
	Type string `json:"type"`
	Data struct {
		Path  struct{ Text string } `json:"path"`
		Lines struct {
			Text  string `json:"text"`
			Bytes []byte `json:"bytes"`
		} `json:"lines"`
		LineNumber   int    `json:"line_number"`
		BinaryOffset *int64 `json:"binary_offset"`
		Submatches   []struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"submatches"`
	} `json:"data"`
}

type searchStream struct {
	output *searchOutput
	source searchSource
}

func (s *searchStream) resolvePath(name string) (string, bool, error) {
	if len(s.source.stores) == 0 {
		if name != "<stdin>" {
			return "", false, errors.New("unexpected ripgrep file")
		}
		return s.source.path, true, nil
	}
	name = strings.TrimPrefix(name, "./")
	fd, name, _ := strings.Cut(name, "/")
	n, err := strconv.Atoi(fd)
	if err != nil || n < 3 || n-3 >= len(s.source.stores) {
		return "", false, errors.New("unexpected ripgrep store")
	}
	if err := memorystore.ValidatePath(name); err != nil {
		return "", false, err
	}
	path := memorystore.Root + "/" + s.source.stores[n-3].name + "/" + name
	return path, s.output.input.matcher.MatchString(path), nil
}

func memorySearchGlobs(pattern, store string) []string {
	parts := strings.Split(strings.TrimPrefix(pattern, memorystore.Root+"/"), "/")
	var patterns []string
	if len(parts) > 0 && parts[0] == "**" {
		patterns = append(patterns, strings.Join(parts, "/"))
		for len(parts) > 0 && parts[0] == "**" {
			parts = parts[1:]
		}
	}
	if len(parts) > 0 {
		matcher, _ := storage.CompileFilePattern("/" + parts[0])
		if matcher.MatchString("/"+store) && len(parts) > 1 {
			patterns = append(patterns, strings.Join(parts[1:], "/"))
		}
	}
	for i, pattern := range patterns {
		parts := strings.Split(pattern, "/")
		for j, part := range parts {
			if part != "**" {
				part = strings.ReplaceAll(part, "?", "*")
				for strings.Contains(part, "**") {
					part = strings.ReplaceAll(part, "**", "*")
				}
			}
			parts[j] = searchGlobEscaper.Replace(part)
		}
		patterns[i] = "/" + strings.Join(parts, "/")
	}
	return patterns
}

func (s *searchStream) consume(data []byte) error {
	p := s.output
	if p.input.mode != "" {
		name := string(data)
		var count *uint64
		if p.input.mode == "-c" {
			var value string
			var ok bool
			index := strings.LastIndexByte(name, ':')
			if index >= 0 {
				name, value, ok = name[:index], name[index+1:], true
			}
			n, err := strconv.ParseUint(value, 10, 64)
			if !ok || err != nil {
				return errors.New("invalid ripgrep count")
			}
			count = &n
		}
		path, ok, err := s.resolvePath(name)
		if err != nil || !ok {
			return err
		}
		if p.result.MatchCount >= p.input.Limit || p.used+len(path)+64 > toolcatalog.FilePageBytes {
			return errSearchResultLimit
		}
		p.result.Files = append(p.result.Files, searchFileResult{Path: path, Count: count})
		p.result.MatchCount++
		p.used += len(path) + 64
		return nil
	}
	var event searchEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("decode ripgrep output: %w", err)
	}
	if event.Type == "summary" {
		return nil
	}
	path, ok, err := s.resolvePath(event.Data.Path.Text)
	if err != nil || !ok {
		return err
	}
	if event.Type == "begin" {
		return nil
	}
	if event.Type == "end" {
		if event.Data.BinaryOffset != nil {
			p.result.Truncated = true
			if p.result.IncompleteReason == "" {
				p.result.IncompleteReason = "binary data detected; results may be incomplete"
			}
		}
		return nil
	}
	if event.Type != "match" && event.Type != "context" {
		return errors.New("unexpected ripgrep event")
	}
	d := event.Data
	if d.LineNumber < *p.input.OffsetLine {
		return nil
	}
	if event.Type == "match" && p.result.MatchCount >= p.input.Limit {
		return errSearchResultLimit
	}
	text := d.Lines.Text
	if d.Lines.Bytes != nil {
		text = string(d.Lines.Bytes)
	}
	text = strings.TrimSuffix(text, "\n")
	var matchStart, matchEnd int
	if len(d.Submatches) > 0 {
		matchStart, matchEnd = d.Submatches[0].Start, d.Submatches[0].End
	}
	line := searchLine{Path: path, LineNumber: d.LineNumber,
		EndLine: d.LineNumber + strings.Count(text, "\n"),
		Text:    searchSnippet(text, matchStart, matchEnd),
		IsMatch: event.Type == "match"}
	size := len(line.Path) + len(line.Text) + 64
	if p.used+size > toolcatalog.FilePageBytes {
		return errSearchResultLimit
	}
	p.result.Lines = append(p.result.Lines, line)
	p.used += size
	if line.IsMatch {
		p.result.MatchCount++
	}
	return nil
}

func searchSnippet(text string, matchStart, matchEnd int) string {
	matchStart, matchEnd = min(matchStart, len(text)), min(matchEnd, len(text))
	if !utf8.ValidString(text) {
		matchStart, matchEnd = len(strings.ToValidUTF8(text[:matchStart], "�")),
			len(strings.ToValidUTF8(text[:matchEnd], "�"))
		text = strings.ToValidUTF8(text, "�")
	}
	if len(text) > searchLineBytes {
		const marker = "…"
		limit := searchLineBytes - 2*len(marker)
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
