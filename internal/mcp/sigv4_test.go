package mcp

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/omnara-ai/omnara/internal/sigv4"
)

func TestBuildHTTPPreparesSigV4Request(t *testing.T) {
	ctx := context.Background()
	signer, err := sigv4.NewSigner(
		"execute-api",
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIAEXAMPLE", "secret", "session-token"),
	)
	if err != nil {
		t.Fatalf("create SigV4 request signer: %v", err)
	}
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	request, err := (&httpClient{}).buildHTTP(ctx, Conn{
		EndpointURL:     "https://example.execute-api.us-west-2.amazonaws.com/mcp",
		MCPSessionID:    "session",
		ProtocolVersion: ProtocolVersion,
		prepareRequest:  signer.Sign,
	}, body)
	if err != nil {
		t.Fatalf("build signed request: %v", err)
	}
	authorization := request.Header.Get("Authorization")
	for _, want := range []string{
		"AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/",
		"/us-west-2/execute-api/aws4_request",
		"mcp-protocol-version",
		"mcp-session-id",
	} {
		if !strings.Contains(authorization, want) {
			t.Errorf("Authorization header %q does not contain %q", authorization, want)
		}
	}
	if request.Header.Get("X-Amz-Date") == "" {
		t.Error("signed request is missing X-Amz-Date")
	}
	if request.Header.Get("X-Amz-Security-Token") != "session-token" {
		t.Errorf("X-Amz-Security-Token = %q", request.Header.Get("X-Amz-Security-Token"))
	}
	gotBody, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read signed request body: %v", err)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("signed body = %s, want %s", gotBody, body)
	}
}
