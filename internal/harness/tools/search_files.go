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
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/fileread"
	"github.com/omnara-ai/omnara/internal/filesearch"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const searchInitialBufferBytes = 64 * 1024
const searchEventBytes = 2 * 1024 * 1024
const searchStoreBatchSize = 32

var searchGlobEscaper = strings.NewReplacer("[", "\\[", "]", "\\]", "{", "\\{", "}", "\\}", " ", "\\ ")

type searchFilesRequest struct {
	Path       string          `json:"path"`
	Args       []string        `json:"args"`
	Limit      int             `json:"limit,omitempty"`
	OffsetLine *int            `json:"offset_line,omitempty"`
	MachineID  json.RawMessage `json:"machine_id,omitempty"`
	mode       string
	matcher    *regexp.Regexp
	machineID  uuid.UUID
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
	if input.machineID, err = resolveOptionalMachineID(input.MachineID); err != nil {
		return input, err
	}
	memory := strings.HasPrefix(input.Path, memorystore.Root+"/")
	if onMachine(input.Path, input.machineID) {
		if err := validateMachineGlob(input.Path); err != nil {
			return input, err
		}
		// Machines search with Go regular expressions rather than ripgrep.
		if options, err := filesearch.ParseArgs(input.Args); err == nil {
			if _, err := filesearch.Compile(options); err != nil {
				return input, fmt.Errorf("invalid search pattern: %w", err)
			}
		}
	} else if memory {
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
		if strings.HasPrefix(input.Path, toolcatalog.ArtifactVFSRoot+"/") {
			return input, err
		}
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

func machineSearchFiles(raw json.RawMessage) (machineFileCall, bool, error) {
	input, err := resolveSearchFilesRequest(raw)
	if err != nil || !onMachine(input.Path, input.machineID) {
		return machineFileCall{}, false, err
	}
	operation := processcmd.FileOperation{
		Path: input.Path, Args: input.Args, Limit: input.Limit, Paging: fileread.Paging{OffsetLine: input.OffsetLine},
	}
	return machineFileCall{machineID: input.machineID, kind: processcmd.KindFileSearch, operation: operation}, true, nil
}

func parseSearchArgs(args []string) (string, error) {
	options, err := filesearch.ParseArgs(args)
	return options.Mode, err
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
	searchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output := newSearchOutput(input)
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
			err := output.search(searchCtx, searchSource{stores: stores})
			closeStores()
			return err
		}
		err = call.Executor.Store.Memories().VisitSearchStores(searchCtx, call.Turn.ProjectID, call.Turn.AgentID,
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
		content, _, loadErr := loadArtifactContent(searchCtx, call, id)
		if loadErr != nil {
			if searchCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
				return nil, errors.New("artifact loading timed out; retry")
			}
			return nil, loadErr
		}
		searched = true
		err = output.search(searchCtx, searchSource{path: input.Path, content: content})
	}
	if err == nil && !searched {
		err = output.search(searchCtx, searchSource{})
	}
	result, err := output.Finish(err)
	if err != nil {
		return nil, err
	}
	return completeFileTool(result)
}

type searchResult = filesearch.Result

type searchFileResult = filesearch.File

type searchOutput struct {
	input searchFilesRequest
	filesearch.Collector
}

func newSearchOutput(input searchFilesRequest) *searchOutput {
	return &searchOutput{input: input, Collector: filesearch.Collector{
		Mode: input.mode, Limit: input.Limit, OffsetLine: *input.OffsetLine,
	}}
}

func (output *searchOutput) search(ctx context.Context, source searchSource) error {
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var view *os.File
	if len(source.stores) > 0 {
		var err error
		view, err = newSearchView(source.stores)
		if err != nil {
			return fileToolUnavailable(ctx, fileSearchUnavailableMessage, err)
		}
		defer func() {
			_ = view.Close()
			_ = os.RemoveAll(view.Name())
		}()
	}
	command, err := newSearchCommand(commandCtx, output.input, source, view)
	if err != nil {
		return fileToolUnavailable(ctx, fileSearchUnavailableMessage, err)
	}
	command.WaitDelay = time.Second
	command.Env = []string{"LANG=C.UTF-8"}
	var stderr boundedStderrBuffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fileToolUnavailable(ctx, fileSearchUnavailableMessage, err)
	}
	if err := command.Start(); err != nil {
		return fileToolUnavailable(ctx, fileSearchUnavailableMessage, err)
	}
	stream := searchStream{output: output, source: source}
	readErr := stream.read(stdout)
	if readErr != nil {
		cancel()
		_ = stdout.Close()
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if readErr != nil {
		return readErr
	}
	if sandboxSetupFailed(waitErr, stderr.data) {
		return fileToolUnavailable(ctx, fileSearchUnavailableMessage, fmt.Errorf("%s: %w", stderr.data, waitErr))
	}
	var exit *exec.ExitError
	if waitErr != nil && !(errors.As(waitErr, &exit) && exit.ExitCode() == 1 && len(stderr.data) == 0) {
		return fmt.Errorf("ripgrep: %s (%w)", string(stderr.data), waitErr)
	}
	return nil
}

