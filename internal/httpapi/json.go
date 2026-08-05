package httpapi

import (
	"net/http"

	"github.com/omnara-ai/omnara/internal/httpapi/httpjson"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	httpjson.Write(w, status, body)
}
