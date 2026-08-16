package orglifecycle

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/resourcename"
)

func TestCreateOrgForUserValidatesNameLengthBeforeStorage(t *testing.T) {
	service := &Service{}
	_, err := service.CreateOrgForUser(context.Background(), CreateOrgForUserInput{
		OrgID:  uuid.New(),
		UserID: uuid.New(),
		Name:   strings.Repeat("界", resourcename.MaxCodePoints+1),
	})
	if err == nil || !strings.Contains(
		err.Error(),
		fmt.Sprintf("cannot exceed %d Unicode characters", resourcename.MaxCodePoints),
	) {
		t.Fatalf("CreateOrgForUser error = %v, want name length rejection", err)
	}
}
