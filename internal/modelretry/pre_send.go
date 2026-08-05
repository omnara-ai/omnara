package modelretry

import (
	"encoding/json"
	"errors"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type PreSendFailure struct {
	Code    string
	Message string
}

func NormalizePreSendFailure(err error, failure PreSendFailure) error {
	switch {
	case errors.Is(err, storeerr.ErrModelGrantUnavailable):
		return model.ProviderError{
			Kind:    model.ErrorKindAuth,
			Source:  "model_resolver",
			Code:    "model_grant_unavailable",
			Message: "This project does not currently have access to the configured model.",
			Cause:   err,
		}
	}
	if _, classified := model.ClassifyError(err); classified {
		return err
	}
	if failure.Code == "" {
		failure.Code = "pre_send_infrastructure_failure"
	}
	if failure.Message == "" {
		failure.Message = "Omnara could not prepare the model request."
	}
	metadata, marshalErr := json.Marshal(map[string]string{"cause": sanitize(errorText(err))})
	if marshalErr != nil {
		metadata = json.RawMessage(`{}`)
	}
	return model.ProviderError{
		Kind:     model.ErrorKindTransient,
		Source:   "omnara",
		Code:     failure.Code,
		Message:  failure.Message,
		Metadata: metadata,
		Cause:    err,
	}
}
