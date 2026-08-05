package anthropicmessages

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
)

func (c Client) routeClient() route.Client {
	return route.Client{
		ProviderModelSlug: c.RequestedProviderModelSlug(),
		ModelCapabilities: c.ModelCapabilities,
		Endpoint: route.StaticEndpoint{
			BaseURL:        c.BaseURL,
			DefaultBaseURL: "https://api.anthropic.com/v1",
			Path:           c.EndpointPath,
		},
		Auth: route.Chain{
			c.Auth,
			route.Headers{"Anthropic-Version": APIVersion},
		},
		Transport: route.HTTPTransport{Client: c.HTTPClient, Method: http.MethodPost},
		Protocol:  protocol{client: c},
	}
}

func (c Client) Prepare(ctx context.Context, input model.PrepareInput) (model.PreparedRequest, error) {
	if strings.TrimSpace(c.EndpointPath) == "" {
		return model.PreparedRequest{}, route.SetupError{Err: errors.New("model endpoint path is required")}
	}
	return c.prepareWithinRequestBodyLimit(ctx, input, anthropicMessagesRequestBodyLimit)
}

func (c Client) Respond(ctx context.Context, input model.Request) (model.Response, error) {
	if strings.TrimSpace(c.EndpointPath) == "" {
		return model.Response{}, route.SetupError{Err: errors.New("model endpoint path is required")}
	}
	return c.routeClient().RespondStream(ctx, input)
}
