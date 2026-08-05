package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const testCursorPublicID = "agt_aaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestKeysetCursorRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, 6, 22, 8, 30, 15, 123456789, time.UTC)

	token, err := encodeKeysetCursor(createdAt, testCursorPublicID)
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty cursor token")
	}

	got, err := decodeKeysetCursor(token)
	if err != nil {
		t.Fatalf("decode round-trip cursor: %v", err)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at round-trip = %s, want %s", got.CreatedAt, createdAt)
	}
	if got.ID != testCursorPublicID {
		t.Fatalf("id round-trip = %q, want %q", got.ID, testCursorPublicID)
	}
}

func TestKeysetCursorCarriesPublicIDNotRawUUID(t *testing.T) {
	createdAt := time.Date(2026, 6, 22, 8, 30, 15, 0, time.UTC)
	token, err := encodeKeysetCursor(createdAt, testCursorPublicID)
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("base64 decode token: %v", err)
	}
	if got := string(decoded); !strings.Contains(got, testCursorPublicID) {
		t.Fatalf("cursor payload %q does not carry the public id %q", got, testCursorPublicID)
	}
}

func TestKeysetCursorNormalizesToUTC(t *testing.T) {
	loc := time.FixedZone("UTC+5", 5*60*60)
	createdAt := time.Date(2026, 6, 22, 13, 30, 15, 0, loc)

	token, err := encodeKeysetCursor(createdAt, testCursorPublicID)
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	got, err := decodeKeysetCursor(token)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at instant changed across encode: %s vs %s", got.CreatedAt, createdAt)
	}
	if got.CreatedAt.Location() != time.UTC {
		t.Fatalf("decoded cursor location = %s, want UTC", got.CreatedAt.Location())
	}
}

func TestKeysetCursorToleratesUnknownFields(t *testing.T) {
	payload := `{"created_at":"2026-06-22T08:30:15Z","id":"agt_aaaaaaaaaaaaaaaaaaaaaaaaaa","future":"value"}`
	token := base64.RawURLEncoding.EncodeToString([]byte(payload))
	if _, err := decodeKeysetCursor(token); err != nil {
		t.Fatalf("expected forward-compatible decode, got %v", err)
	}
}

func TestDecodeKeysetCursorRejectsMalformed(t *testing.T) {
	zeroTimePayload := base64.RawURLEncoding.EncodeToString([]byte(`{"id":"agt_aaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	emptyIDPayload := base64.RawURLEncoding.EncodeToString([]byte(`{"created_at":"2026-06-22T08:30:15Z","id":""}`))
	garbageJSONPayload := base64.RawURLEncoding.EncodeToString([]byte(`{"created_at":"2026-06-22T08:30:15Z"`))
	notJSONPayload := base64.RawURLEncoding.EncodeToString([]byte("not json"))
	oversizedPayload := base64.RawURLEncoding.EncodeToString(
		[]byte(
			`{"created_at":"2026-06-22T08:30:15Z","id":"agt_aaaaaaaaaaaaaaaaaaaaaaaaaa","padding":"` + strings.Repeat(
				"x",
				maxCursorPayloadLength,
			) + `"}`,
		),
	)

	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "oversized token", token: strings.Repeat("a", maxCursorTokenLength+1)},
		{name: "oversized payload", token: oversizedPayload},
		{name: "not base64", token: "!!!not-base64!!!"},
		{name: "base64 of non-json", token: notJSONPayload},
		{name: "truncated json", token: garbageJSONPayload},
		{name: "missing created_at", token: zeroTimePayload},
		{name: "empty id", token: emptyIDPayload},
		{name: "empty", token: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeKeysetCursor(tc.token); err == nil {
				t.Fatalf("expected malformed cursor %q to be rejected", tc.token)
			}
		})
	}
}

