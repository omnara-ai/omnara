package tools

import (
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage"
)

func integrationToolTestID(seed string) storage.ID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("omnara-integration-tool:"+seed))
}
