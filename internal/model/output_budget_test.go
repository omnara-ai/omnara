package model

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestOutputBudgetSeparatesAdmissionFromWireAllowance(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		window, allowance, input, minimum int
		reserveFull                       bool
		required                          bool
		wantAllowance, wantUsable         int
		wantOver                          bool
		wantCode                          string
	}{
		{
			name:          "large ceiling small input",
			window:        200000,
			allowance:     128000,
			input:         1000,
			wantAllowance: 128000,
			wantUsable:    159040,
		},
		{
			name:          "large ceiling long input",
			window:        200000,
			allowance:     128000,
			input:         120000,
			wantAllowance: 71808,
			wantUsable:    120000,
		},
		{name: "unknown capacity", window: 200000, input: 1000, wantUsable: 159040},
		{name: "unknown still reserves headroom", window: 200000, input: 180000, wantUsable: 159040, wantOver: true},
		{
			name:          "explicit small allowance",
			window:        200000,
			allowance:     8192,
			input:         1000,
			wantAllowance: 8192,
			wantUsable:    183616,
		},
		{name: "small window", window: 1000, allowance: 900, input: 400, wantAllowance: 550, wantUsable: 400},
		{
			name:          "small window needs compaction",
			window:        1000,
			allowance:     900,
			input:         500,
			wantAllowance: 900,
			wantUsable:    450,
			wantOver:      true,
		},
		{
			name:          "thinking minimum exceeds headroom",
			window:        100000,
			allowance:     64000,
			input:         40000,
			minimum:       48001,
			wantAllowance: 55000,
			wantUsable:    40000,
		},
		{
			name:          "thinking needs compaction",
			window:        100000,
			allowance:     64000,
			input:         50000,
			minimum:       48001,
			wantAllowance: 64000,
			wantUsable:    46999,
			wantOver:      true,
		},
		{
			name:      "configured allowance below thinking",
			window:    100000,
			allowance: 32000,
			input:     1000,
			minimum:   48001,
			wantCode:  OutputTokenLimitIncompatibleCode,
		},
		{
			name:      "thinking cannot fit context",
			window:    1000,
			allowance: 2000,
			input:     10,
			minimum:   1001,
			wantCode:  InvalidOutputTokenConfigurationCode,
		},
		{
			name:     "required unknown allowance",
			window:   100000,
			input:    1000000,
			required: true,
			minimum:  1,
			wantCode: OutputTokenLimitRequiredCode,
		},
		{
			name:          "compaction reserves whole allowance",
			window:        100000,
			allowance:     64000,
			reserveFull:   true,
			input:         40000,
			wantAllowance: 64000,
			wantUsable:    31000,
			wantOver:      true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &budgetPrepareClient{
				outputLimitPrepareClient: outputLimitPrepareClient{
					prepareForSendClient: prepareForSendClient{
						capabilities: Capabilities{
							ContextWindowTokens: tc.window,
						},
					},
					limits: OutputTokenLimits{Minimum: tc.minimum, Required: tc.required},
				}, estimate: tc.input,
			}
			prepared, err := PrepareForSend(context.Background(), client, PrepareForSendInput{
				Policy: RequestPolicy{
					MaxOutputTokens: tc.allowance,
				}, ReserveFullOutputAllowance: tc.reserveFull, ErrorSource: "budget-test",
			})
			if tc.wantCode != "" {
				var providerErr ProviderError
				if !errors.As(
					err,
					&providerErr,
				) ||
					providerErr.Code != tc.wantCode ||
					providerErr.Kind != ErrorKindInvalidRequest {
					t.Fatalf("error = %v, want %s", err, tc.wantCode)
				}
				if client.prepareCalls != 0 {
					t.Fatal("invalid configuration reached provider preparation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var wire RequestPolicy
			if err := json.Unmarshal(prepared.Body, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.MaxOutputTokens != tc.wantAllowance ||
				prepared.MaxOutputTokens != tc.wantAllowance ||
				prepared.InputBudget.UsableInputTokens != tc.wantUsable ||
				prepared.InputBudget.OverBudget() != tc.wantOver {
				t.Fatalf(
					"wire = %s, prepared = %+v, want allowance=%d usable=%d over=%v",
					prepared.Body,
					prepared,
					tc.wantAllowance,
					tc.wantUsable,
					tc.wantOver,
				)
			}
			if tc.wantAllowance == 0 && string(prepared.Body) != "{}" {
				t.Fatalf("unknown allowance was serialized: %s", prepared.Body)
			}
		})
	}
}

type budgetPrepareClient struct {
	outputLimitPrepareClient
	estimate int
}

func (c *budgetPrepareClient) Prepare(_ context.Context, input PrepareInput) (PreparedRequest, error) {
	c.prepareCalls++
	body, err := json.Marshal(input.Policy)
	return PreparedRequest{Body: body, InputTokenEstimate: c.estimate}, err
}
