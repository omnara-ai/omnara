package tools

import "errors"

const managedWorkAdmissionDeniedMessage = "new work using this deployment-managed resource is temporarily unavailable"

var ErrActionRequired = errors.New("tool call requires permission")
