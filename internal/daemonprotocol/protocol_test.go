package daemonprotocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var protocolTestDaemonInstanceID = uuid.MustParse("00000000-0000-4000-8000-000000000001")

func TestProcessTerminalSourceTimeContract(t *testing.T) {
	startedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(time.Second)
	tests := []struct {
		name      string
		state     ProcessState
		startedAt time.Time
		endedAt   time.Time
		valid     bool
	}{
		{name: "exited with observed end", state: ProcessStateExited, startedAt: startedAt, endedAt: endedAt, valid: true},
		{name: "exited without observed end", state: ProcessStateExited},
		{name: "failed before start", state: ProcessStateFailed, valid: true},
		{
			name:      "failed after start with observed end",
			state:     ProcessStateFailed,
			startedAt: startedAt,
			endedAt:   endedAt,
			valid:     true,
		},
		{name: "failed after start without observed end", state: ProcessStateFailed, startedAt: startedAt},
		{name: "killed with observed end", state: ProcessStateKilled, startedAt: startedAt, endedAt: endedAt, valid: true},
		{name: "killed without observed end", state: ProcessStateKilled},
		{name: "unknown without observed end", state: ProcessStateUnknown, startedAt: startedAt, valid: true},
		{name: "reversed source times", state: ProcessStateUnknown, startedAt: endedAt, endedAt: startedAt},
		{name: "nonterminal state", state: ProcessStateRunning, startedAt: startedAt, endedAt: endedAt},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ValidProcessTerminalSourceTimes(
				test.state,
				test.startedAt,
				test.endedAt,
			); got != test.valid {
				t.Fatalf("source-time validity = %t, want %t", got, test.valid)
			}
		})
	}
}

func TestSkillOfferRoundTrip(t *testing.T) {
	offer := SkillOffer{
		RequestID:         "req-1",
		SkillID:           "skl_abc",
		RevisionID:        "skr_abc",
		DownloadToken:     "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		DownloadExpiresAt: time.Now().Add(time.Minute).Unix(),
		Digest:            "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	body, err := json.Marshal(Message{Type: "skill_offer", SkillOffer: &offer})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"type":"skill_offer"`) || !strings.Contains(string(body), `"skill_id":"skl_abc"`) {
		t.Fatalf("envelope: %s", body)
	}
	var decoded Message
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.SkillOffer == nil || !reflect.DeepEqual(*decoded.SkillOffer, offer) {
		t.Fatalf("round-trip mismatch: got %+v want %+v", decoded.SkillOffer, offer)
	}
}

func TestSkillReportRoundTrip(t *testing.T) {
	report := SkillReport{
		RequestID: "req-1",
		SkillID:   "skl_abc",
		State:     "ready",
	}
	body, err := json.Marshal(Message{Type: "skill_report", SkillReport: &report})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Message
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.SkillReport == nil || !reflect.DeepEqual(*decoded.SkillReport, report) {
		t.Fatalf("round-trip mismatch: got %+v want %+v", decoded.SkillReport, report)
	}
}

func TestMessageMarshalUsesTypedEnvelope(t *testing.T) {
	body, err := json.Marshal(
		Message{
			Type:             "heartbeat",
			DaemonInstanceID: protocolTestDaemonInstanceID,
			ObservedPlatform: json.RawMessage(`{"os":"darwin"}`),
		},
	)
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, `"type":"heartbeat"`) || !strings.Contains(got, `"payload"`) {
		t.Fatalf("heartbeat envelope = %s", got)
	}
	if strings.Contains(got, `"daemon_instance_id"`+`:""`) {
		t.Fatalf("heartbeat envelope retained zero top-level fields: %s", got)
	}

	var decoded Message
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal heartbeat envelope: %v", err)
	}
	if decoded.Type != "heartbeat" || decoded.DaemonInstanceID != protocolTestDaemonInstanceID ||
		string(decoded.ObservedPlatform) != `{"os":"darwin"}` {
		t.Fatalf("decoded heartbeat = %+v", decoded)
	}
}

func TestMessageMarshalPointerUsesTypedEnvelope(t *testing.T) {
	body, err := json.Marshal(
		&Message{Type: "runtime_ended", ErrorCode: ErrorCodeInvalidRuntime, Error: "runtime ended"},
	)
	if err != nil {
		t.Fatalf("marshal runtime ended: %v", err)
	}
	if !strings.Contains(string(body), `"payload"`) ||
		!strings.Contains(string(body), `"error_code":"invalid_runtime"`) {
		t.Fatalf("runtime ended envelope = %s", body)
	}
}

func TestMessageUnmarshalRejectsLegacyFlatShape(t *testing.T) {
	var decoded Message
	err := json.Unmarshal(
		[]byte(`{"type":"heartbeat","daemon_instance_id":"`+protocolTestDaemonInstanceID.String()+`"}`),
		&decoded,
	)
	if err == nil {
		t.Fatal("legacy flat daemon message decoded successfully")
	}
}

func TestMessageUnmarshalIgnoresUnknownPayloadFields(t *testing.T) {
	var decoded Message
	err := json.Unmarshal(
		[]byte(`{"type":"process_accept","payload":{"process_id":"prc_1","event":{"type":"process_started"}}}`),
		&decoded,
	)
	if err != nil {
		t.Fatalf("decode additive unknown payload field: %v", err)
	}
	if decoded.Type != "process_accept" || decoded.ProcessID != "prc_1" {
		t.Fatalf("decoded process_accept = %+v", decoded)
	}
}

