package modelcontext

import (
	"fmt"

	"github.com/google/uuid"
)

var (
	testProjectID = uuid.MustParse("019b18be-0000-7000-8000-000000000001")
	testAgentID   = uuid.MustParse("019b18be-0000-7000-8000-000000000002")
	testTurnID    = uuid.MustParse("019b18be-0000-7000-8000-000000000003")
	testInputID   = uuid.MustParse("019b18be-0000-7000-8000-000000000004")
)

func testIDN(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("019b18be-0000-7000-8000-%012d", n))
}
