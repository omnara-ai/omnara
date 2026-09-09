package mcp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	StatelessProtocolVersion = "2026-07-28"
	LegacyProtocolVersion    = "2025-11-25"

	headerMethod      = "Mcp-Method"
	headerName        = "Mcp-Name"
	headerParamPrefix = "Mcp-Param-"

	base64HeaderPrefix = "=?base64?"
	base64HeaderSuffix = "?="

	resultTypeComplete      = "complete"
	resultTypeInputRequired = "input_required"
)

var statelessProtocolVersions = []string{StatelessProtocolVersion}

func IsStatelessProtocolVersion(version string) bool {
	return version >= StatelessProtocolVersion
}

func NegotiateStatelessProtocolVersion(supported []string) (string, bool) {
	for _, version := range statelessProtocolVersions {
		if slices.Contains(supported, version) {
			return version, true
		}
	}
	return "", false
}

var statelessRequestSequence atomic.Int64

func NextStatelessRequestID() int64 {
	return statelessRequestSequence.Add(1)
}

type CacheHint struct {
	TTLMs      int
	CacheScope string
}

type DiscoverResult struct {
	ProtocolVersion    string
	SupportedVersions  []string
	ServerCapabilities json.RawMessage
	ServerInfo         json.RawMessage
	Instructions       string
	Cache              CacheHint
}

type ToolCall struct {
	Name      string
	Arguments json.RawMessage
	Headers   []ToolHeader
}

func withRequestMeta(
	params json.RawMessage,
	protocolVersion string,
	clientInfo *sdkmcp.Implementation,
) (json.RawMessage, error) {
	object := map[string]json.RawMessage{}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &object); err != nil {
			return nil, fmt.Errorf("mcp: request params must be a JSON object: %w", err)
		}
		if object == nil {
			object = map[string]json.RawMessage{}
		}
	}
	meta := map[string]json.RawMessage{}
	if existing, ok := object["_meta"]; ok && len(existing) != 0 {
		if err := json.Unmarshal(existing, &meta); err != nil {
			return nil, fmt.Errorf("mcp: request _meta must be a JSON object: %w", err)
		}
		if meta == nil {
			meta = map[string]json.RawMessage{}
		}
	}
	version, err := json.Marshal(protocolVersion)
	if err != nil {
		return nil, err
	}
	info, err := json.Marshal(clientInfo)
	if err != nil {
		return nil, err
	}
	meta[sdkmcp.MetaKeyProtocolVersion] = version
	meta[sdkmcp.MetaKeyClientInfo] = info
	meta[sdkmcp.MetaKeyClientCapabilities] = json.RawMessage(`{}`)
	encodedMeta, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	object["_meta"] = encodedMeta
	return json.Marshal(object)
}

type wireResult struct {
	ResultType string          `json:"resultType"`
	Meta       json.RawMessage `json:"_meta"`
}

func checkResultType(result json.RawMessage) error {
	var envelope wireResult
	if err := json.Unmarshal(result, &envelope); err != nil {
		return fmt.Errorf("mcp: decode result envelope: %w", err)
	}
	switch envelope.ResultType {
	case "", resultTypeComplete:
		return nil
	case resultTypeInputRequired:
		return ErrInputRequired
	default:
		return fmt.Errorf("mcp: unrecognized resultType %q", envelope.ResultType)
	}
}

func serverInfoFromMeta(meta json.RawMessage) (json.RawMessage, error) {
	if len(meta) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(meta, &fields); err != nil {
		return nil, fmt.Errorf("mcp: decode result _meta: %w", err)
	}
	info, ok := fields[sdkmcp.MetaKeyServerInfo]
	if !ok || len(info) == 0 || string(info) == "null" {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(info, &object); err != nil {
		return nil, fmt.Errorf("mcp: decode server info: %w", err)
	}
	return info, nil
}

func encodeHeaderValue(value string) string {
	if headerValueIsPlain(value) {
		return value
	}
	return base64HeaderPrefix + base64.StdEncoding.EncodeToString([]byte(value)) + base64HeaderSuffix
}

func headerValueIsPlain(value string) bool {
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, base64HeaderPrefix) && strings.HasSuffix(value, base64HeaderSuffix) {
		return false
	}
	if value[0] == ' ' || value[0] == '\t' || value[len(value)-1] == ' ' || value[len(value)-1] == '\t' {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if c == ' ' || c == '\t' {
			continue
		}
		if c < 0x21 || c > 0x7E {
			return false
		}
	}
	return true
}

func statelessRequestHeaders(method string, name string, paramHeaders map[string]string) (http.Header, error) {
	if method == "" {
		return nil, errors.New("mcp: request method is required")
	}
	headers := http.Header{}
	headers.Set(headerMethod, method)
	if name != "" {
		headers.Set(headerName, encodeHeaderValue(name))
	}
	for header, value := range paramHeaders {
		headers.Set(headerParamPrefix+header, encodeHeaderValue(value))
	}
	return headers, nil
}
