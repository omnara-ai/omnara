package unikraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

// APIHTTPErrorNotFound is defined by Unikraft's SDK as error code 8.
// https://github.com/unikraft-cloud/go-sdk/blob/prod-stable/platform/errors.go
const instanceNotFoundErrorCode = 8

type responseStatus string

// Unikraft response statuses follow the ResponseStatus OpenAPI schema.
// https://github.com/unikraft-cloud/openapi/blob/prod-stable/platform.yaml
const (
	responseStatusSuccess        responseStatus = "success"
	responseStatusError          responseStatus = "error"
	responseStatusPartialSuccess responseStatus = "partial_success"
)

type instanceState string

// Unikraft instance states follow the InstanceState OpenAPI schema.
// https://github.com/unikraft-cloud/openapi/blob/prod-stable/platform.yaml
const (
	instanceStateStopped    instanceState = "stopped"
	instanceStateStarting   instanceState = "starting"
	instanceStateRunning    instanceState = "running"
	instanceStateDraining   instanceState = "draining"
	instanceStateStopping   instanceState = "stopping"
	instanceStateTemplate   instanceState = "template"
	instanceStateStandby    instanceState = "standby"
	instanceStateDeleted    instanceState = "deleted"
	instanceStateCheckpoint instanceState = "checkpoint"
)

type apiClient interface {
	CreateInstance(ctx context.Context, req createInstanceRequest) (instance, error)
	GetInstancesByUUIDs(ctx context.Context, uuids []string) (instanceBatch, error)
	GetInstanceByUUID(ctx context.Context, uuid string) (instance, bool, error)
	GetInstanceByName(ctx context.Context, name string) (instance, bool, error)
	DeleteInstanceByUUID(ctx context.Context, uuid string) error
}

type restClient struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
}

func (c *restClient) CreateInstance(
	ctx context.Context,
	req createInstanceRequest,
) (instance, error) {
	var response instancesResponse
	if _, err := c.doRequest(
		ctx,
		http.MethodPost,
		"/v1/instances",
		req,
		&response,
		false,
	); err != nil {
		return instance{}, err
	}
	if len(response.Instances) == 0 {
		return instance{}, errors.New("unikraft create response contained no instances")
	}
	return response.Instances[0], nil
}

func (c *restClient) GetInstancesByUUIDs(
	ctx context.Context,
	uuids []string,
) (instanceBatch, error) {
	if len(uuids) == 0 {
		return instanceBatch{EnvelopeStatus: responseStatusSuccess}, nil
	}
	lookups := make([]instanceUUIDLookup, len(uuids))
	for index, uuid := range uuids {
		lookups[index] = instanceUUIDLookup{UUID: uuid}
	}
	var response instancesResponse
	envelope, err := c.doRequest(
		ctx,
		http.MethodGet,
		"/v1/instances?details=true",
		lookups,
		&response,
		true,
	)
	if err != nil {
		return instanceBatch{}, err
	}
	return instanceBatchFromEnvelope(envelope, response.Instances), nil
}

func (c *restClient) GetInstanceByUUID(ctx context.Context, uuid string) (instance, bool, error) {
	var response instancesResponse
	envelope, err := c.doRequest(
		ctx,
		http.MethodGet,
		"/v1/instances/"+url.PathEscape(uuid)+"?details=true",
		nil,
		&response,
		true,
	)
	if err != nil {
		if isNotFound(err) {
			return instance{}, false, nil
		}
		return instance{}, false, err
	}
	if len(response.Instances) != 1 {
		return instance{}, false, fmt.Errorf(
			"unikraft uuid lookup returned %d instances, want exactly one",
			len(response.Instances),
		)
	}
	result := response.Instances[0]
	if result.UUID != uuid {
		return instance{}, false, fmt.Errorf(
			"unikraft uuid lookup returned instance %q, want %q",
			result.UUID,
			uuid,
		)
	}
	if missing, err := validateInstanceLookupResult(envelope, result); err != nil {
		return instance{}, false, err
	} else if missing {
		return instance{}, false, nil
	}
	return result, true, nil
}

