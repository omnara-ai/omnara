// Package tooloutput bounds model-visible tool result content.
package tooloutput

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxInlineToolResultBytes = 51_200
	MaxStoredToolResultBytes = 512 * 1024
	PreviewBytes             = 8_192
	PreviewLineBytes         = 500

	ArtifactPageBytes        = 65_536
	ReadArtifactDefaultLines = 2_000
	SearchArtifactMaxMatches = 100
	SearchArtifactLineBytes  = 500
	MaxReadableArtifactBytes = 16 * 1024 * 1024

	TextContentType       = "text/plain; charset=utf-8"
	StructuredContentType = "application/json"
)

var ErrCannotBound = errors.New("tool result cannot be bounded within the inline limit")

type Artifact struct {
	RawID       string
	PublicID    string
	ContentType string
	SizeBytes   int64
	LineCount   int
}

type Persist func(partIndex int, contentType string, content []byte, lineCount int) (Artifact, error)

type candidate struct {
	index   int
	kind    string
	content []byte
}

// RewriteOversized replaces the fewest useful text and structured-data
// payloads, largest first, with bounded previews and artifact references. If
// individual replacements cannot converge, it stores all candidates together
// in original order. Every decision uses the complete serialized content-parts
// size, including JSON framing and escaping.
func RewriteOversized(
	parts json.RawMessage,
	succeeded bool,
	persist Persist,
) (json.RawMessage, error) {
	if len(parts) <= MaxInlineToolResultBytes {
		return parts, nil
	}
	if persist == nil {
		return nil, errors.New("tool result artifact persistence is required")
	}
	decoded, candidates, err := decodeCandidates(parts)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, ErrCannotBound
	}
	outcome := "The tool call succeeded but its output was truncated"
	if !succeeded {
		outcome = "The tool call failed and its output was truncated"
	}
	budget := scaledPreviewBudget(len(candidates))
	selected, preflight, err := selectIndividualCandidates(decoded, candidates, outcome, budget)
	if err != nil {
		return nil, err
	}
	if len(preflight) <= MaxInlineToolResultBytes {
		artifacts := make(map[int]Artifact, len(selected))
		for _, item := range candidates {
			if !selected[item.index] {
				continue
			}
			artifact, err := persistCandidate(persist, item)
			if err != nil {
				return nil, err
			}
			artifacts[item.index] = artifact
		}
		body, err := renderIndividualReplacements(
			decoded,
			candidates,
			selected,
			artifacts,
			outcome,
			budget,
		)
		if err != nil {
			return nil, err
		}
		if len(body) > MaxInlineToolResultBytes {
			return nil, ErrCannotBound
		}
		return body, nil
	}

	combined := combineCandidates(candidates)
	placeholder := placeholderArtifact(combined)
	preflight, err = renderCombinedReplacement(decoded, candidates, combined, placeholder, outcome)
	if err != nil {
		return nil, err
	}
	if len(preflight) > MaxInlineToolResultBytes {
		return nil, ErrCannotBound
	}
	artifact, err := persistCandidate(persist, combined)
	if err != nil {
		return nil, err
	}
	body, err := renderCombinedReplacement(decoded, candidates, combined, artifact, outcome)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxInlineToolResultBytes {
		return nil, ErrCannotBound
	}
	return body, nil
}

func decodeCandidates(
	parts json.RawMessage,
) ([]json.RawMessage, []candidate, error) {
	var decoded []json.RawMessage
	if err := json.Unmarshal(parts, &decoded); err != nil || decoded == nil {
		if err == nil {
			err = errors.New("must be a JSON array")
		}
		return nil, nil, fmt.Errorf("decode tool result content parts: %w", err)
	}
	var candidates []candidate
	for index, raw := range decoded {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return nil, nil, fmt.Errorf("decode tool result content part %d", index)
		}
		var kind string
		if err := json.Unmarshal(fields["type"], &kind); err != nil {
			return nil, nil, fmt.Errorf("decode tool result content part %d type: %w", index, err)
		}
		var content []byte
		switch kind {
		case "text":
			var text string
			if err := json.Unmarshal(fields["text"], &text); err != nil {
				return nil, nil, fmt.Errorf("decode text content part %d: %w", index, err)
			}
			content = []byte(text)
		case "structured_data":
			value := fields["value"]
			if len(value) == 0 {
				continue
			}
			var out bytes.Buffer
			if err := json.Indent(&out, value, "", "  "); err != nil {
				return nil, nil, fmt.Errorf("indent structured content part %d: %w", index, err)
			}
			content = out.Bytes()
		default:
			continue
		}
		if len(content) > 0 {
			candidates = append(candidates, candidate{index: index, kind: kind, content: content})
		}
	}
	return decoded, candidates, nil
}

