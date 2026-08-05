package compaction

import (
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage"
)

var (
	testProjectID      = testID("019b18bf-0000-7000-8000-000000000001")
	testAgentID        = testID("019b18bf-0000-7000-8000-000000000002")
	testTurnID         = testID("019b18bf-0000-7000-8000-000000000003")
	testOpeningInputID = testID("019b18bf-0000-7000-8000-000000000004")
	testRuntimeLockID  = testID("019b18bf-0000-7000-8000-000000000005")
)

func testID(raw string) storage.ID {
	id, err := storage.ParseID(raw)
	if err != nil {
		panic(err)
	}
	return id
}

func testIDN(n int) storage.ID {
	return testID(fmt.Sprintf("019b18bf-0000-7000-8000-%012d", n))
}