func TestMessageUnmarshalAcceptsUnknownType(t *testing.T) {
	var decoded Message
	err := json.Unmarshal([]byte(`{"type":"new_message_type","payload":{"ok":true}}`), &decoded)
	if err != nil {
		t.Fatalf("decode unknown message type: %v", err)
	}
	if decoded.Type != "new_message_type" {
		t.Fatalf("decoded unknown message = %+v", decoded)
	}
}

func TestMessageUnmarshalRejectsNullPayload(t *testing.T) {
	var decoded Message
	err := json.Unmarshal([]byte(`{"type":"heartbeat","payload":null}`), &decoded)
	if err == nil {
		t.Fatal("message with null payload decoded successfully")
	}
}

func TestMessageUnmarshalRejectsInvalidUnknownTypePayload(t *testing.T) {
	for _, body := range []string{
		`{"type":"new_message_type"}`,
		`{"type":"new_message_type","payload":null}`,
		`{"type":"new_message_type","payload":[]}`,
	} {
		var decoded Message
		if err := json.Unmarshal([]byte(body), &decoded); err == nil {
			t.Fatalf("unknown message with invalid payload decoded successfully: %s", body)
		}
	}
}

func TestMessageUnmarshalDecodesOfferPayload(t *testing.T) {
	var decoded Message
	err := json.Unmarshal(
		[]byte(
			`{"type":"action_offer","payload":{`+
				`"process_id":"prc_1","process_action_id":"act_1",`+
				`"action_kind":"read","seq":7,"payload":{"cursor":0}}}`,
		),
		&decoded,
	)
	if err != nil {
		t.Fatalf("unmarshal action offer: %v", err)
	}
	if decoded.ProcessID != "prc_1" || decoded.ProcessActionID != "act_1" || decoded.ActionOffer == nil ||
		decoded.ActionOffer.Seq != 7 {
		t.Fatalf("decoded action offer = %+v", decoded)
	}
}

func TestMessageEnvelopeRoundTripsEveryMessageType(t *testing.T) {
	exitCode := 7
	observedAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	startedAt := observedAt.Add(-time.Second)
	endedAt := observedAt.Add(time.Second)
	tests := []Message{
		{
			Type:             "heartbeat",
			DaemonInstanceID: protocolTestDaemonInstanceID,
			ObservedPlatform: json.RawMessage(`{"arch":"arm64"}`),
		},
		{Type: "heartbeat_ack", NextHeartbeatAfterMS: 10000},
		{
			Type:      "process_offer",
			ProcessID: "prc_1",
			ProcessOffer: &ProcessOffer{
				ProcessID:      "prc_1",
				IOMode:         "pty",
				Command:        "echo ok",
				ShellSelector:  "sh",
				Cwd:            "/work",
				WaitMs:         100,
				TimeoutSeconds: 30,
			},
		},
		{Type: "process_accept", ProcessID: "prc_1"},
		{Type: "process_accept_ack", ProcessID: "prc_1"},
		{Type: "process_terminate", ProcessID: "prc_1"},
		{
			Type:            "action_offer",
			ProcessID:       "prc_1",
			ProcessActionID: "act_1",
			ActionOffer: &ActionOffer{
				ProcessID:       "prc_1",
				ProcessActionID: "act_1",
				ActionKind:      "read",
				Seq:             9,
				Payload:         json.RawMessage(`{"cursor":1}`),
			},
		},
		{Type: "action_accept", ProcessID: "prc_1", ProcessActionID: "act_1"},
		{
			Type:            "action_accept_ack",
			ProcessID:       "prc_1",
			ProcessActionID: "act_1",
			ActionGrant: &ActionGrant{
				ProcessID:           "prc_1",
				ProcessActionID:     "act_1",
				ProcessState:        "running",
				DefaultOutputCursor: 4,
			},
		},
		{
			Type:     "report",
			ReportID: "rpt_action",
			Event: &ReportedEvent{
				Type:               "process_action_failed",
				ProcessID:          "prc_1",
				ProcessActionID:    "act_1",
				StateReasonCode:    "write_failed",
				StateReasonMessage: "closed",
				Result:             json.RawMessage(`{"ok":false}`),
			},
		},
		{
			Type:      "report_ack",
			ReportID:  "rpt_action",
			AckStatus: AckStatusPermanentReject,
			ErrorCode: ErrorCodeValidationFailed,
			Error:     "bad report",
		},
		{
			Type:      "report_ack",
			ReportID:  "rpt_action",
			AckStatus: AckStatusCleanupOnly,
		},
		{
			Type:            "error",
			ErrorCode:       ErrorCodeStorageUnavailable,
			Error:           "database unavailable",
			ProcessID:       "prc_1",
			ProcessActionID: "act_1",
		},
		{Type: "runtime_ended", ErrorCode: ErrorCodeInvalidRuntime, Error: "runtime ended"},
		{
			Type:     "report",
			ReportID: "rpt_process",
			Event: &ReportedEvent{
				Type:               "process_finished",
				ProcessID:          "prc_1",
				State:              "failed",
				ExitCode:           &exitCode,
				ExitSignal:         "SIGTERM",
				StateReasonCode:    "nonzero_exit",
				StateReasonMessage: "exit 7",
				StartedAt:          startedAt,
				EndedAt:            endedAt,
			},
		},
	}
	for _, tt := range tests {
		t.Run(string(tt.Type), func(t *testing.T) {
			body, err := json.Marshal(tt)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got Message
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("unmarshal %s: %v\n%s", tt.Type, err, body)
			}
			if !reflect.DeepEqual(got, tt) {
				t.Fatalf("round trip mismatch\nwant: %#v\n got: %#v\njson: %s", tt, got, body)
			}
		})
	}
}
