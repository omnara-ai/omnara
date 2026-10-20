package modelcontext

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

func marshalJSON(value any) (json.RawMessage, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal model context json: %w", err)
	}
	return body, nil
}

func StructuredDataText(value json.RawMessage) string {
	metadata, texts := splitMultilineFields(value)
	if len(texts) == 0 {
		return undoHTMLEscapes(value)
	}
	var b strings.Builder
	b.WriteString(metadata)
	for _, text := range texts {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("<" + text.key + ">\n")
		b.WriteString(text.value)
		b.WriteString("</" + text.key + ">")
	}
	return b.String()
}

type multilineField struct {
	key   string
	value string
}

func splitMultilineFields(value json.RawMessage) (string, []multilineField) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return "", nil
	}
	var (
		metadata bytes.Buffer
		texts    []multilineField
	)
	metadata.WriteByte('{')
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return "", nil
		}
		key, ok := token.(string)
		if !ok {
			return "", nil
		}
		var field json.RawMessage
		if err := decoder.Decode(&field); err != nil {
			return "", nil
		}
		var text string
		if field[0] == '"' && json.Unmarshal(field, &text) == nil && strings.Contains(text, "\n") {
			texts = append(texts, multilineField{key: key, value: text})
			continue
		}
		if metadata.Len() > 1 {
			metadata.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return "", nil
		}
		metadata.Write(encodedKey)
		metadata.WriteByte(':')
		metadata.Write(field)
	}
	if len(texts) == 0 || metadata.Len() == 1 {
		return "", texts
	}
	metadata.WriteByte('}')
	return undoHTMLEscapes(metadata.Bytes()), texts
}

func undoHTMLEscapes(value []byte) string {
	if !bytes.Contains(value, []byte(`\u`)) {
		return string(value)
	}
	out := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' || i+1 >= len(value) {
			out = append(out, value[i])
			continue
		}
		if value[i+1] == 'u' && i+6 <= len(value) {
			if r, ok := htmlEscapedRunes[strings.ToLower(string(value[i+2:i+6]))]; ok {
				out = utf8.AppendRune(out, r)
				i += 5
				continue
			}
		}
		out = append(out, value[i], value[i+1])
		i++
	}
	return string(out)
}

var htmlEscapedRunes = map[string]rune{
	"003c": '<',
	"003e": '>',
	"0026": '&',
	"2028": 0x2028,
	"2029": 0x2029,
}