func scaledPreviewBudget(candidateCount int) int {
	if candidateCount < 1 {
		candidateCount = 1
	}
	budget := MaxInlineToolResultBytes / (2 * candidateCount)
	if budget > PreviewBytes {
		return PreviewBytes
	}
	if budget < 1_024 {
		return 1_024
	}
	return budget
}

func selectIndividualCandidates(
	decoded []json.RawMessage,
	candidates []candidate,
	outcome string,
	budget int,
) (map[int]bool, json.RawMessage, error) {
	bySize := append([]candidate(nil), candidates...)
	sort.SliceStable(bySize, func(i, j int) bool {
		return len(bySize[i].content) > len(bySize[j].content)
	})
	selected := make(map[int]bool, len(candidates))
	current, err := marshalBoundedParts(decoded)
	if err != nil {
		return nil, nil, err
	}
	currentSize := len(current)
	for _, item := range bySize {
		selected[item.index] = true
		body, err := renderIndividualReplacements(
			decoded,
			candidates,
			selected,
			nil,
			outcome,
			budget,
		)
		if err != nil {
			return nil, nil, err
		}
		if len(body) >= currentSize {
			delete(selected, item.index)
			continue
		}
		current = body
		currentSize = len(body)
		if currentSize <= MaxInlineToolResultBytes {
			return selected, current, nil
		}
	}
	return selected, current, nil
}

func renderIndividualReplacements(
	decoded []json.RawMessage,
	candidates []candidate,
	selected map[int]bool,
	artifacts map[int]Artifact,
	outcome string,
	budget int,
) (json.RawMessage, error) {
	candidateByIndex := make(map[int]candidate, len(candidates))
	for _, item := range candidates {
		candidateByIndex[item.index] = item
	}
	out := make([]json.RawMessage, 0, len(decoded)+len(selected))
	for index, raw := range decoded {
		item, isCandidate := candidateByIndex[index]
		if !isCandidate || !selected[index] {
			out = append(out, raw)
			continue
		}
		artifact, ok := artifacts[index]
		if !ok {
			artifact = placeholderArtifact(item)
		}
		replacement, err := replacementParts(item, artifact, outcome, budget)
		if err != nil {
			return nil, err
		}
		out = append(out, replacement...)
	}
	return marshalBoundedParts(out)
}

func renderCombinedReplacement(
	decoded []json.RawMessage,
	candidates []candidate,
	combined candidate,
	artifact Artifact,
	outcome string,
) (json.RawMessage, error) {
	candidateByIndex := make(map[int]bool, len(candidates))
	for _, item := range candidates {
		candidateByIndex[item.index] = true
	}
	replacement, err := replacementParts(combined, artifact, outcome, PreviewBytes)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(decoded)-len(candidates)+len(replacement))
	for index, raw := range decoded {
		if !candidateByIndex[index] {
			out = append(out, raw)
			continue
		}
		if index == combined.index {
			out = append(out, replacement...)
		}
	}
	return marshalBoundedParts(out)
}

