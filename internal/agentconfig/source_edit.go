package agentconfig

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"gopkg.in/yaml.v3"
)

var (
	ErrInvalidEdit  = errors.New("invalid agent config edit")
	errEditTooLarge = errors.New("edited config too large")
)

const maxEditedSourceBytes = 1024 * 1024

type Edit struct {
	Type    string          `json:"type"`
	Path    string          `json:"path"`
	Value   json.RawMessage `json:"value,omitempty"`
	Find    string          `json:"find,omitempty"`
	Replace string          `json:"replace,omitempty"`
}

type EditResult struct {
	Source  string
	Edits   []string
	Changed bool
}

func ApplyEdits(format SourceFormat, source string, edits []Edit) (EditResult, error) {
	jsonSource, doc, err := sourceJSON(format, []byte(source))
	if err != nil {
		return EditResult{}, fmt.Errorf("%w: parse config source: %w", ErrInvalidEdit, err)
	}
	before, err := objectValue(jsonSource)
	if err != nil {
		return EditResult{}, err
	}
	after, err := objectValue(jsonSource)
	if err != nil {
		return EditResult{}, err
	}
	limit := max(maxEditedSourceBytes, len(source))
	budget := limit
	outcomes := make([]string, len(edits))
	for i, edit := range edits {
		outcome, err := apply(after, edit, &budget)
		if errors.Is(err, errEditTooLarge) {
			err = fmt.Errorf("%w: text replacements would add more than %d bytes", ErrInvalidEdit, limit)
		}
		if err != nil {
			return EditResult{}, fmt.Errorf("edit %d: %w", i, err)
		}
		outcomes[i] = outcome
	}
	if reflect.DeepEqual(before, after) {
		return EditResult{Source: source, Edits: outcomes}, nil
	}
	encoded, err := encodeEdited(format, []byte(source), doc, before, after)
	if err != nil {
		return EditResult{}, err
	}
	if len(encoded) > limit {
		return EditResult{}, fmt.Errorf("%w: edited config would exceed %d bytes", ErrInvalidEdit, limit)
	}
	return EditResult{Source: encoded, Edits: outcomes, Changed: true}, nil
}

func sourceValue(format SourceFormat, raw []byte) (map[string]any, error) {
	jsonSource, _, err := sourceJSON(format, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: parse config source: %w", ErrInvalidEdit, err)
	}
	return objectValue(jsonSource)
}

func objectValue(jsonSource []byte) (map[string]any, error) {
	value, err := jsonValue(jsonSource)
	if err != nil {
		return nil, fmt.Errorf("%w: parse config source: %w", ErrInvalidEdit, err)
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: config source must be an object", ErrInvalidEdit)
	}
	return root, nil
}

func jsonValue(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	err := decoder.Decode(&value)
	return value, err
}

func apply(root map[string]any, edit Edit, budget *int) (string, error) {
	keys, err := parsePointer(edit.Path)
	if err != nil {
		return "", err
	}
	path, found := walk(root, keys[:len(keys)-1], edit.Type == "set")
	if !found {
		return "path_not_found", nil
	}
	parent, key := path[len(path)-1], keys[len(keys)-1]
	switch edit.Type {
	case "remove":
		return remove(path, keys), nil
	case "set":
		value, err := editValue(edit.Value)
		if err != nil {
			return "", err
		}
		if !setChild(parent, key, value) {
			return "path_not_found", nil
		}
		return "applied", nil
	}
	target, found := child(parent, key)
	if !found && len(path) == 1 {
		switch edit.Type {
		case "add_item":
			target, found = []any{}, true
		case "remove_item":
			return "item_not_found", nil
		}
	}
	if !found {
		return "path_not_found", nil
	}
	var updated any
	var outcome string
	switch edit.Type {
	case "replace_text":
		updated, outcome, err = replaceText(target, edit, budget)
	case "add_item", "remove_item":
		updated, outcome, err = editItems(target, edit)
	default:
		return "", fmt.Errorf("%w: unknown edit type %q", ErrInvalidEdit, edit.Type)
	}
	if err == nil && outcome == "applied" {
		setChild(parent, key, updated)
	}
	return outcome, err
}

func remove(path []any, keys []string) string {
	key := keys[len(keys)-1]
	switch parent := path[len(path)-1].(type) {
	case map[string]any:
		if _, exists := parent[key]; !exists {
			return "path_not_found"
		}
		delete(parent, key)
		return "applied"
	case []any:
		i, ok := index(parent, key)
		if !ok {
			return "path_not_found"
		}
		setChild(path[len(path)-2], keys[len(keys)-2], slices.Delete(parent, i, i+1))
		return "applied"
	}
	return "path_not_found"
}

func replaceText(target any, edit Edit, budget *int) (any, string, error) {
	text, ok := target.(string)
	if !ok {
		return nil, "", fmt.Errorf("%w: %s is not text", ErrInvalidEdit, edit.Path)
	}
	if edit.Find == "" {
		return nil, "", fmt.Errorf("%w: find is required", ErrInvalidEdit)
	}
	count := strings.Count(text, edit.Find)
	if count == 0 {
		return nil, "text_not_found", nil
	}
	growth := count * (len(edit.Replace) - len(edit.Find))
	if growth > *budget {
		return nil, "", errEditTooLarge
	}
	*budget -= growth
	return strings.ReplaceAll(text, edit.Find, edit.Replace), "applied", nil
}

