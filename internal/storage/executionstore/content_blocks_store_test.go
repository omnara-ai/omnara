package executionstore

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestToolResultContentBlocksCanonicalizeToPersistedShape(t *testing.T) {
	artifactID := "018ffc6b-7f1a-7828-8687-93aa210f5f4a"
	input := json.RawMessage(`[
		{"type":"text","text":"before"},
		{"type":"structured_data","value":{"runtime_lock_id":"domain value","items":[1,true,null]}},
		{"type":"media_ref","artifact_id":"` + artifactID + `"},
		{"type":"text","text":"after"}
	]`)
	blocks, err := parseToolResultContentBlocks(input)
	if err != nil {
		t.Fatalf("parse tool result content blocks: %v", err)
	}
	if len(blocks) != 4 {
		t.Fatalf("content blocks = %d, want 4", len(blocks))
	}
	canonical, err := marshalToolResultContentBlocks(blocks)
	if err != nil {
		t.Fatalf("marshal tool result content blocks: %v", err)
	}
	if !sameJSON(canonical, input) {
		t.Fatalf("canonical content blocks = %s, want %s", canonical, input)
	}
}

func TestContentBlockMetadataCanonicalizesToPersistedShape(t *testing.T) {
	artifactID := "018ffc6b-7f1a-7828-8687-93aa210f5f4a"
	tests := []struct {
		name    string
		input   json.RawMessage
		parse   func(json.RawMessage) ([]CreateContentBlockInput, error)
		marshal func([]CreateContentBlockInput) (json.RawMessage, error)
	}{
		{
			name:    "agent input",
			input:   json.RawMessage(`[{"type":"text","text":"hello","metadata":{"omnara_hidden":"true"}}]`),
			parse:   parseAgentInputContentBlocks,
			marshal: marshalAgentInputContentBlocks,
		},
		{
			name: "tool result",
			input: json.RawMessage(`[` +
				`{"type":"structured_data","value":{"ok":true},"metadata":{"source":"test"}},` +
				`{"type":"media_ref","artifact_id":"` + artifactID + `","metadata":{"position":"2"}}` +
				`]`),
			parse:   parseToolResultContentBlocks,
			marshal: marshalToolResultContentBlocks,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blocks, err := test.parse(test.input)
			if err != nil {
				t.Fatalf("parse content blocks: %v", err)
			}
			canonical, err := test.marshal(blocks)
			if err != nil {
				t.Fatalf("marshal content blocks: %v", err)
			}
			if !sameJSON(canonical, test.input) {
				t.Fatalf("canonical content blocks = %s, want %s", canonical, test.input)
			}
		})
	}
}

func TestContentBlockMetadataRequiresObject(t *testing.T) {
	_, err := parseAgentInputContentBlocks(json.RawMessage(
		`[{"type":"text","text":"hello","metadata":true}]`,
	))
	if err == nil || !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("invalid metadata error = %v, want invalid request", err)
	}
}

func TestToolResultContentBlocksRejectUnpersistedEnvelopeFields(t *testing.T) {
	tests := []struct {
		name  string
		input json.RawMessage
	}{
		{
			name: "unknown field",
			input: json.RawMessage(
				`[{"type":"structured_data","value":{"ok":true},"transport_metadata":"discarded"}]`,
			),
		},
		{
			name: "provider replay metadata",
			input: json.RawMessage(
				`[{"type":"media_ref","artifact_id":"018ffc6b-7f1a-7828-8687-93aa210f5f4a","provider_replay":{"item":{"id":"ig_1"}}}]`,
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseToolResultContentBlocks(test.input)
			if err == nil || !errors.Is(err, storeerr.ErrInvalidRequest) {
				t.Fatalf("invalid tool result field error = %v, want invalid request", err)
			}
		})
	}
}

func TestToolResultContentBlocksAllowEmptyText(t *testing.T) {
	input := json.RawMessage(`[{"type":"text","text":""}]`)
	blocks, err := parseToolResultContentBlocks(input)
	if err != nil {
		t.Fatalf("parse empty text block: %v", err)
	}
	if len(blocks) != 1 || blocks[0].BlockKind != ContentBlockKindText ||
		blocks[0].TextContent != "" {
		t.Fatalf("empty text blocks = %+v, want one empty text block", blocks)
	}
}

func TestToolResultContentBlocksRequireTextString(t *testing.T) {
	for _, input := range []json.RawMessage{
		json.RawMessage(`[{"type":"text"}]`),
		json.RawMessage(`[{"type":"text","text":null}]`),
		json.RawMessage(`[{"type":"text","text":7}]`),
	} {
		_, err := parseToolResultContentBlocks(input)
		if err == nil || !errors.Is(err, storeerr.ErrInvalidRequest) {
			t.Fatalf("invalid text block %s error = %v, want invalid request", input, err)
		}
	}
}

func TestContentBlockInputsUseExactOwnerContracts(t *testing.T) {
	tests := []struct {
		name  string
		parse func(json.RawMessage) ([]CreateContentBlockInput, error)
		input json.RawMessage
	}{
		{
			name:  "agent input rejects legacy discriminator",
			parse: parseAgentInputContentBlocks,
			input: json.RawMessage(`[{"part_kind":"text","text":"hello"}]`),
		},
		{
			name:  "agent input rejects inline media after ingestion",
			parse: parseAgentInputContentBlocks,
			input: json.RawMessage(
				`[{"type":"media","media_type":"image/png","data":"aGVsbG8="}]`,
			),
		},
		{
			name:  "agent input rejects tool result data",
			parse: parseAgentInputContentBlocks,
			input: json.RawMessage(`[{"type":"structured_data","value":{"ok":true}}]`),
		},
		{
			name:  "agent input rejects model provider replay",
			parse: parseAgentInputContentBlocks,
			input: json.RawMessage(
				`[{"type":"text","text":"hello","provider_replay":{"item":{"signature":"opaque"}}}]`,
			),
		},
		{
			name:  "tool result rejects legacy media alias",
			parse: parseToolResultContentBlocks,
			input: json.RawMessage(
				`[{"type":"media","artifact_id":"018ffc6b-7f1a-7828-8687-93aa210f5f4a"}]`,
			),
		},
		{
			name:  "tool result rejects model reasoning",
			parse: parseToolResultContentBlocks,
			input: json.RawMessage(`[{"type":"reasoning","text":"private"}]`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.parse(test.input); !errors.Is(err, storeerr.ErrInvalidRequest) {
				t.Fatalf("parse content blocks error = %v, want invalid request", err)
			}
		})
	}
}

func TestAgentInputContentBlocksRejectUnknownFields(t *testing.T) {
	_, err := parseAgentInputContentBlocks(json.RawMessage(
		`[{"type":"text","text":"hello","transport_metadata":"discarded"}]`,
	))
	if err == nil || !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("unknown agent input field error = %v, want invalid request", err)
	}
}
