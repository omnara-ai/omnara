package mcp

import (
	"encoding/json"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerInfoFromMetaRejectsMalformedImplementation(t *testing.T) {
	meta, err := json.Marshal(map[string]any{sdkmcp.MetaKeyServerInfo: map[string]any{"name": 1}})
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if _, err := serverInfoFromMeta(meta); err == nil {
		t.Fatal("serverInfoFromMeta() error = nil, want decode error")
	}
}
