package agentconfig

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	kjsonschema "github.com/kaptinlin/jsonschema"
	"gopkg.in/yaml.v3"
)

type Issue struct {
	Path    string
	Message string
	Line    int
	Column  int
}

type issueError struct {
	issue Issue
	cause error
}

func NewIssue(path string, err error) error {
	return issueError{issue: Issue{Path: path, Message: err.Error()}, cause: err}
}

func issuef(path string, format string, args ...any) error {
	return NewIssue(path, fmt.Errorf(format, args...))
}

func (err issueError) Error() string {
	if err.issue.Path == "" {
		return err.issue.Message
	}
	return DisplayPath(err.issue.Path) + ": " + err.issue.Message
}

func (err issueError) Unwrap() error {
	return err.cause
}

func issueAt(path string, err error) error {
	var wrapped issueError
	if errors.As(err, &wrapped) {
		wrapped.issue.Path = path + wrapped.issue.Path
		return wrapped
	}
	return NewIssue(path, err)
}

func issueOr(path string, err error) error {
	var wrapped issueError
	if errors.As(err, &wrapped) {
		return wrapped
	}
	return NewIssue(path, err)
}

type ValidationError struct {
	Issues []Issue
}

func (err *ValidationError) Error() string {
	messages := make([]string, 0, len(err.Issues))
	for _, issue := range err.Issues {
		messages = append(messages, issueError{issue: issue}.Error())
	}
	if len(messages) == 0 {
		return "agent config is invalid"
	}
	return "agent config is invalid: " + strings.Join(messages, "; ")
}

func validationErrorFrom(err error, root *yaml.Node) error {
	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		return validationErr
	}
	var wrapped issueError
	if errors.As(err, &wrapped) {
		return newValidationError([]Issue{wrapped.issue}, root)
	}
	return err
}

func newValidationError(issues []Issue, root *yaml.Node) *ValidationError {
	located := make([]Issue, 0, len(issues))
	for _, issue := range issues {
		if root != nil && issue.Line == 0 {
			issue.Line, issue.Column = yamlLocation(root, issue.Path)
		}
		located = append(located, issue)
	}
	sort.SliceStable(located, func(i, j int) bool {
		if located[i].Line != located[j].Line {
			return located[i].Line < located[j].Line
		}
		return located[i].Path < located[j].Path
	})
	return &ValidationError{Issues: located}
}

func jsonPointer(segments ...any) string {
	var builder strings.Builder
	for _, segment := range segments {
		builder.WriteByte('/')
		switch typed := segment.(type) {
		case int:
			builder.WriteString(strconv.Itoa(typed))
		case string:
			builder.WriteString(escapePointerSegment(typed))
		default:
			builder.WriteString(escapePointerSegment(fmt.Sprint(typed)))
		}
	}
	return builder.String()
}

func escapePointerSegment(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~", "~0"), "/", "~1")
}

func unescapePointerSegment(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
}

var identifierSegmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func DisplayPath(pointer string) string {
	var builder strings.Builder
	for _, segment := range pointerSegments(pointer) {
		switch {
		case isIndexSegment(segment):
			builder.WriteString("[" + segment + "]")
		case identifierSegmentPattern.MatchString(segment):
			if builder.Len() > 0 {
				builder.WriteByte('.')
			}
			builder.WriteString(segment)
		default:
			builder.WriteString("[" + strconv.Quote(segment) + "]")
		}
	}
	return builder.String()
}

func isIndexSegment(segment string) bool {
	if segment == "" {
		return false
	}
	for _, r := range segment {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func pointerSegments(pointer string) []string {
	if pointer == "" {
		return nil
	}
	raw := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	segments := make([]string, 0, len(raw))
	for _, segment := range raw {
		segments = append(segments, unescapePointerSegment(segment))
	}
	return segments
}

var yamlErrorLinePattern = regexp.MustCompile(`(?:^|\s)line (\d+):\s*`)

func yamlSyntaxIssue(err error) error {
	message := strings.TrimPrefix(err.Error(), "yaml: ")
	issue := issueError{issue: Issue{Message: message}, cause: err}
	match := yamlErrorLinePattern.FindStringSubmatchIndex(message)
	if match == nil {
		return issue
	}
	line, convErr := strconv.Atoi(message[match[2]:match[3]])
	if convErr != nil {
		return issue
	}
	issue.issue.Line = line
	issue.issue.Message = strings.TrimSpace(message[:match[0]] + " " + message[match[1]:])
	return issue
}

func yamlLocation(root *yaml.Node, pointer string) (int, int) {
	node := root
	for node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	line, column := node.Line, node.Column
	for _, segment := range pointerSegments(pointer) {
		node = resolveYAMLAlias(node)
		next, marker := yamlChild(node, segment)
		if next == nil {
			break
		}
		line, column = marker.Line, marker.Column
		node = next
	}
	return line, column
}

func yamlChild(node *yaml.Node, segment string) (*yaml.Node, *yaml.Node) {
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == segment {
				return node.Content[i+1], node.Content[i]
			}
		}
	case yaml.SequenceNode:
		index, err := strconv.Atoi(segment)
		if err != nil || index < 0 || index >= len(node.Content) {
			return nil, nil
		}
		return node.Content[index], node.Content[index]
	case yaml.DocumentNode, yaml.ScalarNode, yaml.AliasNode:
		return nil, nil
	}
	return nil, nil
}

func resolveYAMLAlias(node *yaml.Node) *yaml.Node {
	for node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	return node
}

