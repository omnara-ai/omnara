package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

func (c *Client) graphQL(ctx context.Context, token, query string, variables, output any, mutation bool) error {
	input := struct {
		Query     string `json:"query"`
		Variables any    `json:"variables"`
	}{query, variables}
	var response struct {
		Errors []struct {
			Type string `json:"type"`
		} `json:"errors"`
		Data json.RawMessage `json:"data"`
	}
	header, err := c.doJSON(ctx, http.MethodPost, "/graphql", token, input, &response, mutation)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
		c.invalidateToken(ctx, token)
	}
	if err != nil {
		return err
	}
	if len(response.Errors) > 0 {
		limited := header.Get("X-Ratelimit-Remaining") == "0" || header.Get("Retry-After") != ""
		if limited && (!mutation || len(response.Data) == 0) {
			return responseError(http.StatusForbidden, header, nil, false, c.now())
		}
		// https://spec.graphql.org/October2021/#sec-Errors: absent data means
		// execution never started; partial data can accompany an applied mutation.
		if len(response.Data) == 0 {
			if len(response.Errors) == 1 && response.Errors[0].Type == "RATE_LIMITED" {
				return responseError(http.StatusTooManyRequests, header, nil, false, c.now())
			}
			return &APIError{Code: PermanentFailure, StatusCode: http.StatusOK,
				cause: errors.New("GitHub rejected the GraphQL request before execution")}
		}
		var fields map[string]json.RawMessage
		noResult := json.Unmarshal(response.Data, &fields) == nil
		for _, value := range fields {
			if string(value) != "null" {
				noResult = false
			}
		}
		if noResult && len(response.Errors) == 1 {
			switch response.Errors[0].Type {
			case "UNPROCESSABLE":
				return &APIError{Code: PermanentFailure, StatusCode: http.StatusUnprocessableEntity}
			case "FORBIDDEN", "NOT_FOUND":
				return &APIError{Code: PermanentFailure, StatusCode: http.StatusForbidden,
					cause: errors.New("GitHub denied access or could not find the requested resource; check that it " +
						"still exists and this integration can access it")}
			case "RATE_LIMITED":
				return responseError(http.StatusTooManyRequests, header, nil, false, c.now())
			}
		}
		if mutation {
			return &APIError{Code: DeliveryUnknown, StatusCode: http.StatusOK}
		}
		return &APIError{Code: InvalidResponse, StatusCode: http.StatusOK}
	}
	if len(response.Data) == 0 || string(response.Data) == "null" || json.Unmarshal(response.Data, output) != nil {
		code := InvalidResponse
		if mutation {
			code = DeliveryUnknown
		}
		return &APIError{Code: code}
	}
	return nil
}
