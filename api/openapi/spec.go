package openapispec

import _ "embed"

// YAML is the checked-in public OpenAPI contract.
//
//go:embed openapi.yaml
var YAML []byte
