package anthropicmessages

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
)

func (c Client) endpoint() route.StaticEndpoint {
	return route.StaticEndpoint{
		BaseURL:        c.BaseURL,
		DefaultBaseURL: "https://api.anthropic.com/v1",
		Path:           c.EndpointPath,
	}
}

func (c Client) routeClient() route.Client {
	return route.Client{
		ProviderModelSlug: c.RequestedProviderModelSlug(),
		ModelCapabilities: c.ModelCapabilities,
		Endpoint:          c.endpoint(),
		Auth: route.Chain{
			c.Auth,
			route.Headers{"Anthropic-Version": APIVersion},
			c.betaHeader(),
		},
		Transport: route.HTTPTransport{Client: c.HTTPClient, Method: http.MethodPost, IdleTimeout: c.IdleTimeout},
		Protocol:  protocol{client: c},
	}
}

func (c Client) betaHeader() route.Auth {
	if !c.firstPartyAnthropic() {
		return nil
	}
	return anthropicBeta(thinkingBindingControlsBeta)
}

type anthropicBeta string

func (b anthropicBeta) Apply(req *http.Request) error {
	var betas []string
	for _, value := range req.Header.Values("Anthropic-Beta") {
		for _, beta := range strings.Split(value, ",") {
			if beta = strings.TrimSpace(beta); beta != "" {
				betas = append(betas, beta)
			}
		}
	}
	if !slices.Contains(betas, string(b)) {
		betas = append(betas, string(b))
	}
	req.Header.Set("Anthropic-Beta", strings.Join(betas, ","))
	return nil
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
