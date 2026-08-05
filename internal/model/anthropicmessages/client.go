package anthropicmessages

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

const APIVersion = "2023-06-01"

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
	return modelprotocol.APIFormatAnthropicMessages
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

type protocol struct {
	client Client
}

func (p protocol) APIFormat() modelprotocol.APIFormat {
	return modelprotocol.APIFormatAnthropicMessages
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
