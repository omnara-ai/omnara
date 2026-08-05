package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
)

func (s *Server) redirectOAuthOutcome(w http.ResponseWriter, r *http.Request, returnTo string, params url.Values) {
	target, err := url.Parse(s.absolutePublicURL(httpauth.SafeReturnTo(returnTo)))
	if err != nil {
		target, err = url.Parse(s.absolutePublicURL("/"))
		if err != nil {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
	}
	query := target.Query()
	for key, values := range params {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	target.RawQuery = query.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (s *Server) absolutePublicURL(path string) string {
	return strings.TrimRight(s.publicURL, "/") + path
}
