package tools

import (
	"github.com/google/uuid"
)

func integrationToolTestID(seed string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("omnara-integration-tool:"+seed))
}
