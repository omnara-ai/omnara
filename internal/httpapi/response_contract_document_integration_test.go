//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

const visibleMachinesPath = "/orgs/{orgID}/projects/{projectID}/machines"

func visibleMachineBody(t *testing.T, extra map[string]any) []byte {
	t.Helper()
	machine := map[string]any{
		"id":               "mch_abcdefghijklmnopqrstuvwxyz",
		"org_id":           "org_abcdefghijklmnopqrstuvwxyz",
		"source_kind":      "byo",
		"display_name":     "build box",
		"description":      "",
		"provider":         "aws",
		"lifecycle_state":  "active",
		"connection_state": "online",
		"last_observed_at": nil,
		"deleted_at":       nil,
		"created_at":       "2026-01-01T00:00:00Z",
		"updated_at":       "2026-01-01T00:00:00Z",
		"access": map[string]any{
			"can_manage": true,
			"sources":    []any{},
		},
	}
	for key, value := range extra {
		machine[key] = value
	}
	body, err := json.Marshal(map[string]any{
		"data":        []any{machine},
		"next_cursor": nil,
	})
	if err != nil {
		t.Fatalf("marshal response body: %v", err)
	}
	return body
}

func TestResponseContractDocumentAwareBody(t *testing.T) {
	contract, err := loadResponseContract()
	if err != nil {
		t.Fatalf("load response contract: %v", err)
	}
	valid := visibleMachineBody(t, nil)
	if err := contract.body.validateBody(visibleMachinesPath, http.MethodGet, http.StatusOK, valid); err != nil {
		t.Fatalf("valid VisibleMachine response rejected: %v", err)
	}
	invalid := visibleMachineBody(t, map[string]any{"unexpected": true})
	if err := contract.body.validateBody(visibleMachinesPath, http.MethodGet, http.StatusOK, invalid); err == nil {
		t.Fatal("VisibleMachine response with an unexpected property passed document-aware validation")
	}
}

func TestResponseContractDocumentAwareErrorEnvelope(t *testing.T) {
	contract, err := loadResponseContract()
	if err != nil {
		t.Fatalf("load response contract: %v", err)
	}
	valid := []byte(`{"error":"machine not found","code":"not_found"}`)
	if err := contract.body.validateBody(visibleMachinesPath, http.MethodGet, http.StatusTeapot, valid); err != nil {
		t.Fatalf("valid Error envelope rejected by 4XX range response: %v", err)
	}
	if err := contract.body.validateBody(visibleMachinesPath, http.MethodGet, http.StatusTeapot, []byte(`{"message":"missing"}`)); err == nil {
		t.Fatal("non-Error envelope passed the 4XX range response")
	}
}
