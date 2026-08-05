package mcptest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type greetArgs struct {
	Name string `json:"name" jsonschema:"the name to greet"`
}

func greet(_ context.Context, _ *sdkmcp.CallToolRequest, args greetArgs) (*sdkmcp.CallToolResult, any, error) {
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Hi " + args.Name}},
	}, nil, nil
}

type noisyArgs struct {
	Steps int `json:"steps" jsonschema:"number of progress notifications to emit before returning"`
}

func noisy(ctx context.Context, req *sdkmcp.CallToolRequest, args noisyArgs) (*sdkmcp.CallToolResult, any, error) {
	token := req.Params.GetProgressToken()
	if token == nil {
		token = "noisy"
	}
	for i := range args.Steps {
		_ = req.Session.NotifyProgress(ctx, &sdkmcp.ProgressNotificationParams{
			ProgressToken: token,
			Progress:      float64(i + 1),
			Total:         float64(args.Steps),
			Message:       fmt.Sprintf("step %d/%d", i+1, args.Steps),
		})
	}
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "done"}},
	}, nil, nil
}

func newHandler(jsonResponse bool, greetTool sdkmcp.ToolHandlerFor[greetArgs, any]) http.Handler {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "mcptest", Version: "v0"}, nil)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "greet", Description: "say hi"}, greetTool)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "noisy", Description: "emit N progress notifications, then return"}, noisy)
	return sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return srv },
		&sdkmcp.StreamableHTTPOptions{JSONResponse: jsonResponse},
	)
}

func newServer(tb testing.TB, jsonResponse bool) *httptest.Server {
	tb.Helper()
	h := newHandler(jsonResponse, greet)
	ts := httptest.NewServer(h)
	tb.Cleanup(ts.Close)
	return ts
}

func NewServer(tb testing.TB) *httptest.Server { return newServer(tb, false) }

func NewJSONServer(tb testing.TB) *httptest.Server { return newServer(tb, true) }

func NewJSONServerWithGreetResult(tb testing.TB, result string) *httptest.Server {
	tb.Helper()
	h := newHandler(
		true,
		func(context.Context, *sdkmcp.CallToolRequest, greetArgs) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: result}},
			}, nil, nil
		},
	)
	ts := httptest.NewServer(h)
	tb.Cleanup(ts.Close)
	return ts
}