func editItems(target any, edit Edit) (any, string, error) {
	list, ok := target.([]any)
	if !ok {
		return nil, "", fmt.Errorf("%w: %s is not a list", ErrInvalidEdit, edit.Path)
	}
	value, err := editValue(edit.Value)
	if err != nil {
		return nil, "", err
	}
	match := func(item any) bool { return reflect.DeepEqual(item, value) }
	if edit.Type == "add_item" {
		if slices.ContainsFunc(list, match) {
			return nil, "item_exists", nil
		}
		return append(list, value), "applied", nil
	}
	if !slices.ContainsFunc(list, match) {
		return nil, "item_not_found", nil
	}
	return slices.DeleteFunc(list, match), "applied", nil
}

func editValue(raw json.RawMessage) (any, error) {
	canonical, err := jsoncanonical.Canonical(raw)
	if err == nil {
		var value any
		if value, err = jsonValue(canonical); err == nil {
			return value, nil
		}
	}
	return nil, fmt.Errorf("%w: invalid value: %w", ErrInvalidEdit, err)
}

func parsePointer(path string) ([]string, error) {
	pointer := jsontext.Pointer(path)
	if path == "" || path == "/" || !pointer.IsValid() {
		return nil, fmt.Errorf("%w: path must be a JSON Pointer such as /instruction", ErrInvalidEdit)
	}
	return slices.Collect(pointer.Tokens()), nil
}

func walk(root map[string]any, keys []string, createSection bool) ([]any, bool) {
	path := []any{root}
	for _, key := range keys {
		next, found := child(path[len(path)-1], key)
		if !found && createSection && len(path) == 1 && len(keys) == 1 {
			next, found = map[string]any{}, true
			root[key] = next
		}
		if !found {
			return nil, false
		}
		path = append(path, next)
	}
	return path, true
}

func child(node any, key string) (any, bool) {
	var value any
	switch typed := node.(type) {
	case map[string]any:
		value = typed[key]
	case []any:
		if i, ok := index(typed, key); ok {
			value = typed[i]
		}
	}
	return value, value != nil
}

func setChild(node any, key string, value any) bool {
	switch typed := node.(type) {
	case map[string]any:
		typed[key] = value
		return true
	case []any:
		i, ok := index(typed, key)
		if ok {
			typed[i] = value
		}
		return ok
	}
	return false
}

func index(list []any, key string) (int, bool) {
	i, err := strconv.Atoi(key)
	return i, err == nil && i >= 0 && i < len(list) && strconv.Itoa(i) == key
}

func encodeEdited(format SourceFormat, raw []byte, doc *yaml.Node, before, after map[string]any) (string, error) {
	if format == SourceFormatJSON {
		var out bytes.Buffer
		encoder := json.NewEncoder(&out)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(after); err != nil {
			return "", fmt.Errorf("%w: %w", ErrInvalidEdit, err)
		}
		return strings.TrimSuffix(out.String(), "\n"), nil
	}
	if spliced, ok := spliceYAML(raw, doc, before, after); ok && writesValue(spliced, after) {
		return spliced, nil
	}
	if node, err := yamlValueNode(after); err == nil {
		if canonical, err := encodeYAML(node); err == nil && writesValue(canonical, after) {
			return canonical, nil
		}
	}
	return "", fmt.Errorf("%w: the edited config can't be written as YAML", ErrInvalidEdit)
}

func writesValue(source string, want map[string]any) bool {
	got, err := sourceValue(SourceFormatYAML, []byte(source))
	return err == nil && reflect.DeepEqual(got, want)
}

type yamlSplice struct {
	lines [][]byte
	crlf  bool
	edits []yamlLineEdit
}

type yamlLineEdit struct {
	start, end int
	text       string
}

