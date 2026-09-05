package openaichatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type Client struct {
	ModelProviderConfigID string
	Auth                  route.Auth
	BaseURL               string
	EndpointPath          string
	ProviderModelSlug     string
	HTTPClient            *http.Client
	ModelCapabilities     model.Capabilities
	APIVariant            modelprotocol.APIVariant
	APIVariantOptions     json.RawMessage
}

var _ modelcontext.MediaProjector = Client{}

func (c Client) RequestedProviderModelSlug() string {
	return c.ProviderModelSlug
}

func (c Client) APIFormat() modelprotocol.APIFormat {
	return modelprotocol.APIFormatOpenAIChatCompletions
}

func (c Client) ModelAPIVariant() modelprotocol.APIVariant {
	return normalizeAPIVariant(c.APIVariant)
}

func (c Client) Capabilities() model.Capabilities {
	return c.ModelCapabilities
}

func (c Client) ProjectRenderedMedia(bundle modelcontext.Bundle) []modelcontext.RenderedMedia {
	return (protocol{client: c}).ProjectRenderedMedia(bundle)
}

func (c Client) endpoint() route.StaticEndpoint {
	return route.StaticEndpoint{
		BaseURL:        c.BaseURL,
		DefaultBaseURL: "https://api.openai.com/v1",
		Path:           c.EndpointPath,
	}
}

func (c Client) routeClient() route.Client {
	return route.Client{
		ProviderModelSlug: c.RequestedProviderModelSlug(),
		ModelCapabilities: c.ModelCapabilities,
		Endpoint:          c.endpoint(),
		Auth:              c.Auth,
		Transport:         route.HTTPTransport{Client: c.HTTPClient, Method: http.MethodPost},
		Protocol:          protocol{client: c},
	}
}

func (c Client) Prepare(ctx context.Context, input model.PrepareInput) (model.PreparedRequest, error) {
	if strings.TrimSpace(c.EndpointPath) == "" {
		return model.PreparedRequest{}, route.SetupError{Err: errors.New("model endpoint path is required")}
	}
	return c.routeClient().Prepare(ctx, input)
}

func (c Client) Respond(ctx context.Context, input model.Request) (model.Response, error) {
	if strings.TrimSpace(c.EndpointPath) == "" {
		return model.Response{}, route.SetupError{Err: errors.New("model endpoint path is required")}
	}
	return c.routeClient().RespondStream(ctx, input)
}

type protocol struct {
	client Client
}

func (p protocol) APIFormat() modelprotocol.APIFormat {
	return modelprotocol.APIFormatOpenAIChatCompletions
}

func (p protocol) ModelAPIVariant() modelprotocol.APIVariant {
	return p.client.ModelAPIVariant()
}

func normalizeAPIVariant(value modelprotocol.APIVariant) modelprotocol.APIVariant {
	value = modelprotocol.APIVariant(strings.TrimSpace(string(value)))
	if value == "" {
		return modelprotocol.APIVariantDefault
	}
	return value
}