var schemaContainerKeywords = map[string]bool{
	"$ref":                 true,
	"additionalProperties": true,
	"allOf":                true,
	"anyOf":                true,
	"contains":             true,
	"dependentSchemas":     true,
	"else":                 true,
	"items":                true,
	"oneOf":                true,
	"patternProperties":    true,
	"prefixItems":          true,
	"properties":           true,
	"propertyNames":        true,
	"then":                 true,
}

type schemaIssue struct {
	Issue
	keyword  string
	expected string
}

func schemaIssues(result *kjsonschema.EvaluationResult) []Issue {
	collected := collectSchemaIssues(result, "")
	issues := make([]Issue, 0, len(collected))
	for _, item := range collected {
		issues = append(issues, item.Issue)
	}
	sort.SliceStable(issues, func(i, j int) bool {
		return issues[i].Path < issues[j].Path
	})
	return issues
}

func collectSchemaIssues(result *kjsonschema.EvaluationResult, base string) []schemaIssue {
	location := base + result.InstanceLocation
	missing := map[string]bool{}
	var own []schemaIssue
	for keyword, evalErr := range result.Errors {
		switch evalErr.Code {
		case "missing_required_property", "missing_required_properties":
			for _, property := range quotedListParam(evalErr.Params) {
				missing[property] = true
				own = append(own, schemaIssue{
					Issue:   Issue{Path: location + jsonPointer(property), Message: "required field is missing"},
					keyword: keyword,
				})
			}
		case "false_schema_mismatch":
			own = append(own, schemaIssue{
				Issue:   Issue{Path: location, Message: "unknown field"},
				keyword: keyword,
			})
		default:
			if schemaContainerKeywords[keyword] {
				continue
			}
			own = append(own, schemaIssue{
				Issue:    Issue{Path: location, Message: lowerFirst(evalErr.Localize(nil))},
				keyword:  keyword,
				expected: stringParam(evalErr.Params, "expected"),
			})
		}
	}
	sort.SliceStable(own, func(i, j int) bool {
		if own[i].Path != own[j].Path {
			return own[i].Path < own[j].Path
		}
		return own[i].keyword < own[j].keyword
	})

	var branches [][]schemaIssue
	var nested []schemaIssue
	for _, detail := range result.Details {
		if detail.Valid || isConditionBranch(detail) {
			continue
		}
		if len(detail.InstanceLocation) > 0 && missing[strings.TrimPrefix(detail.InstanceLocation, "/")] {
			continue
		}
		if keyword, ok := alternativeKeyword(detail); ok {
			if _, failed := result.Errors[keyword]; failed {
				branches = append(branches, collectSchemaIssues(detail, location))
			}
			continue
		}
		nested = append(nested, collectSchemaIssues(detail, location)...)
	}
	nested = append(nested, mergeAlternatives(branches, location)...)
	if len(nested) > 0 {
		return append(own, nested...)
	}
	if len(own) == 0 {
		return []schemaIssue{{Issue: Issue{Path: location, Message: "value does not match the schema"}}}
	}
	return own
}

func isConditionBranch(result *kjsonschema.EvaluationResult) bool {
	return lastEvaluationSegment(result.EvaluationPath) == "if"
}

func alternativeKeyword(result *kjsonschema.EvaluationResult) (string, bool) {
	segments := strings.Split(strings.TrimPrefix(result.EvaluationPath, "/"), "/")
	if len(segments) < 2 {
		return "", false
	}
	parent := segments[len(segments)-2]
	if parent == "anyOf" || parent == "oneOf" {
		return parent, true
	}
	return "", false
}

func lastEvaluationSegment(path string) string {
	index := strings.LastIndex(path, "/")
	if index < 0 {
		return path
	}
	return path[index+1:]
}

func mergeAlternatives(branches [][]schemaIssue, location string) []schemaIssue {
	if len(branches) == 0 {
		return nil
	}
	if merged, ok := mergeTypeAlternatives(branches, location); ok {
		return []schemaIssue{merged}
	}
	var best []schemaIssue
	for _, branch := range branches {
		if isWrongKindBranch(branch, location) {
			continue
		}
		if best == nil || len(branch) < len(best) {
			best = branch
		}
	}
	return best
}

func isWrongKindBranch(branch []schemaIssue, location string) bool {
	return len(branch) == 1 && branch[0].keyword == "type" && branch[0].Path == location && branch[0].expected != ""
}

func mergeTypeAlternatives(branches [][]schemaIssue, location string) (schemaIssue, bool) {
	expected := make([]string, 0, len(branches))
	received := ""
	for _, branch := range branches {
		if !isWrongKindBranch(branch, location) {
			return schemaIssue{}, false
		}
		expected = append(expected, branch[0].expected)
		received = branch[0].Message
	}
	message := received
	if index := strings.Index(received, " but should be "); index >= 0 {
		message = received[:index] + " but should be " + strings.Join(expected, " or ")
	}
	return schemaIssue{Issue: Issue{Path: location, Message: message}, keyword: "type"}, true
}

func quotedListParam(params map[string]any) []string {
	raw := stringParam(params, "property")
	if raw == "" {
		raw = stringParam(params, "properties")
	}
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ", ")
	properties := make([]string, 0, len(parts))
	for _, part := range parts {
		properties = append(properties, strings.Trim(part, "'"))
	}
	return properties
}

func stringParam(params map[string]any, key string) string {
	value, ok := params[key].(string)
	if !ok {
		return ""
	}
	return value
}

func lowerFirst(message string) string {
	if message == "" {
		return message
	}
	return strings.ToLower(message[:1]) + message[1:]
}
