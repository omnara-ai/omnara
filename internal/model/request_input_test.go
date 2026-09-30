package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/stretchr/testify/require"
)

func TestRequestInputIdentityMatchesPreparedPrefixAndStaticFooter(t *testing.T) {
	for _, format := range []modelprotocol.APIFormat{
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIFormatOpenAIResponses,
	} {
		t.Run(string(format), func(t *testing.T) {
			key, allowance := "messages", "max_completion_tokens"
			if format == modelprotocol.APIFormatOpenAIResponses {
				key, allowance = "input", "max_output_tokens"
			}
			body := `{"model":"test","` + allowance + `":32000,"` + key + `":[` +
				`{"role":"system","content":"instructions"},` +
				`{"role":"user","content":"question"},{"role":"system","content":"integration targets"}]}`
			prior, err := projectRequestInput(json.RawMessage(body), format)
			if err != nil {
				t.Fatal(err)
			}
			route := strings.Repeat("a", 64)
			identity, err := prior.identity(route)
			if err != nil {
				t.Fatal(err)
			}
			if identity.ItemCount != 1 {
				t.Fatalf("conversation item count = %d; system prefix/footer must be static", identity.ItemCount)
			}
			next := strings.Replace(body, `32000`, `16000`, 1)
			next = strings.Replace(next, `{"role":"system","content":"integration targets"}`,
				`{"role":"assistant","content":"answer"},{"role":"user","content":"next"},`+
					`{"role":"system","content":"integration targets"}`, 1)
			projection, err := projectRequestInput(json.RawMessage(next), format)
			if err != nil || !projection.matches(identity, route) {
				t.Fatalf("unchanged input plus suffix must match across output clamping: %v", err)
			}
			for _, changed := range []string{
				strings.Replace(next, "instructions", "new instructions", 1),
				strings.Replace(next, "integration targets", "different targets", 1),
				strings.Replace(next, "question", "edited question", 1),
			} {
				projection, err := projectRequestInput(json.RawMessage(changed), format)
				if err != nil {
					t.Fatal(err)
				}
				if projection.matches(identity, route) {
					t.Fatal("changed measured input reused old measurement")
				}
			}
			if projection.matches(identity, strings.Repeat("b", 64)) {
				t.Fatal("changed route reused old measurement")
			}
		})
	}
}

type inputMeasurementClient struct {
	prepareForSendClient
	format modelprotocol.APIFormat
}

func (c inputMeasurementClient) APIFormat() modelprotocol.APIFormat { return c.format }

func TestOpaqueReasoningUsesMeasuredCompleteReplayOnlyWithinToolContinuation(t *testing.T) {
	for _, format := range []modelprotocol.APIFormat{
		modelprotocol.APIFormatAnthropicMessages, modelprotocol.APIFormatOpenAIResponses,
	} {
		t.Run(string(format), func(t *testing.T) {
			key := "messages"
			if format == modelprotocol.APIFormatOpenAIResponses {
				key = "input"
			}
			priorBody := json.RawMessage(`{"model":"prepare-test","` + key + `":[{"role":"user","content":"question"}]}`)
			prior, err := projectRequestInput(priorBody, format)
			require.NoError(t, err)
			route := strings.Repeat("a", 64)
			identity, err := prior.identity(route)
			require.NoError(t, err)
			replay := `[{"type":"thinking","thinking":"short visible summary","signature":"` +
				strings.Repeat("opaque", 10000) + `"},{"type":"tool_use","id":"call","name":"read","input":{}}]`
			assistant := `{"role":"assistant","content":` + replay + `}`
			result := `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":"result"}]}`
			if format == modelprotocol.APIFormatOpenAIResponses {
				replay = `[{"type":"reasoning","encrypted_content":"` + strings.Repeat("opaque", 10000) +
					`"},{"type":"function_call","call_id":"call","name":"read","arguments":"{}"}]`
				assistant = replay[1 : len(replay)-1]
				result = `{"type":"function_call_output","call_id":"call","output":"result"}`
			}
			body := json.RawMessage(`{"model":"prepare-test","` + key + `":[` +
				`{"role":"user","content":"question"},` + assistant + `,` + result + `]}`)
			source := modelcontext.Message{
				Role: modelprotocol.RoleAssistant, RequestInputIdentity: &identity, ServedProviderModelSlug: "prepare-test",
				Usage:      modelenvelope.Usage{InputTokens: 2000, OutputTokens: 5000, ReasoningTokens: 4900},
				StopReason: modelenvelope.StopReasonToolUse, ProviderReplay: json.RawMessage(replay),
			}
			bundle := modelcontext.Bundle{Messages: []modelcontext.Message{source}}
			client := inputMeasurementClient{format: format}
			prepared := PreparedRequest{Body: body, InputRouteFingerprint: route, InputTokenEstimate: 25000}
			applyRequestInputMeasurement(&prepared, client, bundle)
			require.True(t, prepared.HasMeasuredInputPrefix)
			require.InDelta(t, 7000, prepared.InputTokenEstimate, 100, "count output tokens once, never ciphertext characters")

			for _, changed := range []json.RawMessage{
				json.RawMessage(strings.Replace(string(body), `"question"`, `"changed"`, 1)),
				json.RawMessage(string(body[:len(body)-2]) + `,{"role":"user","content":"new task"}]}`),
				json.RawMessage(strings.Replace(string(body), "opaque", "modified", 1)),
			} {
				prepared = PreparedRequest{Body: changed, InputRouteFingerprint: route, InputTokenEstimate: 25000}
				applyRequestInputMeasurement(&prepared, client, bundle)
				require.False(t, prepared.HasMeasuredInputPrefix)
				require.Equal(t, 25000, prepared.InputTokenEstimate)
			}
		})
	}
}

