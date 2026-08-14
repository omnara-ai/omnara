package modelprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
)

type DiscoveredModel struct {
	Slug                string
	DisplayName         string
	ContextWindowTokens *int
	MaxOutputTokens     *int
}

type DiscoverFunc func(
	ctx context.Context,
	providerConfig modelstore.ModelProviderConfigRecord,
	apiKey string,
	allowLoopback bool,
) ([]DiscoveredModel, error)

const (
	discoveryRequestTimeout  = 10 * time.Second
	discoveryMaxResponseSize = 1024 * 1024
	discoveryPageLimit       = "1000"
)

func DiscoverModels(
	ctx context.Context,
	providerConfig modelstore.ModelProviderConfigRecord,
	apiKey string,
	allowLoopback bool,
) ([]DiscoveredModel, error) {
	auth, err := routeAuthForProviderConfig(providerConfig, apiKey)
	if err != nil {
		return nil, err
	}
	if providerConfig.APIVariant == modelprotocol.APIVariantBedrock {
		auth = route.BearerToken{Token: apiKey}
	} else if providerConfig.APIFormat == modelprotocol.APIFormatAnthropicMessages {
		auth = route.Chain{auth, route.Headers{"Anthropic-Version": anthropicmessages.APIVersion}}
	}
	ctx, cancel := context.WithTimeout(ctx, discoveryRequestTimeout)
	defer cancel()
	client := newSSRFHTTPClient(allowLoopback)
	client.Timeout = discoveryRequestTimeout
	client = outboundhttp.CloneWithoutRedirects(client)
	if providerConfig.APIVariant == modelprotocol.APIVariantOpenRouter {
		body, err := getDiscoveryEndpoint(
			ctx, client, providerConfig.BaseURL, "/key", "OpenRouter key endpoint",
			auth, discoveryMaxResponseSize,
		)
		if err != nil {
			return nil, err
		}
		var decoded struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil || !jsonObject(decoded.Data) {
			return nil, errors.New("OpenRouter key endpoint returned an unrecognized response")
		}
	}
	modelsBaseURL := providerConfig.BaseURL
	modelsPath := "/models"
	if providerConfig.APIVariant == modelprotocol.APIVariantBedrock {
		parsed, err := url.Parse(modelsBaseURL)
		if err != nil {
			return nil, err
		}
		parsed.Path = "/v1"
		modelsBaseURL = parsed.String()
	} else if providerConfig.APIFormat == modelprotocol.APIFormatAnthropicMessages {
		modelsPath += "?limit=" + discoveryPageLimit
	}
	entries, err := fetchModelEntries(
		ctx, client, modelsBaseURL, modelsPath, "models endpoint",
		auth, discoveryMaxResponseSize,
	)
	if err != nil {
		return nil, err
	}
	if providerConfig.APIVariant == modelprotocol.APIVariantBedrock {
		return nil, nil
	}
	type rankedModel struct {
		model     DiscoveredModel
		createdAt int64
	}
	ranked := make([]rankedModel, 0, len(entries))
	for _, entry := range entries {
		entry.ID = strings.TrimSpace(entry.ID)
		if entry.ID == "" || !entry.supportsTextAndTools() {
			continue
		}
		contextWindowTokens := entry.contextWindowTokens()
		maxOutputTokens := clampedMaxOutput(contextWindowTokens, entry.maxOutputTokens())
		displayName := entry.DisplayName
		if displayName == "" {
			displayName = entry.Name
		}
		ranked = append(ranked, rankedModel{
			model: DiscoveredModel{
				Slug:                entry.ID,
				DisplayName:         displayName,
				ContextWindowTokens: contextWindowTokens,
				MaxOutputTokens:     maxOutputTokens,
			},
			createdAt: entry.createdAtUnix(),
		})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].createdAt > ranked[j].createdAt
	})
	models := make([]DiscoveredModel, 0, len(ranked))
	seenSlugs := make(map[string]struct{}, len(ranked))
	for _, entry := range ranked {
		if _, seen := seenSlugs[entry.model.Slug]; seen {
			continue
		}
		seenSlugs[entry.model.Slug] = struct{}{}
		models = append(models, entry.model)
	}
	return models, nil
}

