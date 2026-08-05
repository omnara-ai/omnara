//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestKernelToolResultStructuredDataPreservesJSONValuesAndOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_tool_result_structured_values")
	values := []string{
		`{"answer":42}`,
		`["first",2,false,null]`,
		`"plain string"`,
		`17.5`,
		`true`,
		`null`,
		`{}`,
	}

	for index, value := range values {
		toolCallID := createToolCallForProcessTest(
			t,
			ctx,
			fixture,
			fmt.Sprintf("kernel_tool_result_structured_value_%d", index),
			"read_process",
		)
		content := json.RawMessage(
			`[{"type":"text","text":"before"},{"type":"structured_data","value":` +
				value +
				`},{"type":"text","text":"after"}]`,
		)
		if _, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			ID:                 toolCallID,
			Outcome:            executionstore.ToolResultOutcomeSucceeded,
			RuntimeLockID:      fixture.Lock.ID,
			ResultContentParts: content,
		}); err != nil {
			t.Fatalf("complete structured value %s: %v", value, err)
		}
		stored, err := fixture.Store.Execution().GetToolCall(
			ctx,
			testProjectID,
			fixture.AgentID,
			toolCallID,
		)
		if err != nil {
			t.Fatalf("read structured value %s: %v", value, err)
		}
		if !sameJSON(stored.ResultContentParts, content) {
			t.Fatalf(
				"structured value %s round trip = %s, want %s",
				value,
				stored.ResultContentParts,
				content,
			)
		}
	}
}
