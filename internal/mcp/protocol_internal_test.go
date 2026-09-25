package mcp

import (
	"encoding/json"
	"errors"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerInfoFromMetaRejectsMalformedImplementation(t *testing.T) {
	meta, err := json.Marshal(map[string]any{sdkmcp.MetaKeyServerInfo: map[string]any{"name": 1}})
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if _, err := serverInfoFromMeta(meta); !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("serverInfoFromMeta() error = %v, want ErrMalformedResponse", err)
	}
}
