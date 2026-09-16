package providererrors

import "testing"

func TestUserFacingMessageRewritesDeferredToolsUnsupported(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{
			name:    "anthropic tool addition",
			message: "tool_addition/tool_removal is not supported on this model",
			want:    DeferredToolsUnsupportedMessage,
		},
		{
			name:    "openai responses tool search",
			message: "Tool 'tool_search' is not supported with gpt-4.1.",
			want:    DeferredToolsUnsupportedMessage,
		},
		{
			name:    "unrelated invalid request",
			message: "messages.0.content: field required",
			want:    "messages.0.content: field required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UserFacingMessage(tt.message); got != tt.want {
				t.Fatalf("UserFacingMessage(%q) = %q, want %q", tt.message, got, tt.want)
			}
		})
	}
}

func TestUserFacingCodeRewritesDeferredToolsUnsupported(t *testing.T) {
	unsupported := "Tool 'tool_search' is not supported with gpt-5-mini."
	if got := UserFacingCode("invalid_request_error", unsupported); got != DeferredToolsUnsupportedCode {
		t.Fatalf("code = %q", got)
	}
	if got := UserFacingCode("invalid_request_error", "bad field"); got != "invalid_request_error" {
		t.Fatalf("code = %q", got)
	}
}
