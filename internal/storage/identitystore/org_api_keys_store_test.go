package identitystore

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestPrepareOrgAPIKeyInputRejectsInvalidNameBeforeTokenGeneration(t *testing.T) {
	creatorID := uuid.New()
	_, _, err := prepareOrgAPIKeyInput(CreateOrgAPIKeyInput{
		OrgID:           uuid.New(),
		ActorPrincipal:  NewUserPrincipal(creatorID),
		CreatedByUserID: creatorID,
		Name:            "unsafe\u200dname",
		OrgRole:         authz.OrgRoleAdmin,
	})
	if !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
}