func TestParseOpenAPIPageParamsDefaults(t *testing.T) {
	limit, after, err := parseOpenAPIPageParams(nil, nil, publicid.KindAgent)
	if err != nil {
		t.Fatalf("parse default page params: %v", err)
	}
	if limit != defaultPageLimit {
		t.Fatalf("default limit = %d, want %d", limit, defaultPageLimit)
	}
	if after.Set {
		t.Fatalf("expected unset cursor for first page, got %+v", after)
	}
}

func TestParseOpenAPIPageParamsBounds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limit   openapi.PageLimit
		wantErr error
		want    int
	}{
		{name: "explicit min", limit: 1, want: 1},
		{name: "explicit max", limit: 100, want: 100},
		{name: "zero", limit: 0, wantErr: errInvalidLimit},
		{name: "negative", limit: -1, wantErr: errInvalidLimit},
		{name: "over max", limit: 101, wantErr: errInvalidLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit, _, err := parseOpenAPIPageParams(&tc.limit, nil, publicid.KindAgent)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if limit != tc.want {
				t.Fatalf("limit = %d, want %d", limit, tc.want)
			}
		})
	}
}

func TestParseOpenAPIPageParamsCursorRoundTrip(t *testing.T) {
	rawID := uuid.New()
	createdAt := time.Date(2026, 6, 22, 8, 30, 15, 0, time.UTC)
	publicID, err := publicid.Encode(publicid.KindUser, rawID)
	if err != nil {
		t.Fatalf("encode public id: %v", err)
	}
	token, err := encodeKeysetCursor(createdAt, publicID)
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}

	cursor := token
	_, after, err := parseOpenAPIPageParams(nil, &cursor, publicid.KindUser)
	if err != nil {
		t.Fatalf("parse cursor page params: %v", err)
	}
	if !after.Set {
		t.Fatal("expected cursor to be set")
	}
	if after.ID != rawID {
		t.Fatalf("cursor id = %s, want raw uuid %s", after.ID, rawID)
	}
	if !after.CreatedAt.Equal(createdAt) {
		t.Fatalf("cursor created_at = %s, want %s", after.CreatedAt, createdAt)
	}
}

func TestParseOpenAPIPageParamsCursorMalformed(t *testing.T) {
	token, err := encodeKeysetCursor(time.Date(2026, 6, 22, 8, 30, 15, 0, time.UTC), testCursorPublicID)
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "not base64", token: "!!!not-base64!!!"},
		{name: "wrong kind", token: token},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cursor := tc.token
			if _, _, err := parseOpenAPIPageParams(nil, &cursor, publicid.KindUser); !errors.Is(err, errMalformedCursor) {
				t.Fatalf("err = %v, want errMalformedCursor", err)
			}
		})
	}
}

func TestEncodeNextCursorTerminalPage(t *testing.T) {
	token, err := encodeNextCursor(false, time.Now(), publicid.KindAgent, uuid.New())
	if err != nil {
		t.Fatalf("encode next cursor: %v", err)
	}
	if token != nil {
		t.Fatal("expected nil next_cursor on the terminal page")
	}

	if token, _ := encodeNextCursor(true, time.Now(), publicid.KindAgent, storage.NilID); token != nil {
		t.Fatal("expected nil next_cursor for an empty page")
	}
}

func TestEncodeNextCursorRoundTrip(t *testing.T) {
	rawID := uuid.New()
	createdAt := time.Date(2026, 6, 22, 8, 30, 15, 0, time.UTC)

	token, err := encodeNextCursor(true, createdAt, publicid.KindAgentProfile, rawID)
	if err != nil {
		t.Fatalf("encode next cursor: %v", err)
	}
	if token == nil {
		t.Fatal("expected next_cursor when a further page exists")
	}

	cursor := *token
	_, after, err := parseOpenAPIPageParams(nil, &cursor, publicid.KindAgentProfile)
	if err != nil {
		t.Fatalf("decode round-trip cursor: %v", err)
	}
	if after.ID != rawID || !after.CreatedAt.Equal(createdAt) {
		t.Fatalf("round-trip keyset = (%s, %s), want (%s, %s)", after.ID, after.CreatedAt, rawID, createdAt)
	}
}

