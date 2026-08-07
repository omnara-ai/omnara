package modelcontext

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/tooloutput"
)

const toolOutputReducerVersion = "tool-output-head-tail-v1"

const toolResultProjectionFloorBytes = 1_024

type ToolResultProjectionReducer struct {
	MaxResultBytes int
}

func ProjectionReducerForWindow(window ModelWindow) ToolResultProjectionReducer {
	maxBytes := tooloutput.MaxInlineToolResultBytes
	if usable := window.UsableInputTokens(); window.ContextTokens > 0 && usable < maxBytes {
		// Four bytes/token is a useful prompt estimate, and reserving roughly
		// one quarter of usable input for a single result gives a byte cap
		// numerically close to the usable-token count.
		maxBytes = usable
		if maxBytes < toolResultProjectionFloorBytes {
			maxBytes = toolResultProjectionFloorBytes
		}
	}
	return ToolResultProjectionReducer{MaxResultBytes: maxBytes}
}

func (r ToolResultProjectionReducer) Reduce(
	parts json.RawMessage,
	toolName string,
) (json.RawMessage, bool, error) {
	if r.MaxResultBytes <= 0 || len(parts) <= r.MaxResultBytes {
		return parts, false, nil
	}
	var decoded []json.RawMessage
	if err := json.Unmarshal(parts, &decoded); err != nil || decoded == nil {
		if err == nil {
			err = errors.New("must be a JSON array")
		}
		return nil, false, err
	}
	targets := make(map[int][]byte)
	var combined bytes.Buffer
	hasArtifactRef := false
	firstTarget := -1
	for index, raw := range decoded {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, false, err
		}
		var kind string
		_ = json.Unmarshal(fields["type"], &kind)
		if kind == "artifact_ref" {
			hasArtifactRef = true
			continue
		}
		var content []byte
		switch kind {
		case "text":
			var text string
			if json.Unmarshal(fields["text"], &text) == nil {
				content = []byte(text)
			}
		case "structured_data":
			if value := fields["value"]; len(value) > 0 {
				var outcomeEnvelope struct {
					Outcome string `json:"outcome"`
				}
				if index == 0 && json.Unmarshal(value, &outcomeEnvelope) == nil &&
					outcomeEnvelope.Outcome != "" {
					continue
				}
				var indented bytes.Buffer
				if err := json.Indent(&indented, value, "", "  "); err != nil {
					return nil, false, err
				}
				content = indented.Bytes()
			}
		}
		if len(content) == 0 {
			continue
		}
		if firstTarget < 0 {
			firstTarget = index
		}
		targets[index] = content
		if combined.Len() > 0 {
			combined.WriteByte('\n')
		}
		combined.Write(content)
	}
	if firstTarget < 0 {
		return nil, false, errors.New("oversized tool result has no reducible content")
	}
	for budget := min(tooloutput.PreviewBytes, r.MaxResultBytes/2); ; budget /= 2 {
		excerpt := tooloutput.BuildExcerpt(tooloutput.Lines(combined.Bytes()), budget)
		guidance := "The omitted projection content is not retrievable; " +
			"rerun the tool with narrower parameters if it matters."
		if hasArtifactRef {
			guidance = "The full output remains retrievable through the artifact reference below."
		} else if strings.TrimSpace(toolName) != "" {
			guidance = fmt.Sprintf(
				"Call %s again with narrower limits or a later offset to retrieve omitted content.",
				toolName,
			)
		}
		notice, err := json.Marshal(map[string]any{
			"type": "text",
			"text": fmt.Sprintf(
				"This tool result was reduced to fit the model context: showing the first %d and last %d of %d lines (%d of %d bytes). %s\n%s",
				excerpt.HeadLines,
				excerpt.TailLines,
				len(tooloutput.Lines(combined.Bytes())),
				excerpt.ShownBytes,
				combined.Len(),
				guidance,
				excerpt.Text,
			),
		})
		if err != nil {
			return nil, false, err
		}
		out := make([]json.RawMessage, 0, len(decoded)-len(targets)+1)
		for index, raw := range decoded {
			if _, replace := targets[index]; replace {
				if index == firstTarget {
					out = append(out, notice)
				}
				continue
			}
			out = append(out, raw)
		}
		body, err := json.Marshal(out)
		if err != nil {
			return nil, false, err
		}
		if len(body) <= r.MaxResultBytes {
			return body, true, nil
		}
		if budget == 0 {
			break
		}
	}
	return nil, false, errors.New("tool result cannot be projected within the model window")
}

type ToolOutputReducer struct {
	PreviewBytes int
}

func (r ToolOutputReducer) Reduce(parts json.RawMessage) (json.RawMessage, error) {
	limit := r.PreviewBytes
	if limit <= 0 || len(parts) <= limit {
		return parts, nil
	}
	headBytes := limit * 3 / 4
	tailBytes := limit - headBytes
	headEnd := validUTF8PrefixEnd(parts, headBytes)
	tailStart := validUTF8SuffixStart(parts, len(parts)-tailBytes)
	omittedBytes := tailStart - headEnd
	preview := string(parts[:headEnd]) +
		"\n[... omitted " + strconv.Itoa(omittedBytes) + " bytes from completed tool output ...]\n" +
		string(parts[tailStart:])
	reduced, err := marshalJSON(
		[]map[string]any{
			{
				"type":            "text",
				"text":            preview,
				"reduced":         true,
				"reducer_version": toolOutputReducerVersion,
				"original_bytes":  len(parts),
				"preview_bytes":   len(preview),
				"omitted_bytes":   omittedBytes,
			},
		},
	)
	if err != nil {
		return nil, err
	}
	return reduced, nil
}

func validUTF8PrefixEnd(value []byte, limit int) int {
	if limit >= len(value) {
		return len(value)
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return limit
}

func validUTF8SuffixStart(value []byte, start int) int {
	if start <= 0 {
		return 0
	}
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return start
}