func (c *restClient) GetInstanceByName(ctx context.Context, name string) (instance, bool, error) {
	var response instancesResponse
	envelope, err := c.doRequest(
		ctx,
		http.MethodGet,
		"/v1/instances?details=false",
		[]struct {
			Name string `json:"name,omitempty"`
		}{{Name: name}},
		&response,
		true,
	)
	if err != nil {
		if isNotFound(err) {
			return instance{}, false, nil
		}
		return instance{}, false, err
	}
	if len(response.Instances) != 1 {
		return instance{}, false, fmt.Errorf(
			"unikraft exact name lookup returned %d instances, want exactly one",
			len(response.Instances),
		)
	}
	result := response.Instances[0]
	if result.Name != name {
		return instance{}, false, fmt.Errorf(
			"unikraft name lookup returned instance %q, want %q",
			result.Name,
			name,
		)
	}
	if missing, err := validateInstanceLookupResult(envelope, result); err != nil {
		return instance{}, false, err
	} else if missing {
		return instance{}, false, nil
	}
	return result, true, nil
}

func validateInstanceLookupResult(
	envelope responseEnvelope,
	result instance,
) (bool, error) {
	batch := instanceBatchFromEnvelope(envelope, nil)
	if result.isNotFound() {
		if batch.notFoundItemsAuthoritative() {
			return true, nil
		}
		return false, errors.New(
			"unikraft instance lookup returned a non-authoritative not-found response",
		)
	}
	if !batch.cleanEnvelope() {
		return false, errors.New(
			"unikraft instance lookup returned a non-authoritative response",
		)
	}
	if result.Error != 0 {
		return false, apiError{
			StatusCode: http.StatusOK,
			Message:    fmt.Sprintf("item error %d", result.Error),
		}
	}
	if result.Status != "" && result.Status != responseStatusSuccess {
		return false, apiError{
			StatusCode: http.StatusOK,
			Message:    "status " + string(result.Status),
		}
	}
	return false, nil
}

func (c *restClient) DeleteInstanceByUUID(ctx context.Context, uuid string) error {
	_, err := c.doRequest(
		ctx,
		http.MethodDelete,
		"/v1/instances/"+url.PathEscape(uuid),
		nil,
		nil,
		false,
	)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *restClient) doRequest(
	ctx context.Context,
	method, path string,
	body any,
	out any,
	allowLogicalErrorData bool,
) (responseEnvelope, error) {
	response, err := providers.DoHTTPResponse(
		ctx,
		c.httpClient,
		providers.Unikraft,
		method,
		c.baseURL+path,
		map[string]string{"Authorization": "Bearer " + c.apiToken},
		body,
	)
	if err != nil {
		return responseEnvelope{}, err
	}
	statusCode, raw := response.StatusCode, response.Body
	if statusCode < 200 || statusCode >= 300 {
		message := ""
		var errorResponse responseEnvelope
		if err := json.Unmarshal(raw, &errorResponse); err == nil {
			message = errorResponse.errorMessage()
		}
		return responseEnvelope{}, providers.WithRetryAfter(
			apiError{StatusCode: statusCode, Message: message},
			response.Header,
		)
	}
	if len(raw) == 0 {
		return responseEnvelope{}, nil
	}
	var envelope responseEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return responseEnvelope{}, fmt.Errorf("decode unikraft response: %w", err)
	}
	if err := envelope.validate(statusCode, allowLogicalErrorData); err != nil {
		return responseEnvelope{}, err
	}
	if out == nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return envelope, nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return responseEnvelope{}, fmt.Errorf("decode unikraft response data: %w", err)
	}
	return envelope, nil
}

type apiError struct {
	StatusCode int
	Message    string
}

func (e apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("unikraft API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("unikraft API returned HTTP %d: %s", e.StatusCode, e.Message)
}

func isNotFound(err error) bool {
	var httpErr apiError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound
}

type responseEnvelope struct {
	Status responseStatus  `json:"status"`
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Status int `json:"status"`
	} `json:"errors"`
}

