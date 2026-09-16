package channelconnector

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSendResultSeparatesMessageLocationFromReplyDestination(t *testing.T) {
	t.Parallel()
	child := `"reply_channel":{"implementation_key":"slack_thread","provider_ref":"C1:1.000001","provider_ref_kind":"thread"}`
	for _, location := range []MessageLocation{MessageAtDestination, MessageAtReplyChannel} {
		t.Run(string(location), func(t *testing.T) {
			t.Parallel()
			raw := json.RawMessage(`{"publication":"published","message_channel":"` + string(location) + `","message_id":"1.000001",` + child + `}`)
			result, err := DecodeSendResult(raw)
			require.NoError(t, err)
			require.Equal(t, location, result.MessageChannel)
			require.Equal(t, MessagePublished, result.Publication)
			require.Equal(t, "1.000001", result.MessageID)
			require.Equal(t, "C1:1.000001", result.ReplyChannel.ProviderRef)
		})
	}
	_, err := DecodeSendResult(json.RawMessage(`{"publication":"draft","message_channel":"destination"}`))
	require.NoError(t, err, "an existing conversation needs no new reply destination")
}

func TestSendResultPreservesKnownPublicationWithUnavailableContinuation(t *testing.T) {
	for _, publication := range []string{"published", "draft"} {
		raw := json.RawMessage(`{"publication":"` + publication + `","message_channel":"destination",` +
			`"message_id":"known-message","continuation_error":{"code":"reply_channel_unavailable",` +
			`"message":"Message exists, but its reply channel is unavailable. Do not resend."}}`)
		result, err := DecodeSendResult(raw)
		require.NoError(t, err)
		require.Equal(t, MessagePublication(publication), result.Publication)
		require.Equal(t, "known-message", result.MessageID)
		require.Nil(t, result.ReplyChannel)
		require.Equal(t, "reply_channel_unavailable", result.ContinuationError.Code)
	}
	for _, suffix := range []string{
		`"continuation_error":null`,
		`"continuation_error":{"code":"reply_channel_unavailable"}`,
		`"continuation_error":{"code":"","message":"Not available"}`,
		`"continuation_error":{"code":"reply_channel_unavailable","message":" "}`,
		`"continuation_error":{"code":"reply_channel_unavailable","message":"bad\u0000text"}`,
		`"continuation_error":{"code":"reply_channel_unavailable","message":"Not available","extra":true}`,
		`"reply_channel":{"implementation_key":"thread","provider_ref":"thread","provider_ref_kind":"thread"},` +
			`"continuation_error":{"code":"reply_channel_unavailable","message":"Not available"}`,
	} {
		_, err := DecodeSendResult(json.RawMessage(`{"publication":"published","message_channel":"destination",` + suffix + `}`))
		require.Error(t, err, suffix)
	}
}

