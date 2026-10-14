package discord

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func callbackID(t *testing.T) string {
	t.Helper()
	id, err := publicid.Encode(publicid.KindAgentInteraction, uuid.MustParse("11111111-2222-4333-8444-555555555555"))
	if err != nil {
		t.Fatal(err)
	}
	custom, err := EncodeCustomID(CustomID{InteractionID: id, Action: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	return custom
}

func signedRequest(private ed25519.PrivateKey, body string, timestamp time.Time) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/discord/interactions", strings.NewReader(body))
	ts := strconv.FormatInt(timestamp.Unix(), 10)
	sig := ed25519.Sign(private, []byte(ts+body))
	r.Header.Set("X-Signature-Timestamp", ts)
	r.Header.Set("X-Signature-Ed25519", hex.EncodeToString(sig))
	return r
}

func interactionFixture(t *testing.T, intake InteractionIntake) (*InteractionHandler, ed25519.PrivateKey) {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewInteractionHandler("111", hex.EncodeToString(pub), intake)
	if err != nil {
		t.Fatal(err)
	}
	return handler, private
}

func TestInteractionSignaturePingAndActor(t *testing.T) {
	var receipts []Interaction
	handler, private := interactionFixture(t, func(ctx context.Context, i Interaction) (InteractionResponse, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > InteractionTimeout {
			t.Error("intake misses ack budget")
		}
		receipts = append(receipts, i)
		return Acknowledge(i), nil
	})
	for _, test := range []struct {
		body         string
		responseType int
		actor        string
	}{
		{`{"id":"999","application_id":"111","type":1}`, 1, ""},
		{
			`{"id":"999","application_id":"111","type":2,"guild_id":"333","channel_id":"444","member":{"user":{"id":"888"}},"data":{"name":"ask","options":[{"name":"text","type":3,"value":"help"}]}}`, 4, "888",
		},
		{fmt.Sprintf(
			`{"id":"999","application_id":"111","type":3,"channel_id":"555","user":{"id":"777"},"data":{"custom_id":%q}}`, callbackID(t)), 6, "777"},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, signedRequest(private, test.body, time.Now()))
		var response InteractionResponse
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Type != test.responseType {
			t.Fatalf("status=%d, response=%s", w.Code, w.Body.String())
		}
		if test.actor != "" && receipts[len(receipts)-1].Actor().ID != test.actor {
			t.Error("wrong actor")
		}
	}
	if len(receipts) != 2 {
		t.Fatal("ping required durable application intake")
	}
}

