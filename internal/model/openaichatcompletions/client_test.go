package openaichatcompletions

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
)

func TestPrepareRequiresEndpointPath(t *testing.T) {
	_, err := (Client{ProviderModelSlug: "gpt-test"}).Prepare(context.Background(), model.PrepareInput{})
	var setupErr route.SetupError
	if !errors.As(err, &setupErr) || !strings.Contains(err.Error(), "endpoint path") {
		t.Fatalf("prepare error = %T %v, want endpoint path setup error", err, err)
	}
}