func fetchModelEntries(
	ctx context.Context,
	client *http.Client,
	baseURL, path, name string,
	auth route.Auth,
	maxResponseSize int,
) ([]discoveredModelEntry, error) {
	body, err := getDiscoveryEndpoint(ctx, client, baseURL, path, name, auth, maxResponseSize)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Data []discoveredModelEntry `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Data == nil {
		return nil, fmt.Errorf("%s returned an unrecognized response", name)
	}
	return decoded.Data, nil
}

func getDiscoveryEndpoint(
	ctx context.Context,
	client *http.Client,
	baseURL, path, name string,
	auth route.Auth,
	maxResponseSize int,
) ([]byte, error) {
	endpoint, err := route.StaticEndpoint{BaseURL: baseURL, Path: path}.URL()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if auth != nil {
		if err := auth.Apply(req); err != nil {
			return nil, err
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", name, err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxResponseSize)+1))
	closeErr := resp.Body.Close()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("read %s response: %w", name, err), closeErr)
	}
	if len(body) > maxResponseSize {
		return nil, errors.Join(fmt.Errorf("%s response exceeds the byte limit", name), closeErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, discoveryStatusError(name, resp.StatusCode, body)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close %s response: %w", name, closeErr)
	}
	return body, nil
}

func jsonObject(value json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

type discoveredModelEntry struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"` // Anthropic
	Name        string `json:"name"`         // OpenRouter
	Created     int64  `json:"created"`      // OpenAI, OpenRouter (unix seconds)
	CreatedAt   string `json:"created_at"`   // Anthropic (RFC 3339)

	// Context window, one concept: OpenRouter reports context_length,
	// Anthropic reports max_input_tokens.
	ContextLength  json.RawMessage `json:"context_length"`
	MaxInputTokens json.RawMessage `json:"max_input_tokens"`

	// Max output tokens, one concept: OpenAI-compatible endpoints report
	// max_output_tokens or max_completion_tokens, Anthropic reports max_tokens.
	MaxOutputTokens     json.RawMessage `json:"max_output_tokens"`
	MaxCompletionTokens json.RawMessage `json:"max_completion_tokens"`
	MaxTokens           json.RawMessage `json:"max_tokens"`

	Architecture *struct {
		OutputModalities []string `json:"output_modalities"`
	} `json:"architecture"`
	SupportedParameters []string `json:"supported_parameters"`
	TopProvider         *struct {
		ContextLength       json.RawMessage `json:"context_length"`
		MaxCompletionTokens json.RawMessage `json:"max_completion_tokens"`
	} `json:"top_provider"`
}

func (e discoveredModelEntry) contextWindowTokens() *int {
	for _, raw := range []json.RawMessage{e.ContextLength, e.MaxInputTokens} {
		if value := modelTokenCount(raw); value != nil && *value >= 2 {
			return value
		}
	}
	if e.TopProvider != nil {
		if value := modelTokenCount(e.TopProvider.ContextLength); value != nil && *value >= 2 {
			return value
		}
	}
	return nil
}

func (e discoveredModelEntry) maxOutputTokens() *int {
	for _, raw := range []json.RawMessage{e.MaxOutputTokens, e.MaxCompletionTokens, e.MaxTokens} {
		if value := modelTokenCount(raw); value != nil {
			return value
		}
	}
	if e.TopProvider != nil {
		return modelTokenCount(e.TopProvider.MaxCompletionTokens)
	}
	return nil
}

func clampedMaxOutput(contextWindow, maxOutput *int) *int {
	if contextWindow != nil && maxOutput != nil && *maxOutput >= *contextWindow {
		return nil
	}
	return maxOutput
}

func modelTokenCount(raw json.RawMessage) *int {
	var value int
	if json.Unmarshal(raw, &value) != nil || value <= 0 || value > math.MaxInt32 {
		return nil
	}
	return &value
}

func (e discoveredModelEntry) createdAtUnix() int64 {
	if e.Created > 0 {
		return e.Created
	}
	if e.CreatedAt != "" {
		if createdAt, err := time.Parse(time.RFC3339, e.CreatedAt); err == nil {
			return createdAt.Unix()
		}
	}
	return 0
}

func (e discoveredModelEntry) supportsTextAndTools() bool {
	if e.Architecture != nil {
		if !slices.Contains(e.Architecture.OutputModalities, "text") {
			return false
		}
		return slices.Contains(e.SupportedParameters, "tools")
	}
	slug := strings.ToLower(e.ID)
	for _, marker := range nonChatModelMarkers {
		if strings.Contains(slug, marker) {
			return false
		}
	}
	return true
}

var nonChatModelMarkers = []string{
	"whisper",
	"tts",
	"transcribe",
	"audio",
	"realtime",
	"dall-e",
	"image",
	"sora",
	"embedding",
	"moderation",
	"davinci",
	"babbage",
	"-instruct",
}

func discoveryStatusError(endpoint string, statusCode int, body []byte) error {
	message := providerErrorMessage(body)
	if message == "" {
		return fmt.Errorf("%s returned status %d", endpoint, statusCode)
	}
	return fmt.Errorf("%s returned status %d: %s", endpoint, statusCode, message)
}

func providerErrorMessage(body []byte) string {
	var decoded struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return ""
	}
	message := strings.TrimSpace(decoded.Error.Message)
	const maxMessageLength = 200
	if len(message) > maxMessageLength {
		message = message[:maxMessageLength]
	}
	return message
}