func TestNewMediaIsCountedWithoutChargingMeasuredHistoricalImagesAgain(t *testing.T) {
	image := modelcontext.RenderedMedia{Representation: modelcontext.MediaRepresentationInline, TokenEstimate: 900,
		Media: modelcontext.ResolvedMedia{Kind: modelcontext.AttachmentKindImage, Data: []byte("image")}}
	raw := json.RawMessage(`[{"role":"user","content":[{"type":"image_url",` +
		`"image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]}]`)
	selected := inputSuffixMedia(raw, []modelcontext.RenderedMedia{image, image})
	require.Len(t, selected, 1)
	estimate := modelcontext.EstimatePreparedRequest(raw, selected)
	require.GreaterOrEqual(t, estimate, 900)
	require.Less(t, estimate, 1000)
}

func TestRequestInputIdentityPreservesMediaAndExactNumbers(t *testing.T) {
	const body = `{"model":"test","messages":[{"role":"user","content":[{"type":"image_url",` +
		`"image_url":{"url":"data:image/png;base64,AAAA"}},{"type":"text","text":"question"}]},` +
		`{"role":"assistant","tool_calls":[{"id":"call",` +
		`"function":{"arguments":{"value":9007199254740992}}}]}]}`
	prior, err := projectRequestInput(json.RawMessage(body), modelprotocol.APIFormatOpenAIChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	route := strings.Repeat("a", 64)
	identity, err := prior.identity(route)
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{
		strings.Replace(body, "AAAA", "BBBB", 1),
		strings.Replace(body, "9007199254740992", "9007199254740993", 1),
	} {
		projection, err := projectRequestInput(json.RawMessage(changed), modelprotocol.APIFormatOpenAIChatCompletions)
		if err != nil {
			t.Fatal(err)
		}
		if projection.matches(identity, route) {
			t.Fatal("changed image or integer reused old measurement")
		}
	}
}

func TestRequestInputIdentityIgnoresOnlyProtocolCacheMarkers(t *testing.T) {
	const body = `{"model":"test","max_tokens":1000,"system":[{"type":"text","text":"instructions",` +
		`"cache_control":{"type":"ephemeral"}}],"tools":[{"name":"tool",` +
		`"input_schema":{"type":"object","properties":{"cache_control":{"type":"string",` +
		`"description":"before"}}},"cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"question",` +
		`"cache_control":{"type":"ephemeral"}}]},{"role":"assistant",` +
		`"content":[{"type":"tool_use","id":"call","name":"tool",` +
		`"input":{"cache_control":"before"}}]}]}`
	prior, err := projectRequestInput(json.RawMessage(body), modelprotocol.APIFormatAnthropicMessages)
	if err != nil {
		t.Fatal(err)
	}
	route := strings.Repeat("a", 64)
	identity, err := prior.identity(route)
	if err != nil {
		t.Fatal(err)
	}
	withoutMarkers := strings.ReplaceAll(body, `,"cache_control":{"type":"ephemeral"}`, "")
	projection, err := projectRequestInput(json.RawMessage(withoutMarkers), modelprotocol.APIFormatAnthropicMessages)
	if err != nil || !projection.matches(identity, route) {
		t.Fatalf("cache marker placement changed input identity: %v", err)
	}
	for _, changed := range []string{
		strings.Replace(body, `"description":"before"`, `"description":"after"`, 1),
		strings.Replace(body, `"cache_control":"before"`, `"cache_control":"after"`, 1),
	} {
		projection, err := projectRequestInput(json.RawMessage(changed), modelprotocol.APIFormatAnthropicMessages)
		if err != nil {
			t.Fatal(err)
		}
		if projection.matches(identity, route) {
			t.Fatal("cache_control inside real schema/arguments was discarded")
		}
	}
}

func TestRequestInputIdentityInvalidatesActivatedDeferredTools(t *testing.T) {
	const body = `{"model":"test","max_tokens":1000,"tools":[{"name":"new_tool","defer_loading":true,` +
		`"input_schema":{"type":"object"}}],"messages":[{"role":"user","content":[{"type":"text",` +
		`"text":"question"}]}]}`
	prior, err := projectRequestInput(json.RawMessage(body), modelprotocol.APIFormatAnthropicMessages)
	if err != nil {
		t.Fatal(err)
	}
	route := strings.Repeat("a", 64)
	identity, err := prior.identity(route)
	if err != nil {
		t.Fatal(err)
	}
	var next map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &next); err != nil {
		t.Fatal(err)
	}
	next["messages"] = json.RawMessage(`[{"role":"user","content":[{"type":"text","text":"question"}]},{"role":"assistant",` +
		`"content":[{"type":"tool_use","id":"search","name":"tool_search","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"search",` +
		`"content":[{"type":"tool_reference","tool_name":"new_tool"}]}]}]`)
	raw, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := projectRequestInput(raw, modelprotocol.APIFormatAnthropicMessages)
	if err != nil {
		t.Fatal(err)
	}
	if projection.matches(identity, route) {
		t.Fatal("newly activated static tool schema reused the prior input measurement")
	}
}