func TestSendResultRejectsUntrustworthyReferences(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"unknown publication":  `{"publication":"delivered","message_channel":"destination"}`,
		"missing location":     `{"publication":"published"}`,
		"missing child":        `{"publication":"published","message_channel":"reply_channel"}`,
		"child outside shape":  `{"publication":"published","message_channel":"destination","reply_channel":{"implementation_key":"thread","provider_ref":"C:1","provider_ref_kind":"thread","project_id":"other"}}`,
		"duplicate metadata":   `{"publication":"published","message_channel":"destination","metadata":{"id":1,"id":2}}`,
		"unsafe metadata":      `{"publication":"published","message_channel":"destination","metadata":{"x":1e1000000}}`,
		"wrong implementation": `{"publication":"published","message_channel":"reply_channel","reply_channel":{"implementation_key":"../thread","provider_ref":"C:1","provider_ref_kind":"thread"}}`,
		"unsafe ID":            `{"publication":"published","message_channel":"destination","message_id":"bad\u0000id"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeSendResult(json.RawMessage(raw))
			require.Error(t, err)
		})
	}
	// Distinct fields may have identical values but must keep their own bounds.
	longKind := strings.Repeat("x", 129)
	raw, err := json.Marshal(SendResult{
		Publication: MessagePublished, MessageChannel: MessageAtReplyChannel,
		ReplyChannel: &ReplyDestination{
			ImplementationKey: "thread", ProviderRef: longKind, ProviderRefKind: longKind, DisplayName: longKind,
		},
	})
	require.NoError(t, err)
	_, err = DecodeSendResult(raw)
	require.Error(t, err)
}

func TestHistoryPreservesPartialAttachmentObservationsAndEmptyPages(t *testing.T) {
	t.Parallel()
	page := ProviderReadResult{
		Messages: []ProviderMessageObservation{{MessageID: "file-message", Publication: MessagePublished}},
		Coverage: HistoryPartial, CoverageReason: "Attachment content is unavailable.",
		NextCursor: strings.Repeat("c", 4096),
	}
	raw, err := json.Marshal(page)
	require.NoError(t, err)
	result, err := DecodeReadResult(raw, 1)
	require.NoError(t, err)
	require.Empty(t, result.Messages[0].Content.Text)
	require.Empty(t, result.Messages[0].Content.ArtifactIDs)
	require.Equal(t, page.NextCursor, result.NextCursor)
	result, err = DecodeReadResult(json.RawMessage(`{"messages":[],"coverage":"complete"}`), 1)
	require.NoError(t, err)
	require.NotNil(t, result.Messages)
	require.Empty(t, result.NextCursor)
}

func TestHistoryRejectsMisleadingCoverageAndMalformedReferences(t *testing.T) {
	t.Parallel()
	message := ProviderMessageObservation{Content: Message{Text: "hello"}, Publication: MessagePublished}
	for name, mutate := range map[string]func(*ProviderReadResult){
		"too many messages":        func(p *ProviderReadResult) { p.Messages = append(p.Messages, message) },
		"null messages":            func(p *ProviderReadResult) { p.Messages = nil },
		"unknown coverage":         func(p *ProviderReadResult) { p.Coverage = "everything" },
		"unexplained partial":      func(p *ProviderReadResult) { p.Coverage = HistoryPartial },
		"unavailable but complete": func(p *ProviderReadResult) { p.Messages[0].Content = Message{} },
		"oversized cursor":         func(p *ProviderReadResult) { p.NextCursor = strings.Repeat("c", 4097) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			page := ProviderReadResult{Messages: []ProviderMessageObservation{message}, Coverage: HistoryComplete}
			mutate(&page)
			raw, err := json.Marshal(page)
			require.NoError(t, err)
			_, err = DecodeReadResult(raw, 1)
			require.Error(t, err)
		})
	}
	for _, content := range []string{`null`, `[]`} {
		raw := json.RawMessage(`{"messages":[{"content":` + content + `,"publication":"published"}],"coverage":"partial","coverage_reason":"Missing attachment."}`)
		_, err := DecodeReadResult(raw, 1)
		require.Error(t, err, "unavailable observations still require an explicit content object")
	}
}

func TestHistoryReturnsProviderReplyFactsWithoutInventingPublicChannels(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"messages":[{"content":{"text":"Root"},"publication":"published",` +
		`"message_id":"1.000001","reply_channel":{"implementation_key":"slack_thread",` +
		`"provider_ref":"C1:1.000001","provider_ref_kind":"thread"},` +
		`"reply_to":{"message_id":"0.000001"}}],"coverage":"complete"}`)
	page, err := DecodeReadResult(raw, 1)
	require.NoError(t, err)
	require.Equal(t, "C1:1.000001", page.Messages[0].ReplyChannel.ProviderRef)
	require.Nil(t, page.Messages[0].ReplyTo.Destination, "omitted address refers to the requested conversation")
	for _, injected := range []string{
		`"channel_id":"another-public-channel"`,
		`"reply_channel_id":"another-public-channel"`,
		`"reply_to":{"message_id":"id","destination":{"implementation_key":"../private","provider_ref":"C","provider_ref_kind":"thread"}}`,
		`"reply_channel":{"implementation_key":"slack_thread","provider_ref":"C","provider_ref_kind":"bad\u0000kind"}`,
	} {
		_, err := DecodeReadResult(json.RawMessage(`{"messages":[{"content":{"text":"body"},`+
			`"publication":"published",`+injected+`}],"coverage":"complete"}`), 1)
		require.Error(t, err)
	}
}

func TestChannelResultsRejectCaseAliasesAndNulls(t *testing.T) {
	t.Parallel()
	for name, field := range map[string]string{
		"case alias":    `"Publication":"draft"`,
		"null scalar":   `"message_id":null`,
		"null child":    `"reply_channel":null`,
		"nested null":   `"reply_channel":{"implementation_key":"thread","provider_ref":"C:1","provider_ref_kind":null}`,
		"unicode alias": `"meſſage_id":"other"`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeSendResult(json.RawMessage(`{"publication":"published","message_channel":"destination",` + field + `}`))
			require.Error(t, err)
		})
	}
	for _, content := range []string{`{"text":null}`, `{"text":"hello","Text":"changed"}`, `{"artifact_ids":[null]}`} {
		_, err := DecodeReadResult(json.RawMessage(`{"messages":[{"content":`+content+`,"publication":"published"}],"coverage":"partial","coverage_reason":"Unavailable file"}`), 1)
		require.Error(t, err)
	}
	result, err := DecodeSendResult(json.RawMessage(`{"publication":"published","message_channel":"destination","metadata":{"ProviderKey":null,"nested":{"UPPER":true}}}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"ProviderKey":null,"nested":{"UPPER":true}}`, string(result.Metadata))
	_, err = DecodeInteractionResult(json.RawMessage(`{"message_id":"original","Message_ID":"changed"}`))
	require.Error(t, err)
}

func TestHistoryPreservesWhitespaceContent(t *testing.T) {
	t.Parallel()
	page, err := DecodeReadResult(json.RawMessage(`{"messages":[{"content":{"text":" \n\t"},"publication":"published"}],"coverage":"complete"}`), 1)
	require.NoError(t, err)
	require.Equal(t, " \n\t", page.Messages[0].Content.Text)
	require.Error(t, page.Messages[0].Content.Validate(), "sending a blank message remains invalid")
}
