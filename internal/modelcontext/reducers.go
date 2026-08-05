package modelcontext

import (
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

const toolOutputReducerVersion = "tool-output-head-tail-v1"

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