func spliceYAML(raw []byte, doc *yaml.Node, before, after map[string]any) (string, bool) {
	if doc == nil || len(doc.Content) != 1 {
		return "", false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode || root.Style&yaml.FlowStyle != 0 || len(root.Content) == 0 || sharesNodes(root) {
		return "", false
	}
	if !bytes.HasSuffix(raw, []byte("\n")) {
		raw = append(slices.Clone(raw), '\n')
	}
	lines := bytes.SplitAfter(raw, []byte("\n"))
	splice := yamlSplice{lines: lines[:len(lines)-1], crlf: bytes.Contains(raw, []byte("\r\n"))}
	if !splice.mapping(root, before, after, len(splice.lines)) {
		return "", false
	}
	return splice.String(), true
}

func (s *yamlSplice) mapping(node *yaml.Node, before, after map[string]any, end int) bool {
	seen := make(map[string]bool, len(node.Content)/2)
	last, indent := end, node.Content[0].Column-1
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		seen[key.Value] = true
		start, stop := key.Line-1, end
		if i+2 < len(node.Content) {
			stop = node.Content[i+2].Line - 1
		}
		stop = s.trim(start, stop, key.Column-1)
		last = stop
		current, kept := after[key.Value]
		if kept && reflect.DeepEqual(before[key.Value], current) {
			continue
		}
		previous, wasMapping := before[key.Value].(map[string]any)
		nested, isMapping := current.(map[string]any)
		if wasMapping && isMapping && len(nested) > 0 && value.Kind == yaml.MappingNode &&
			value.Style&yaml.FlowStyle == 0 && len(value.Content) > 0 && value.Line > key.Line {
			if !s.mapping(value, previous, nested, stop) {
				return false
			}
			continue
		}
		text := ""
		if kept {
			var ok bool
			if text, ok = s.literal(key, value, current); !ok {
				text, ok = s.render(key.Value, current, key.Column-1)
			}
			if !ok {
				return false
			}
		}
		s.edits = append(s.edits, yamlLineEdit{start: start, end: stop, text: text})
	}
	for _, key := range slices.Sorted(maps.Keys(after)) {
		if seen[key] {
			continue
		}
		text, ok := s.render(key, after[key], indent)
		if !ok {
			return false
		}
		s.edits = append(s.edits, yamlLineEdit{start: last, end: last, text: text})
	}
	return true
}

func (s *yamlSplice) trim(start, stop, indent int) int {
	for stop > start+1 {
		line := strings.TrimRight(string(s.lines[stop-1]), "\r\n")
		text := strings.TrimSpace(line)
		comment := strings.HasPrefix(text, "#") && len(line)-len(strings.TrimLeft(line, " \t")) <= indent
		if text != "" && text != "..." && !comment {
			break
		}
		stop--
	}
	return stop
}

func (s *yamlSplice) render(key string, value any, indent int) (string, bool) {
	node, err := yamlValueNode(map[string]any{key: value})
	if err != nil {
		return "", false
	}
	text, err := encodeYAML(node)
	if err != nil {
		return "", false
	}
	return s.indented(strings.Split(strings.TrimSuffix(text, "\n"), "\n"), strings.Repeat(" ", indent)), true
}

func (s *yamlSplice) literal(key, value *yaml.Node, current any) (string, bool) {
	text, ok := current.(string)
	line := []rune(string(s.lines[key.Line-1]))
	if !ok || !strings.Contains(text, "\n") || strings.ContainsFunc(text, breaksLiteral) ||
		value.Line != key.Line || value.Column < 2 || value.Column > len(line) {
		return "", false
	}
	parent := key.Column - 1
	indent := parent + 2
	if value.Style == yaml.LiteralStyle {
		indent = max(indent, s.contentIndent(value.Line))
	}
	body := strings.TrimRight(text, "\n")
	trailing := len(text) - len(body)
	chomp := ""
	switch {
	case trailing == 0:
		chomp = "-"
	case trailing > 1:
		chomp = "+"
	}
	lines := strings.Split(body, "\n")
	header := "|" + chomp
	if first := slices.IndexFunc(lines, func(l string) bool { return l != "" }); first >= 0 &&
		strings.HasPrefix(lines[first], " ") {
		header = "|" + strconv.Itoa(indent-parent) + chomp
	}
	block := s.indented(lines, strings.Repeat(" ", indent)) + strings.Repeat(s.newline(), max(0, trailing-1))
	return string(line[:value.Column-1]) + header + s.newline() + block, true
}

func (s *yamlSplice) contentIndent(from int) int {
	for _, raw := range s.lines[from:] {
		line := strings.TrimRight(string(raw), "\r\n")
		if strings.TrimSpace(line) != "" {
			return len(line) - len(strings.TrimLeft(line, " "))
		}
	}
	return 0
}

func (s *yamlSplice) indented(lines []string, pad string) string {
	var out strings.Builder
	for _, line := range lines {
		if line != "" {
			out.WriteString(pad + line)
		}
		out.WriteString(s.newline())
	}
	return out.String()
}

func (s *yamlSplice) newline() string {
	if s.crlf {
		return "\r\n"
	}
	return "\n"
}

func breaksLiteral(r rune) bool {
	return r != '\t' && r != '\n' &&
		(r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x2028 || r == 0x2029 || r == 0xfeff)
}

func (s *yamlSplice) String() string {
	slices.SortStableFunc(s.edits, func(a, b yamlLineEdit) int { return a.start - b.start })
	var out bytes.Buffer
	next := 0
	for _, edit := range s.edits {
		for _, line := range s.lines[next:edit.start] {
			out.Write(line)
		}
		out.WriteString(edit.text)
		next = max(next, edit.end)
	}
	for _, line := range s.lines[next:] {
		out.Write(line)
	}
	return out.String()
}

func sharesNodes(node *yaml.Node) bool {
	if node.Kind == yaml.AliasNode || node.Anchor != "" || node.Tag == "!!merge" {
		return true
	}
	return slices.ContainsFunc(node.Content, sharesNodes)
}

func yamlValueNode(value any) (*yaml.Node, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return sourceYAMLNode(decoder)
}

func encodeYAML(node *yaml.Node) (string, error) {
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(node); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return out.String(), nil
}
