package daemonprotocol

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonversion"
	"github.com/omnara-ai/omnara/internal/processaction"
)

func actionOfferMessage(kind ProcessActionKind, payload string) Message {
	return Message{
		Type:            MessageActionOffer,
		ProcessID:       "prc_1",
		ProcessActionID: "pra_1",
		ActionOffer: &ActionOffer{
			ProcessID:       "prc_1",
			ProcessActionID: "pra_1",
			ActionKind:      kind,
			Seq:             3,
			Payload:         json.RawMessage(payload),
		},
	}
}

func encodedOfferPayload(t *testing.T, daemonVersion string, message Message) map[string]any {
	t.Helper()
	data, err := encodeForDaemon(daemonVersion, message)
	if err != nil {
		t.Fatalf("encodeForDaemon: %v", err)
	}
	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode encoded message: %v", err)
	}
	if decoded.ActionOffer == nil {
		t.Fatalf("encoded message has no action offer: %s", data)
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded.ActionOffer.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return payload
}

func TestEncodeForDaemonClampsLongReadWaitsForOlderDaemons(t *testing.T) {
	longRead := actionOfferMessage(ProcessActionRead, `{"cursor":7,"max_bytes":4096,"wait_ms":300000}`)
	for _, version := range []string{"0.2.5", "0.1.9", "", "not-a-version"} {
		payload := encodedOfferPayload(t, version, longRead)
		if payload["wait_ms"] != float64(processaction.LegacyMaxWaitMilliseconds) {
			t.Fatalf("version %q: wait_ms = %v, want %d", version, payload["wait_ms"], processaction.LegacyMaxWaitMilliseconds)
		}
		if payload["cursor"] != float64(7) || payload["max_bytes"] != float64(4096) {
			t.Fatalf("version %q: other fields changed: %v", version, payload)
		}
		clamped, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := processaction.DecodeReadPayload(clamped); err != nil {
			t.Fatalf("version %q: clamped payload is invalid: %v", version, err)
		}
	}
	const original = `{"cursor":7,"max_bytes":4096,"wait_ms":300000}`
	if string(longRead.ActionOffer.Payload) != original {
		t.Fatalf("encodeForDaemon mutated the caller's offer: %s", longRead.ActionOffer.Payload)
	}
}

func TestEncodeForDaemonKeepsLongReadWaitsForCurrentDaemons(t *testing.T) {
	longRead := actionOfferMessage(ProcessActionRead, `{"wait_ms":300000}`)
	for _, version := range []string{processaction.LongWaitDaemonVersion, "0.3.0", "1.0.0", daemonversion.Development} {
		if got := encodedOfferPayload(t, version, longRead)["wait_ms"]; got != float64(300000) {
			t.Fatalf("version %q: wait_ms = %v, want 300000", version, got)
		}
	}
}

func TestEncodeForDaemonLeavesOtherOffersUnchanged(t *testing.T) {
	shortRead := actionOfferMessage(ProcessActionRead, `{"wait_ms":5000}`)
	if got := encodedOfferPayload(t, "0.2.5", shortRead)["wait_ms"]; got != float64(5000) {
		t.Fatalf("short read wait_ms = %v, want 5000", got)
	}
	write := actionOfferMessage(ProcessActionWrite, `{"data":"x","wait_ms":300000}`)
	if got := encodedOfferPayload(t, "0.2.5", write)["wait_ms"]; got != float64(300000) {
		t.Fatalf("write action wait_ms = %v, want unchanged", got)
	}
}

func TestEncodeForDaemonStripsOutputWaitForOlderDaemons(t *testing.T) {
	read := actionOfferMessage(
		ProcessActionRead,
		`{"max_bytes":16384,"wait_ms":5000,"wait_for_exit":true,"quiet_ms":1000,"head_bytes":2048}`,
	)
	for _, version := range []string{"0.2.8", "0.2.5", ""} {
		payload := encodedOfferPayload(t, version, read)
		for _, name := range []string{"wait_for_exit", "quiet_ms", "head_bytes"} {
			if _, ok := payload[name]; ok {
				t.Fatalf("version %q kept %s: %v", version, name, payload)
			}
		}
		if payload["max_bytes"] != float64(16384) || payload["wait_ms"] != float64(5000) {
			t.Fatalf("version %q: other fields changed: %v", version, payload)
		}
	}
	for _, version := range []string{processaction.OutputWindowDaemonVersion, daemonversion.Development} {
		payload := encodedOfferPayload(t, version, read)
		if payload["wait_for_exit"] != true || payload["quiet_ms"] != float64(1000) ||
			payload["head_bytes"] != float64(2048) {
			t.Fatalf("version %q dropped output wait: %v", version, payload)
		}
	}
}