func TestInteractionsRejectUnverifiedOrWrongIdentity(t *testing.T) {
	var calls int
	handler, private := interactionFixture(t, func(ctx context.Context, i Interaction) (InteractionResponse, error) {
		calls++
		return Acknowledge(i), nil
	})
	body := `{"id":"999","application_id":"111","type":1}`
	for _, mode := range []string{"missing signature", "tampered", "stale", "future",
		"wrong app", "oversized", "bad custom ID", "missing actor"} {
		t.Run(mode, func(t *testing.T) {
			payload, timestamp := body, time.Now()
			want := http.StatusUnauthorized
			switch mode {
			case "stale":
				timestamp = timestamp.Add(-6 * time.Minute)
			case "future":
				timestamp = timestamp.Add(6 * time.Minute)
			case "wrong app":
				payload = `{"id":"999","application_id":"333","type":1}`
				want = http.StatusBadRequest
			case "oversized":
				payload = strings.Repeat("x", InteractionMaxBytes+1)
				want = http.StatusBadRequest
			case "bad custom ID":
				payload = `{"id":"999","application_id":"111","type":3,"channel_id":"555",` +
					`"user":{"id":"777"},"data":{"custom_id":"scope:other"}}`
				want = http.StatusBadRequest
			case "missing actor":
				payload = `{"id":"999","application_id":"111","type":2,"channel_id":"555"}`
				want = http.StatusBadRequest
			}
			r := signedRequest(private, payload, timestamp)
			if mode == "missing signature" {
				r.Header.Del("X-Signature-Ed25519")
			}
			if mode == "tampered" {
				r.Header.Set("X-Signature-Timestamp", strconv.FormatInt(time.Now().Unix()+1, 10))
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != want || calls != 0 {
				t.Fatalf("status=%d, intake calls=%d", w.Code, calls)
			}
		})
	}
}

type ackRecorder struct {
	*httptest.ResponseRecorder
	written atomic.Bool
}

func (w *ackRecorder) WriteHeader(code int) {
	w.written.Store(true)
	w.ResponseRecorder.WriteHeader(code)
}
func (w *ackRecorder) Write(body []byte) (int, error) {
	w.written.Store(true)
	return w.ResponseRecorder.Write(body)
}

func TestInteractionAckWaitsForDurableIntake(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			handler, private := interactionFixture(t, func(ctx context.Context, i Interaction) (InteractionResponse, error) {
				close(entered)
				<-release
				if fail {
					return InteractionResponse{}, errors.New("private database error")
				}
				return Acknowledge(i), nil
			})
			r := signedRequest(private,
				`{"id":"999","application_id":"111","type":2,"channel_id":"555","user":{"id":"777"}}`, time.Now())
			w := &ackRecorder{ResponseRecorder: httptest.NewRecorder()}
			go func() { handler.ServeHTTP(w, r); close(done) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("intake not called")
			}
			if w.written.Load() {
				t.Fatal("acknowledged before durable commit")
			}
			close(release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("ack did not finish")
			}
			want := http.StatusOK
			if fail {
				want = http.StatusServiceUnavailable
			}
			if w.Code != want || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("response=%s", w.Body.String())
			}
		})
	}
}

func TestExpiredIntakeDoesNotAckSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handler, private := interactionFixture(t, func(ctx context.Context, i Interaction) (InteractionResponse, error) {
		cancel()
		return Acknowledge(i), nil
	})
	r := signedRequest(private,
		`{"id":"999","application_id":"111","type":2,"channel_id":"555","user":{"id":"777"}}`, time.Now())
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r.WithContext(ctx))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("acknowledged after deadline/cancellation")
	}
}

func TestCustomIDsButtonsAndModalResponses(t *testing.T) {
	raw := callbackID(t)
	if len(raw) > 100 {
		t.Fatal("custom ID exceeds Discord limit")
	}
	id, err := DecodeCustomID(raw)
	if err != nil || id.Action != "approve" {
		t.Fatal("custom ID did not round trip")
	}
	for _, raw := range []string{"om1:agent_id:approve", raw + ":scope",
		"om1:" + id.InteractionID + ":" + strings.Repeat("x", 33)} {
		if _, err := DecodeCustomID(raw); err == nil {
			t.Fatal("accepted malformed custom ID")
		}
	}
	button := Component{Type: 2, Style: 3, Label: "Approve", CustomID: raw}
	if err := validateRows([]ActionRow{{Type: 1, Components: []Component{button}}}, false); err != nil {
		t.Fatal(err)
	}
	if err := validateRows([]ActionRow{{Type: 1, Components: []Component{button, button}}}, false); err == nil {
		t.Fatal("accepted duplicate component identities")
	}
	input := Component{Type: 4, Style: 2, Label: "Your answer", CustomID: "answer", MaxLength: 4000}
	modal := InteractionResponse{Type: 9, Data: &InteractionResponseData{Title: "Answer", CustomID: raw,
		Components: []ActionRow{{Type: 1, Components: []Component{input}}}}}
	if !validResponse(3, modal) || validResponse(5, modal) {
		t.Fatal("invalid modal response eligibility")
	}
	if validResponse(2, InteractionResponse{Type: 6}) {
		t.Fatal("slash command allowed component-only ack")
	}
}
