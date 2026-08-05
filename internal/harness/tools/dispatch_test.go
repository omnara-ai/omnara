package tools

import "testing"

func TestToolCompletionRejectsUnsetContent(t *testing.T) {
	transactional := completeInTransaction(toolResultContent{})
	if failed, ok := transactional.(failTransaction); !ok || failed.cause == nil {
		t.Fatalf("transactional result = %#v, want failure", transactional)
	}

	async := completeAsynchronously(toolResultContent{})
	if failed, ok := async.(failAsync); !ok || failed.cause == nil {
		t.Fatalf("async result = %#v, want failure", async)
	}

	empty := newToolResultContent()
	if _, ok := completeInTransaction(empty).(completeTransaction); !ok {
		t.Fatal("explicitly empty transactional result was not accepted")
	}
	if _, ok := completeAsynchronously(empty).(completeAsync); !ok {
		t.Fatal("explicitly empty async result was not accepted")
	}
}