func newSearchView(stores []searchStore) (_ *os.File, err error) {
	dir, err := os.MkdirTemp("", "omnara-search-")
	if err != nil {
		return nil, fmt.Errorf("create search view: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := os.Mkdir(filepath.Join(dir, "memory"), 0700); err != nil {
		return nil, err
	}
	for i, store := range stores {
		if err := os.Symlink("/proc/self/fd/"+strconv.Itoa(i+4), filepath.Join(dir, "memory", store.name)); err != nil {
			return nil, err
		}
	}
	return os.Open(dir)
}

func newSearchCommand(
	ctx context.Context, input searchFilesRequest, source searchSource, view *os.File,
) (*exec.Cmd, error) {
	args := []string{"--no-config", "--no-mmap", "--threads", "1", "--color", "never",
		"--engine", "default", "--regex-size-limit", "10M", "--dfa-size-limit", "10M", "--encoding", "none"}
	args = append(args, input.Args...)
	if input.mode == "" {
		args = append(args, "--json")
	} else {
		args = append(args, "--with-filename")
	}
	var roots []*os.File
	if len(source.stores) == 0 {
		args = append(args, "--", "-")
	} else {
		args = append(args, "--hidden", "--no-ignore",
			"--max-filesize", strconv.Itoa(daemonprotocol.MaxFileTransferBytes))
		args = append(args, memorySearchArgs(input.Path)...)
		args = append(args, "--")
		roots = append(roots, view)
		for _, store := range source.stores {
			operand := "memory/" + store.name
			if !strings.ContainsAny(input.Path, "*?") {
				operand = strings.TrimPrefix(input.Path, "/")
			}
			args = append(args, operand)
			roots = append(roots, store.root)
		}
	}
	binary, err := exec.LookPath("rg")
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, "omnara-sandbox",
		append([]string{strconv.Itoa(len(source.stores)), binary}, args...)...)
	command.ExtraFiles = roots
	if len(source.stores) == 0 {
		command.Stdin = bytes.NewReader(source.content)
	}
	return command, nil
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

func (s *searchStream) read(stdout io.Reader) error {
	reader := bufio.NewReaderSize(stdout, searchInitialBufferBytes)
	skipping := false
	for {
		data, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) {
			if reader.Size() < searchEventBytes {
				reader = bufio.NewReaderSize(io.MultiReader(bytes.NewReader(data), stdout), searchEventBytes)
				continue
			}
			skipping = true
			s.output.Result.Truncated = true
			s.output.Result.IncompleteReason = "oversized search events were skipped; results are incomplete"
			continue
		}
		if !skipping && len(data) > 0 {
			if err := s.consume(bytes.TrimSuffix(data, []byte{'\n'})); err != nil {
				return err
			}
		}
		skipping = false
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func (s *searchStream) resolvePath(name string) (string, bool, error) {
	if len(s.source.stores) == 0 {
		if name != "<stdin>" {
			return "", false, errors.New("unexpected ripgrep file")
		}
		return s.source.path, true, nil
	}
	path := "/" + strings.TrimPrefix(name, "./")
	storeName, _, err := memorystore.ParsePath(path)
	if err != nil {
		return "", false, err
	}
	if !slices.ContainsFunc(s.source.stores, func(store searchStore) bool { return store.name == storeName }) {
		return "", false, errors.New("unexpected ripgrep store")
	}
	return path, s.output.input.matcher.MatchString(path), nil
}

func memorySearchArgs(pattern string) []string {
	parts := strings.Split(pattern, "/")
	for i, part := range parts {
		if part != "**" {
			part = strings.ReplaceAll(part, "?", "*")
			for strings.Contains(part, "**") {
				part = strings.ReplaceAll(part, "**", "*")
			}
		}
		parts[i] = searchGlobEscaper.Replace(part)
	}
	args := []string{"--glob", strings.Join(parts, "/")}
	if !strings.ContainsAny(pattern, "*?") {
		return args
	}
	for i := 2; i < len(parts); i++ {
		if parts[i] == "**" {
			break
		}
		if i == 2 || parts[i] == "*" && i < len(parts)-1 {
			continue
		}
		parent := strings.Join(parts[:i], "/")
		args = append(args, "--glob", "!"+parent+"/*/")
		if i < len(parts)-1 {
			args = append(args, "--glob", strings.Join(parts[:i+1], "/")+"/")
		}
	}
	return args
}

func (s *searchStream) consume(data []byte) error {
	output := s.output
	if output.input.mode != "" {
		name := string(data)
		var count *uint64
		if output.input.mode == "-c" {
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
		return output.AddFile(path, count)
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
			output.MarkIncomplete("binary data detected; results may be incomplete")
		}
		return nil
	}
	if event.Type != "match" && event.Type != "context" {
		return errors.New("unexpected ripgrep event")
	}
	eventData := event.Data
	text := eventData.Lines.Text
	if eventData.Lines.Bytes != nil {
		text = string(eventData.Lines.Bytes)
	}
	var matchStart, matchEnd int
	if len(eventData.Submatches) > 0 {
		matchStart, matchEnd = eventData.Submatches[0].Start, eventData.Submatches[0].End
	}
	return output.AddLine(path, eventData.LineNumber, strings.TrimSuffix(text, "\n"),
		matchStart, matchEnd, event.Type == "match")
}
