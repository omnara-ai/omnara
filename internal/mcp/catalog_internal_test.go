package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type probeClient struct {
	Client
	responses []func(protocolVersion string) (DiscoverResult, error)
	versions  []string
}

func (c *probeClient) Discover(_ context.Context, _ Conn, protocolVersion string) (DiscoverResult, error) {
	c.versions = append(c.versions, protocolVersion)
	next := c.responses[0]
	c.responses = c.responses[1:]
	return next(protocolVersion)
}

func discoverOK(protocolVersion string) (DiscoverResult, error) {
	return DiscoverResult{ProtocolVersion: protocolVersion, SupportedVersions: []string{protocolVersion}}, nil
}

func unsupportedVersion(supported ...string) func(string) (DiscoverResult, error) {
	data, err := json.Marshal(struct {
		Supported []string `json:"supported"`
	}{Supported: supported})
	if err != nil {
		panic(err)
	}
	return func(string) (DiscoverResult, error) {
		return DiscoverResult{}, &RPCError{
			Code:       CodeUnsupportedProtocolVersion,
			Message:    "unsupported",
			Data:       data,
			HTTPStatus: http.StatusBadRequest,
		}
	}
}

func TestProbeServerClassifiesResponses(t *testing.T) {
	transportErr := errors.New("dial tcp: connection refused")
	tests := []struct {
		name          string
		responses     []func(string) (DiscoverResult, error)
		wantStateless bool
		wantErr       error
		wantErrText   string
	}{
		{
			name:          "stateless server",
			responses:     []func(string) (DiscoverResult, error){discoverOK},
			wantStateless: true,
		},
		{
			name: "legacy plain 400",
			responses: []func(string) (DiscoverResult, error){func(string) (DiscoverResult, error) {
				return DiscoverResult{}, &HTTPError{Status: http.StatusBadRequest, Body: []byte("Bad Request")}
			}},
		},
		{
			name: "legacy method not found",
			responses: []func(string) (DiscoverResult, error){func(string) (DiscoverResult, error) {
				return DiscoverResult{}, &RPCError{Code: -32601, Message: "method not found"}
			}},
		},
		{
			name:      "dual server rejecting version but supporting legacy",
			responses: []func(string) (DiscoverResult, error){unsupportedVersion("2027-01-01", LegacyProtocolVersion)},
		},
		{
			name:        "server with no mutual version",
			responses:   []func(string) (DiscoverResult, error){unsupportedVersion("2027-01-01")},
			wantErr:     errNoMutualProtocolVersion,
			wantErrText: "2027-01-01",
		},
		{
			name: "header mismatch is a client bug not a legacy server",
			responses: []func(string) (DiscoverResult, error){func(string) (DiscoverResult, error) {
				return DiscoverResult{}, &RPCError{Code: CodeHeaderMismatch, Message: "mismatch", HTTPStatus: http.StatusBadRequest}
			}},
			wantErrText: "mismatch",
		},
		{
			name: "internal discovery error propagates over HTTP 200",
			responses: []func(string) (DiscoverResult, error){func(string) (DiscoverResult, error) {
				return DiscoverResult{}, &RPCError{Code: -32603, Message: "discovery unavailable"}
			}},
			wantErrText: "discovery unavailable",
		},
		{
			name: "internal discovery error propagates over HTTP 400",
			responses: []func(string) (DiscoverResult, error){func(string) (DiscoverResult, error) {
				return DiscoverResult{}, &RPCError{Code: -32603, Message: "discovery unavailable", HTTPStatus: 400}
			}},
			wantErrText: "discovery unavailable",
		},
		{
			name: "unauthorized propagates",
			responses: []func(string) (DiscoverResult, error){func(string) (DiscoverResult, error) {
				return DiscoverResult{}, &HTTPError{Status: http.StatusUnauthorized}
			}},
			wantErrText: "401",
		},
		{
			name: "transport failure propagates",
			responses: []func(string) (DiscoverResult, error){func(string) (DiscoverResult, error) {
				return DiscoverResult{}, transportErr
			}},
			wantErr: transportErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &probeClient{responses: tt.responses}
			probe, err := Manager{Client: client}.probeServer(context.Background(), Conn{EndpointURL: "https://example.com/mcp"})
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErrText != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrText) {
					t.Fatalf("error = %v, want text %q", err, tt.wantErrText)
				}
				return
			}
			if tt.wantErr != nil {
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if probe.Stateless != tt.wantStateless {
				t.Fatalf("stateless = %t, want %t", probe.Stateless, tt.wantStateless)
			}
			if client.versions[0] != StatelessProtocolVersion {
				t.Fatalf("probe used version %q", client.versions[0])
			}
		})
	}
}

func TestDropToolsWithInvalidHeaders(t *testing.T) {
	tools := []*sdkmcp.Tool{
		{Name: "ok"},
		{Name: "bad", InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"ratio": map[string]any{"type": "number", "x-mcp-header": "Ratio"},
		}}},
		nil,
	}
	kept := dropToolsWithInvalidHeaders(context.Background(), tools)
	if len(kept) != 1 || kept[0].Name != "ok" {
		t.Fatalf("kept = %+v", kept)
	}
}

func TestListAllToolsMergesCacheHints(t *testing.T) {
	client := &pagedToolsClient{pages: []ToolsPage{
		{Tools: []*sdkmcp.Tool{{Name: "a"}}, NextCursor: "1", TTLMs: 60_000, CacheScope: "public"},
		{Tools: []*sdkmcp.Tool{{Name: "b"}}, TTLMs: 5_000, CacheScope: "private"},
	}}
	listing, err := listAllTools(context.Background(), client, Conn{}, sequentialRequestIDs())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if listing.Cache.TTLMs != 5_000 || listing.Cache.CacheScope != "private" {
		t.Fatalf("merged cache hint = %+v", listing.Cache)
	}
}
