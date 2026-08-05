package model

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"strconv"
	"strings"

	"github.com/omnara-ai/omnara/internal/modelcontext"
	_ "golang.org/x/image/webp"
)

const (
	anthropicImagePatchSize         = 28
	anthropicStandardMaxImageEdge   = 1_568
	anthropicStandardMaxImageTokens = 1_568
	anthropicHighMaxImageEdge       = 2_576
	anthropicHighMaxImageTokens     = 4_784
	openAIUnknownImageTokens        = 25_501
)

type imageResolutionLimits struct {
	maxEdge   int
	maxTokens int
}

// AnthropicImageTokenEstimate follows https://platform.claude.com/docs/en/build-with-claude/vision.
func AnthropicImageTokenEstimate(
	providerModelSlug string,
	media modelcontext.ResolvedMedia,
) int {
	width, height, ok := imageDimensions(media.Data)
	if !ok {
		return anthropicImageResolutionLimits(providerModelSlug).maxTokens
	}
	return anthropicImageTokenEstimateForDimensions(providerModelSlug, width, height)
}

func anthropicImageTokenEstimateForDimensions(providerModelSlug string, width, height int) int {
	return resizedImagePatchTokens(
		width,
		height,
		anthropicImagePatchSize,
		anthropicImageResolutionLimits(providerModelSlug),
	)
}

func anthropicImageResolutionLimits(providerModelSlug string) imageResolutionLimits {
	major, minor, recognized := claudeModelVersion(providerModelSlug)
	if recognized && (major < 4 || major == 4 && minor < 7) {
		return imageResolutionLimits{
			maxEdge:   anthropicStandardMaxImageEdge,
			maxTokens: anthropicStandardMaxImageTokens,
		}
	}
	return imageResolutionLimits{
		maxEdge:   anthropicHighMaxImageEdge,
		maxTokens: anthropicHighMaxImageTokens,
	}
}

func claudeModelVersion(providerModelSlug string) (int, int, bool) {
	name := normalizedProviderModelName(providerModelSlug)
	if !strings.HasPrefix(name, "claude-") {
		return 0, 0, false
	}
	numberParts := strings.FieldsFunc(name[len("claude-"):], func(value rune) bool {
		return value < '0' || value > '9'
	})
	if len(numberParts) == 0 || len(numberParts[0]) > 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(numberParts[0])
	if err != nil {
		return 0, 0, false
	}
	minor := 0
	if len(numberParts) > 1 && len(numberParts[1]) <= 2 {
		minor, err = strconv.Atoi(numberParts[1])
		if err != nil {
			return 0, 0, false
		}
	}
	return major, minor, true
}

func resizedImagePatchTokens(
	width, height, patchSize int,
	limits imageResolutionLimits,
) int {
	fits := func(candidateWidth, candidateHeight int) bool {
		patchesWide := ceilDiv(candidateWidth, patchSize)
		patchesHigh := ceilDiv(candidateHeight, patchSize)
		return patchesWide*patchSize <= limits.maxEdge &&
			patchesHigh*patchSize <= limits.maxEdge &&
			patchesWide*patchesHigh <= limits.maxTokens
	}
	if fits(width, height) {
		return ceilDiv(width, patchSize) * ceilDiv(height, patchSize)
	}
	if height > width {
		width, height = height, width
	}
	aspectRatio := float64(width) / float64(height)
	low, high := 1, width
	for low+1 < high {
		middle := low + (high-low)/2
		candidateHeight := max(int(math.Round(float64(middle)/aspectRatio)), 1)
		if fits(middle, candidateHeight) {
			low = middle
		} else {
			high = middle
		}
	}
	resizedHeight := max(int(math.Round(float64(low)/aspectRatio)), 1)
	return ceilDiv(low, patchSize) * ceilDiv(resizedHeight, patchSize)
}

// OpenAIImageTokenEstimate follows https://developers.openai.com/api/docs/guides/images-vision.
func OpenAIImageTokenEstimate(providerModelSlug string, media modelcontext.ResolvedMedia) int {
	return OpenAIImageTokenEstimateForModels([]string{providerModelSlug}, media)
}

func OpenAIImageTokenEstimateForModels(
	providerModelSlugs []string,
	media modelcontext.ResolvedMedia,
) int {
	width, height, ok := imageDimensions(media.Data)
	largest := 0
	for _, providerModelSlug := range providerModelSlugs {
		var estimate int
		if ok {
			estimate = openAIImageTokenEstimateForDimensions(providerModelSlug, width, height)
		} else if isClaudeFamilyModelSlug(providerModelSlug) {
			estimate = anthropicImageResolutionLimits(providerModelSlug).maxTokens
		} else {
			estimate = openAIUnknownImageTokens
		}
		largest = max(largest, estimate)
	}
	if largest == 0 {
		return openAIUnknownImageTokens
	}
	return largest
}

