package auth

import (
	"mime"
	"net/http"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/httpjson"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	httpjson.Write(w, status, body)
}

func decodeAllowedJSONBody(r *http.Request, dst any, allowedFields, pathFields map[string]bool) error {
	return httpjson.DecodeAllowed(r, dst, allowedFields, pathFields)
}

func requireJSONContentType(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		apierror.Write(w, openapi.ErrorCodeUnsupportedMediaType, "content type must be application/json")
		return false
	}
	return true
}