func (r responseEnvelope) validate(httpStatus int, allowLogicalErrorData bool) error {
	hasData := len(r.Data) > 0 && string(r.Data) != "null"
	allowDataInspection := allowLogicalErrorData && hasData
	switch r.Status {
	case responseStatusSuccess:
	case responseStatusError, responseStatusPartialSuccess:
		if !allowDataInspection {
			return apiError{StatusCode: httpStatus, Message: r.errorMessage()}
		}
	default:
		return apiError{StatusCode: httpStatus, Message: r.errorMessage()}
	}
	if len(r.Errors) > 0 && !allowDataInspection {
		return apiError{StatusCode: httpStatus, Message: r.errorMessage()}
	}
	return nil
}

func (r responseEnvelope) errorMessage() string {
	for _, item := range r.Errors {
		if item.Status != 0 {
			return fmt.Sprintf("status %d", item.Status)
		}
	}
	if r.Status != "" {
		return "status " + string(r.Status)
	}
	return ""
}

type createInstanceRequest struct {
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	Args          []string          `json:"args"`
	Env           map[string]string `json:"env"`
	MemoryMB      int               `json:"memory_mb"`
	VCPUs         int               `json:"vcpus"`
	Autostart     bool              `json:"autostart"`
	RestartPolicy string            `json:"restart_policy"`
	ScaleToZero   *scaleToZero      `json:"scale_to_zero,omitempty"`
	ServiceGroup  *serviceGroup     `json:"service_group,omitempty"`
}

type scaleToZero struct {
	Policy         string `json:"policy"`
	Stateful       bool   `json:"stateful"`
	CooldownTimeMS int    `json:"cooldown_time_ms"`
}

type serviceGroup struct {
	Services []service `json:"services,omitempty"`
	Domains  []domain  `json:"domains,omitempty"`
}

type service struct {
	Port            int      `json:"port"`
	DestinationPort int      `json:"destination_port"`
	Handlers        []string `json:"handlers"`
}

type domain struct {
	FQDN string `json:"fqdn"`
}

type instance struct {
	Status       responseStatus `json:"status"`
	Error        int            `json:"error"`
	UUID         string         `json:"uuid"`
	Name         string         `json:"name"`
	State        instanceState  `json:"state"`
	ServiceGroup *serviceGroup  `json:"service_group"`
}

func (i instance) isNotFound() bool {
	return i.Status == responseStatusError && i.Error == instanceNotFoundErrorCode
}

func (i instance) wakeFQDN() string {
	if i.ServiceGroup == nil {
		return ""
	}
	for _, domain := range i.ServiceGroup.Domains {
		if fqdn := strings.TrimSuffix(strings.TrimSpace(domain.FQDN), "."); fqdn != "" {
			return fqdn
		}
	}
	return ""
}

type instancesResponse struct {
	Instances []instance `json:"instances"`
}

type instanceBatch struct {
	Instances         []instance
	EnvelopeStatus    responseStatus
	HasEnvelopeErrors bool
}

func instanceBatchFromEnvelope(
	envelope responseEnvelope,
	instances []instance,
) instanceBatch {
	return instanceBatch{
		Instances:         instances,
		EnvelopeStatus:    envelope.Status,
		HasEnvelopeErrors: len(envelope.Errors) > 0,
	}
}

func (b instanceBatch) cleanEnvelope() bool {
	return b.EnvelopeStatus == responseStatusSuccess && !b.HasEnvelopeErrors
}

func (b instanceBatch) successfulItemsAuthoritative() bool {
	return b.cleanEnvelope() ||
		(b.EnvelopeStatus == responseStatusPartialSuccess && !b.HasEnvelopeErrors)
}

func (b instanceBatch) notFoundItemsAuthoritative() bool {
	return !b.HasEnvelopeErrors &&
		(b.EnvelopeStatus == responseStatusError ||
			b.EnvelopeStatus == responseStatusPartialSuccess)
}

type instanceUUIDLookup struct {
	UUID string `json:"uuid"`
}