func replacementParts(
	item candidate,
	artifact Artifact,
	outcome string,
	budget int,
) ([]json.RawMessage, error) {
	lines := Lines(item.content)
	preview, err := json.Marshal(map[string]any{
		"type": "text",
		"text": Preview(lines, int64(len(item.content)), artifact, outcome, budget),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal tool result preview: %w", err)
	}
	ref, err := json.Marshal(ArtifactRefPart(artifact))
	if err != nil {
		return nil, fmt.Errorf("marshal artifact reference: %w", err)
	}
	return []json.RawMessage{preview, ref}, nil
}

func persistCandidate(persist Persist, item candidate) (Artifact, error) {
	contentType := TextContentType
	if item.kind == "structured_data" {
		contentType = StructuredContentType
	}
	return persist(item.index, contentType, item.content, len(Lines(item.content)))
}

func placeholderArtifact(item candidate) Artifact {
	contentType := TextContentType
	if item.kind == "structured_data" {
		contentType = StructuredContentType
	}
	return Artifact{
		RawID:       "00000000-0000-0000-0000-000000000000",
		PublicID:    "art_00000000000000000000000000",
		ContentType: contentType,
		SizeBytes:   int64(len(item.content)),
		LineCount:   len(Lines(item.content)),
	}
}

func combineCandidates(candidates []candidate) candidate {
	var combined bytes.Buffer
	for _, item := range candidates {
		fmt.Fprintf(&combined, "--- content part %d (%s) ---\n", item.index, item.kind)
		combined.Write(item.content)
		if len(item.content) > 0 && item.content[len(item.content)-1] != '\n' {
			combined.WriteByte('\n')
		}
	}
	return candidate{index: candidates[0].index, kind: "text", content: combined.Bytes()}
}

func marshalBoundedParts(parts []json.RawMessage) (json.RawMessage, error) {
	body, err := json.Marshal(parts)
	if err != nil {
		return nil, fmt.Errorf("marshal bounded tool result: %w", err)
	}
	return body, nil
}

func ArtifactRefPart(artifact Artifact) map[string]any {
	return map[string]any{
		"type":         "artifact_ref",
		"artifact_id":  artifact.RawID,
		"content_type": artifact.ContentType,
		"size_bytes":   artifact.SizeBytes,
		"line_count":   artifact.LineCount,
	}
}

func Preview(
	lines []string,
	totalBytes int64,
	artifact Artifact,
	outcome string,
	budget int,
) string {
	excerpt := BuildExcerpt(lines, budget)
	var rendered strings.Builder
	fmt.Fprintf(
		&rendered,
		"%s: showing the first %d and last %d of %d lines (%d of %d bytes). "+
			"Full output is stored as artifact %s (%s).\n%s",
		outcome,
		excerpt.HeadLines,
		excerpt.TailLines,
		len(lines),
		excerpt.ShownBytes,
		totalBytes,
		artifact.PublicID,
		artifact.ContentType,
		excerpt.Text,
	)
	if excerpt.HeadLines < len(lines) {
		fmt.Fprintf(
			&rendered,
			"Use read_artifact with artifact_id=%q and offset_line=%d to continue, "+
				"or search_artifact with a regex pattern to search the full output.",
			artifact.PublicID,
			excerpt.HeadLines+1,
		)
	} else {
		fmt.Fprintf(
			&rendered,
			"Use read_artifact with artifact_id=%q and offset_byte=0 to page the full line, "+
				"or search_artifact with a regex pattern to search the full output.",
			artifact.PublicID,
		)
	}
	return rendered.String()
}

type Excerpt struct {
	Text       string
	HeadLines  int
	TailLines  int
	ShownBytes int
}

func BuildExcerpt(lines []string, budget int) Excerpt {
	if budget <= 0 || len(lines) == 0 {
		return Excerpt{}
	}
	headBudget := budget * 3 / 4
	tailBudget := budget - headBudget
	head := excerptHead(lines, headBudget)
	tail := excerptTail(lines, len(head), tailBudget)
	var body strings.Builder
	shown := 0
	for _, line := range head {
		body.WriteString(line)
		body.WriteByte('\n')
		shown += len(line) + 1
	}
	omitted := len(lines) - len(head) - len(tail)
	if omitted > 0 {
		fmt.Fprintf(&body, "... [%d lines omitted] ...\n", omitted)
	}
	for _, line := range tail {
		body.WriteString(line)
		body.WriteByte('\n')
		shown += len(line) + 1
	}
	return Excerpt{Text: body.String(), HeadLines: len(head), TailLines: len(tail), ShownBytes: shown}
}

func excerptHead(lines []string, budget int) []string {
	if budget <= 0 {
		return nil
	}
	out := make([]string, 0)
	used := 0
	for _, line := range lines {
		line = TruncateBytes(line, PreviewLineBytes)
		remaining := budget - used - 1
		if remaining < 0 {
			break
		}
		line = TruncateBytes(line, remaining)
		if line == "" && len(out) > 0 {
			break
		}
		out = append(out, line)
		used += len(line) + 1
		if used >= budget {
			break
		}
	}
	return out
}

func excerptTail(lines []string, headLines, budget int) []string {
	if budget <= 0 {
		return nil
	}
	out := make([]string, 0)
	used := 0
	for index := len(lines) - 1; index >= headLines; index-- {
		line := TruncateBytes(lines[index], PreviewLineBytes)
		remaining := budget - used - 1
		if remaining < 0 {
			break
		}
		line = TruncateBytes(line, remaining)
		if line == "" && len(out) > 0 {
			break
		}
		out = append([]string{line}, out...)
		used += len(line) + 1
		if used >= budget {
			break
		}
	}
	return out
}

func IsTextReadableContentType(contentType string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	return strings.HasPrefix(mediaType, "text/") ||
		mediaType == "application/json" ||
		strings.HasSuffix(mediaType, "+json") ||
		mediaType == "application/x-ndjson"
}

func TruncateBytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func Lines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
