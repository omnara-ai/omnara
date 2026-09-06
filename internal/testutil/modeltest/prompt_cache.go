package modeltest

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type PreparedPrefix struct {
	static   map[string]any
	messages []any
}

func PreparePrefix(t testing.TB, client model.Client, bundle modelcontext.Bundle) PreparedPrefix {
	t.Helper()
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: bundle,
		Policy:  model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(prepared.Body, &body); err != nil {
		t.Fatalf("decode prepared body: %v", err)
	}
	withoutCacheControl(body)
	messagesKey := "messages"
	if client.APIFormat() == modelprotocol.APIFormatOpenAIResponses {
		messagesKey = "input"
	}
	messages, _ := body[messagesKey].([]any)
	delete(body, messagesKey)
	return PreparedPrefix{static: body, messages: withoutTrailingSystemContext(messages)}
}

func PrefixViolation(previous, next PreparedPrefix) string {
	if !reflect.DeepEqual(previous.static, next.static) {
		return "request fields outside the conversation changed"
	}
	if len(next.messages) <= len(previous.messages) {
		return "conversation did not extend the previous request"
	}
	for index := range previous.messages {
		if !reflect.DeepEqual(previous.messages[index], next.messages[index]) {
			return fmt.Sprintf("message %d changed", index)
		}
	}
	return ""
}

func withoutCacheControl(value any) {
	switch value := value.(type) {
	case map[string]any:
		delete(value, "cache_control")
		for _, child := range value {
			withoutCacheControl(child)
		}
	case []any:
		for _, child := range value {
			withoutCacheControl(child)
		}
	}
}

func withoutTrailingSystemContext(messages []any) []any {
	end := len(messages)
	for end > 1 {
		message, _ := messages[end-1].(map[string]any)
		if message["role"] != "system" {
			break
		}
		end--
	}
	return messages[:end]
}