func TestInputMediaAccountingDoesNotTreatTextOrToolArgumentsAsMedia(t *testing.T) {
	media := []modelcontext.RenderedMedia{{
		Representation: modelcontext.MediaRepresentationInline, TokenEstimate: 900,
		Media: modelcontext.ResolvedMedia{Kind: modelcontext.AttachmentKindImage, Data: []byte("image")},
	}}
	for _, raw := range []string{
		`[{"role":"user","content":"aW1hZ2U="}]`,
		`[{"role":"user","content":[{"type":"text","text":"data:image/png;base64,aW1hZ2U="}]}]`,
		`[{"role":"assistant","tool_calls":[{"function":{"arguments":{"type":"image_url",` +
			`"image_url":{"url":"data:image/png;base64,aW1hZ2U="}}}}]}]`,
		`[{"role":"assistant","content":[{"type":"tool_use","input":{"type":"image",` +
			`"source":{"type":"base64","data":"aW1hZ2U="}}}]}]`,
	} {
		require.Empty(t, inputSuffixMedia(json.RawMessage(raw), media))
	}
	for _, raw := range []string{
		`[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"aW1hZ2U="}}]}]`,
		`[{"role":"user","content":[{"type":"tool_result","content":[{"type":"document",` +
			`"source":{"type":"base64","data":"aW1hZ2U="}}]}]}]`,
		`[{"type":"function_call_output","output":[{"type":"input_image",` +
			`"image_url":"data:image/png;base64,aW1hZ2U="}]}]`,
		`[{"role":"user","content":[{"type":"input_file","file_data":"data:application/pdf;base64,aW1hZ2U="}]}]`,
		`[{"role":"user","content":[{"type":"file","file":{"file_data":"data:application/pdf;base64,aW1hZ2U="}}]}]`,
	} {
		require.Len(t, inputSuffixMedia(json.RawMessage(raw), media), 1)
	}
}
