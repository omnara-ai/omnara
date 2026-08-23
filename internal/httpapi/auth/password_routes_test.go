package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestWriteAuthStorageErrorMapsConflict(t *testing.T) {
	err := fmt.Errorf("active personal access tokens limit reached: %w", storeerr.ErrConflict)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/device/token", nil)

	(&Handler{}).writeAuthStorageError(rec, req, err)

	var response openapi.Error
	if decodeErr := json.NewDecoder(rec.Body).Decode(&response); decodeErr != nil {
		t.Fatalf("decode conflict response: %v", decodeErr)
	}
	if rec.Code != http.StatusConflict ||
		response.Code != openapi.ErrorCodeConflict ||
		!strings.Contains(response.Error, err.Error()) {
		t.Fatalf("conflict response status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNormalizeAuthEmailCanonicalizesInternationalizedDomain(t *testing.T) {
	t.Parallel()
	email, normalized, valid := normalizeAuthEmail("User@BÜCHER.example")
	if !valid || email != "User@BÜCHER.example" || normalized != "user@xn--bcher-kva.example" {
		t.Fatalf("normalize auth email = (%q, %q, %t)", email, normalized, valid)
	}
}
