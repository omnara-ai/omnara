package compaction

import (
	"fmt"

	"github.com/google/uuid"
)

var (
	testProjectID      = uuid.MustParse("019b18bf-0000-7000-8000-000000000001")
	testAgentID        = uuid.MustParse("019b18bf-0000-7000-8000-000000000002")
	testTurnID         = uuid.MustParse("019b18bf-0000-7000-8000-000000000003")
	testOpeningInputID = uuid.MustParse("019b18bf-0000-7000-8000-000000000004")
	testRuntimeLockID  = uuid.MustParse("019b18bf-0000-7000-8000-000000000005")
)

func testIDN(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("019b18bf-0000-7000-8000-%012d", n))
}