func TestAgentInputQueueCursorRoundTrip(t *testing.T) {
	rawID := uuid.New()
	queuedAt := time.Date(2026, 6, 22, 8, 30, 15, 123456789, time.UTC)
	publicID, err := publicid.Encode(publicid.KindAgentInput, rawID)
	if err != nil {
		t.Fatalf("encode public id: %v", err)
	}
	token, err := encodeAgentInputQueueCursor(agentInputQueueCursor{
		InputRank: 1536,
		QueuedAt:  queuedAt,
		ID:        publicID,
	})
	if err != nil {
		t.Fatalf("encode queue cursor: %v", err)
	}

	cursor := token
	limit, after, err := parseAgentInputQueuePageParams(nil, &cursor)
	if err != nil {
		t.Fatalf("parse queue cursor: %v", err)
	}
	if limit != defaultPageLimit {
		t.Fatalf("limit = %d, want %d", limit, defaultPageLimit)
	}
	if !after.Set {
		t.Fatal("expected queue cursor to be set")
	}
	if after.InputRank != 1536 {
		t.Fatalf("input rank = %d, want 1536", after.InputRank)
	}
	if !after.QueuedAt.Equal(queuedAt) {
		t.Fatalf("queued_at = %s, want %s", after.QueuedAt, queuedAt)
	}
	if after.ID != rawID {
		t.Fatalf("id = %s, want %s", after.ID, rawID)
	}
}

func TestAgentInputQueueCursorRejectsInvalidRank(t *testing.T) {
	publicID, err := publicid.Encode(publicid.KindAgentInput, uuid.New())
	if err != nil {
		t.Fatalf("encode public id: %v", err)
	}
	for _, test := range []struct {
		name string
		rank any
	}{
		{name: "missing", rank: nil},
		{name: "string", rank: "1024"},
		{name: "zero", rank: 0},
		{name: "negative", rank: -1},
		{name: "fraction", rank: 1.5},
		{name: "overflow", rank: json.Number("9223372036854775808")},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{
				"input_rank": test.rank,
				"queued_at":  time.Date(2026, 6, 22, 8, 30, 15, 0, time.UTC),
				"id":         publicID,
			})
			if err != nil {
				t.Fatalf("encode queue cursor payload: %v", err)
			}
			token := base64.RawURLEncoding.EncodeToString(payload)
			if _, _, err := parseAgentInputQueuePageParams(nil, &token); !errors.Is(err, errMalformedCursor) {
				t.Fatalf("err = %v, want errMalformedCursor", err)
			}
		})
	}
}

func TestAgentInputQueueCursorRejectsOversizedToken(t *testing.T) {
	cursor := strings.Repeat("a", maxCursorTokenLength+1)
	if _, _, err := parseAgentInputQueuePageParams(nil, &cursor); !errors.Is(err, errMalformedCursor) {
		t.Fatalf("err = %v, want errMalformedCursor", err)
	}
}

func TestEncodeNextAgentInputQueueCursorTerminalPage(t *testing.T) {
	token, err := encodeNextAgentInputQueueCursor(false, executionstore.AgentInputRecord{
		ID:        uuid.New(),
		InputRank: 1024,
		QueuedAt:  time.Now(),
	})
	if err != nil {
		t.Fatalf("encode next queue cursor: %v", err)
	}
	if token != nil {
		t.Fatal("expected nil next_cursor on the terminal page")
	}

	token, err = encodeNextAgentInputQueueCursor(true, executionstore.AgentInputRecord{})
	if err != nil {
		t.Fatalf("encode next queue cursor: %v", err)
	}
	if token != nil {
		t.Fatal("expected nil next_cursor for an empty page")
	}
}