func openAIImageTokenEstimateForDimensions(providerModelSlug string, width, height int) int {
	name := normalizedProviderModelName(providerModelSlug)
	if isClaudeFamilyModelSlug(name) {
		return anthropicImageTokenEstimateForDimensions(providerModelSlug, width, height)
	}
	patches := ceilDiv(width, 32) * ceilDiv(height, 32)

	switch {
	case strings.HasPrefix(name, "gpt-5.6"):
		return patches
	case strings.HasPrefix(name, "gpt-5.5"):
		return boundedOpenAIPatchCount(width, height, 10_000, 6_000)
	case strings.HasPrefix(name, "gpt-5.4-mini"), strings.HasPrefix(name, "gpt-5-mini"):
		return multipliedPatchTokens(boundedOpenAIPatchCount(width, height, 1_536, 2_048), 1.62)
	case strings.HasPrefix(name, "gpt-5.4-nano"), strings.HasPrefix(name, "gpt-5-nano"):
		return multipliedPatchTokens(boundedOpenAIPatchCount(width, height, 1_536, 2_048), 2.46)
	case strings.HasPrefix(name, "gpt-5.4"):
		return boundedOpenAIPatchCount(width, height, 2_500, 2_048)
	case strings.HasPrefix(name, "gpt-4.1-mini"):
		return multipliedPatchTokens(boundedOpenAIPatchCount(width, height, 1_536, 2_048), 1.62)
	case strings.HasPrefix(name, "gpt-4.1-nano"):
		return multipliedPatchTokens(boundedOpenAIPatchCount(width, height, 1_536, 2_048), 2.46)
	case strings.HasPrefix(name, "o4-mini"):
		return multipliedPatchTokens(boundedOpenAIPatchCount(width, height, 1_536, 2_048), 1.72)
	case strings.HasPrefix(name, "gpt-5.2"),
		strings.HasPrefix(name, "gpt-5.3-codex"),
		strings.HasPrefix(name, "gpt-5-codex-mini"),
		strings.HasPrefix(name, "gpt-5.1-codex-mini"):
		return boundedOpenAIPatchCount(width, height, 1_536, 2_048)
	case isGPT5TileModel(name):
		return tileImageTokens(width, height, 70, 140)
	case strings.HasPrefix(name, "gpt-4o-mini"):
		return tileImageTokens(width, height, 2_833, 5_667)
	case strings.HasPrefix(name, "gpt-4o"),
		strings.HasPrefix(name, "gpt-4.1"),
		strings.HasPrefix(name, "gpt-4.5"):
		return tileImageTokens(width, height, 85, 170)
	case name == "o1" || strings.HasPrefix(name, "o1-") || name == "o3" || strings.HasPrefix(name, "o3-"):
		return tileImageTokens(width, height, 75, 150)
	case strings.HasPrefix(name, "computer-use-preview"):
		return tileImageTokens(width, height, 65, 129)
	default:
		return max(
			max(
				max(patches, multipliedPatchTokens(patches, 2.46)),
				openAIUnknownImageTokens,
			),
			tileImageTokens(width, height, 2_833, 5_667),
		)
	}
}

func isClaudeFamilyModelSlug(providerModelSlug string) bool {
	name := normalizedProviderModelName(providerModelSlug)
	if !strings.HasPrefix(name, "claude-") {
		return false
	}
	_, _, hasVersion := claudeModelVersion(name)
	hasFamily := false
	hasLatestAlias := false
	for _, segment := range strings.Split(name, "-")[1:] {
		switch segment {
		case "opus", "sonnet", "haiku", "fable", "mythos":
			hasFamily = true
		}
		if segment == "latest" {
			hasLatestAlias = true
		}
	}
	return hasFamily && (hasVersion || hasLatestAlias)
}

func boundedOpenAIPatchCount(width, height, patchBudget, maxDimension int) int {
	scale := min(
		1.0,
		min(float64(maxDimension)/float64(width), float64(maxDimension)/float64(height)),
	)
	scaledWidth := float64(width) * scale
	scaledHeight := float64(height) * scale
	patches := ceilDiv(max(int(math.Ceil(scaledWidth)), 1), 32) *
		ceilDiv(max(int(math.Ceil(scaledHeight)), 1), 32)
	if patches <= patchBudget {
		return patches
	}

	shrink := math.Sqrt(float64(32*32*patchBudget) / (scaledWidth * scaledHeight))
	patchesWide := max(math.Floor(scaledWidth*shrink/32), 1)
	patchesHigh := max(math.Floor(scaledHeight*shrink/32), 1)
	adjustment := min(
		patchesWide/(scaledWidth*shrink/32),
		patchesHigh/(scaledHeight*shrink/32),
	)
	resizedWidth := max(int(math.Round(scaledWidth*shrink*adjustment)), 1)
	resizedHeight := max(int(math.Round(scaledHeight*shrink*adjustment)), 1)
	return min(ceilDiv(resizedWidth, 32)*ceilDiv(resizedHeight, 32), patchBudget)
}

func isGPT5TileModel(name string) bool {
	return name == "gpt-5" || strings.HasPrefix(name, "gpt-5-20") ||
		strings.HasPrefix(name, "gpt-5-chat-latest")
}

func imageDimensions(data []byte) (int, int, bool) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return 0, 0, false
	}
	return config.Width, config.Height, true
}

func normalizedProviderModelName(slug string) string {
	name := strings.ToLower(strings.TrimSpace(slug))
	if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
		name = name[slash+1:]
	}
	if variant := strings.IndexByte(name, ':'); variant >= 0 {
		name = name[:variant]
	}
	return name
}

func multipliedPatchTokens(patches int, multiplier float64) int {
	return int(math.Ceil(float64(min(patches, 1_536)) * multiplier))
}

func tileImageTokens(width, height, base, perTile int) int {
	w, h := float64(width), float64(height)
	fit := min(1.0, min(2_048/w, 2_048/h))
	w *= fit
	h *= fit
	shortest := min(w, h)
	if shortest > 0 {
		scale := 768 / shortest
		w *= scale
		h *= scale
	}
	tiles := int(math.Ceil(w/512) * math.Ceil(h/512))
	return base + tiles*perTile
}

func ceilDiv(value, divisor int) int {
	return (value + divisor - 1) / divisor
}
