package model

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelcontext"
)

func TestProviderImageTokenEstimates(t *testing.T) {
	media := modelcontext.ResolvedMedia{Data: pngImage(t, 1024, 1024)}
	tests := []struct {
		name string
		got  int
		want int
	}{
		{
			name: "anthropic patches",
			got:  AnthropicImageTokenEstimate("claude-sonnet-4-6", media),
			want: 1_369,
		},
		{name: "openai original patches", got: OpenAIImageTokenEstimate("gpt-5.6", media), want: 1_229},
		{name: "openai bounded patches", got: OpenAIImageTokenEstimate("gpt-5-mini", media), want: 1_229},
		{name: "openai GPT-5.2 patches", got: OpenAIImageTokenEstimate("gpt-5.2", media), want: 1_229},
		{name: "openai Codex patches", got: OpenAIImageTokenEstimate("gpt-5.3-codex", media), want: 1_229},
		{name: "bedrock openai slug", got: OpenAIImageTokenEstimate("openai.gpt-5.6-sol", media), want: 1_229},
		{name: "openai GPT-5 snapshot tiles", got: OpenAIImageTokenEstimate("gpt-5-2025-08-07", media), want: 630},
		{name: "openai GPT-5.1 tiles", got: OpenAIImageTokenEstimate("gpt-5.1", media), want: 630},
		{name: "openai expensive tiles", got: OpenAIImageTokenEstimate("gpt-4o-mini", media), want: 25_501},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("estimate = %d, want %d", test.got, test.want)
			}
		})
	}
}

func TestAnthropicImageTokenEstimateMatchesDocumentedResolutionTiers(t *testing.T) {
	tests := []struct {
		name              string
		providerModelSlug string
		width             int
		height            int
		want              int
	}{
		{
			name:              "standard 1080p resize",
			providerModelSlug: "claude-sonnet-4-6",
			width:             1_920,
			height:            1_080,
			want:              1_560,
		},
		{
			name:              "standard three megapixel resize",
			providerModelSlug: "anthropic/claude-sonnet-4.5",
			width:             2_000,
			height:            1_500,
			want:              1_564,
		},
		{
			name:              "snapshot date is not a minor version",
			providerModelSlug: "claude-sonnet-4-20250514",
			width:             3_840,
			height:            2_160,
			want:              1_560,
		},
		{
			name:              "standard tier long edge",
			providerModelSlug: "claude-sonnet-4-6",
			width:             100,
			height:            3_000,
			want:              112,
		},
		{
			name:              "bedrock provider slug",
			providerModelSlug: "anthropic.claude-haiku-4-5",
			width:             1_920,
			height:            1_080,
			want:              1_560,
		},
		{
			name:              "high resolution 1080p",
			providerModelSlug: "anthropic/claude-opus-4.8",
			width:             1_920,
			height:            1_080,
			want:              2_691,
		},
		{
			name:              "high resolution 4K resize",
			providerModelSlug: "claude-sonnet-5",
			width:             3_840,
			height:            2_160,
			want:              4_784,
		},
		{
			name:              "high resolution tier long edge",
			providerModelSlug: "claude-sonnet-5",
			width:             100,
			height:            3_000,
			want:              368,
		},
		{
			name:              "unknown routed alias stays conservative",
			providerModelSlug: "private-deployment",
			width:             3_840,
			height:            2_160,
			want:              4_784,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := anthropicImageTokenEstimateForDimensions(
				test.providerModelSlug,
				test.width,
				test.height,
			)
			if got != test.want {
				t.Fatalf("estimate = %d, want %d", got, test.want)
			}
		})
	}
}

func TestOpenAIImageTokenEstimateMatchesDocumentedPatchExamples(t *testing.T) {
	tests := []struct {
		name              string
		providerModelSlug string
		width             int
		height            int
		want              int
	}{
		{
			name:              "GPT-5.6 preserves original patches",
			providerModelSlug: "gpt-5.6",
			width:             1_800,
			height:            2_400,
			want:              5_130,
		},
		{
			name:              "finite patch model resizes within budget",
			providerModelSlug: "gpt-5.2",
			width:             1_800,
			height:            2_400,
			want:              3_687,
		},
		{
			name:              "finite patch model long edge",
			providerModelSlug: "gpt-5.2",
			width:             100,
			height:            3_000,
			want:              231,
		},
		{
			name:              "original detail model long edge",
			providerModelSlug: "gpt-5.5",
			width:             100,
			height:            7_000,
			want:              677,
		},
		{
			name:              "GPT-5.4 patch budget",
			providerModelSlug: "gpt-5.4",
			width:             2_048,
			height:            2_048,
			want:              3_000,
		},
		{
			name:              "GPT-5.5 original patch budget",
			providerModelSlug: "gpt-5.5",
			width:             4_096,
			height:            4_096,
			want:              12_000,
		},
		{
			name:              "tile model keeps a narrow image at native scale",
			providerModelSlug: "gpt-4o",
			width:             100,
			height:            3_000,
			want:              765,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := openAIImageTokenEstimateForDimensions(
				test.providerModelSlug,
				test.width,
				test.height,
			)
			if got != test.want {
				t.Fatalf("estimate = %d, want %d", got, test.want)
			}
		})
	}
}

func TestOpenAICompatibleClaudeUsesDownstreamImageTokenizer(t *testing.T) {
	tests := []struct {
		name              string
		providerModelSlug string
		want              int
	}{
		{name: "versioned", providerModelSlug: "anthropic/claude-sonnet-4.6", want: 1_560},
		{name: "version-first", providerModelSlug: "anthropic/claude-3.5-sonnet", want: 1_560},
		{
			name:              "version-first with OpenRouter variant",
			providerModelSlug: "anthropic/claude-3.5-sonnet:thinking",
			want:              1_560,
		},
		{name: "versionless alias", providerModelSlug: "~anthropic/claude-sonnet-latest", want: 4_784},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := openAIImageTokenEstimateForDimensions(
				test.providerModelSlug,
				3_840,
				2_160,
			)
			if got != test.want {
				t.Fatalf("routed Claude estimate = %d, want %d", got, test.want)
			}
		})
	}
}

func TestAmbiguousOpenAICompatibleAliasUsesGenericImageFallback(t *testing.T) {
	generic := openAIImageTokenEstimateForDimensions(
		"private-deployment",
		3_840,
		2_160,
	)
	for _, alias := range []string{"company-claude-4-router", "claude-4-router"} {
		ambiguous := openAIImageTokenEstimateForDimensions(
			alias,
			3_840,
			2_160,
		)
		if ambiguous != generic {
			t.Errorf("%s estimate = %d, want generic fallback %d", alias, ambiguous, generic)
		}
	}
}

func TestOpenAICompatibleFallbacksUseLargestImageEstimate(t *testing.T) {
	media := modelcontext.ResolvedMedia{Data: pngImage(t, 3_840, 2_160)}
	got := OpenAIImageTokenEstimateForModels(
		[]string{"anthropic/claude-sonnet-4.6", "openai/gpt-5.6"},
		media,
	)
	if got != 9_792 {
		t.Fatalf("fallback estimate = %d, want 9792", got)
	}
}

func TestUnknownOpenAIModelUsesWorstKnownAspectRatioFormula(t *testing.T) {
	media := modelcontext.ResolvedMedia{Data: pngImage(t, 100, 10_000)}
	worstKnown := OpenAIImageTokenEstimate("gpt-4o-mini", media)
	if got := OpenAIImageTokenEstimate("routed-model", media); got < worstKnown {
		t.Fatalf("unknown routed estimate = %d, want at least worst known %d", got, worstKnown)
	}
}

func TestProviderImageTokenEstimateFallbacksAreConservative(t *testing.T) {
	media := modelcontext.ResolvedMedia{Data: []byte("not an image")}
	if got := AnthropicImageTokenEstimate("claude-sonnet-4-6", media); got != anthropicStandardMaxImageTokens {
		t.Fatalf("standard anthropic fallback = %d, want %d", got, anthropicStandardMaxImageTokens)
	}
	if got := AnthropicImageTokenEstimate("private-deployment", media); got != anthropicHighMaxImageTokens {
		t.Fatalf("unknown anthropic fallback = %d, want %d", got, anthropicHighMaxImageTokens)
	}
	if got := OpenAIImageTokenEstimate("unknown", media); got != openAIUnknownImageTokens {
		t.Fatalf("openai fallback = %d, want %d", got, openAIUnknownImageTokens)
	}
}

func pngImage(t *testing.T, width, height int) []byte {
	t.Helper()
	imageValue := image.NewRGBA(image.Rect(0, 0, width, height))
	imageValue.Set(0, 0, color.White)
	var body bytes.Buffer
	if err := png.Encode(&body, imageValue); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return body.Bytes()
}
