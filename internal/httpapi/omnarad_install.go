package httpapi

import (
	"bytes"
	_ "embed"
	"fmt"
	"net/http"
	"strings"
	"text/template"
)

//go:embed omnarad.sh.tmpl
var omnaradInstallTemplateSource string

var omnaradInstallTemplate = template.Must(
	template.New("omnarad.sh").Funcs(template.FuncMap{"shellQuote": shellQuote}).Parse(omnaradInstallTemplateSource),
)

type omnaradInstallTemplateData struct {
	ReleaseURL string
	APIURL     string
}

func (s *Server) omnaradInstallRoute(w http.ResponseWriter, r *http.Request) {
	apiURL := s.publicURL
	if apiURL == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		apiURL = scheme + "://" + r.Host
	}
	script, err := renderOmnaradInstallScript(s.daemonReleaseURL, apiURL)
	if err != nil {
		s.log.Error("render omnarad installer", "error", err)
		http.Error(w, "installer unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(script)
}

func renderOmnaradInstallScript(releaseURL, apiURL string) ([]byte, error) {
	if strings.TrimSpace(releaseURL) == "" {
		return nil, fmt.Errorf("daemon release URL is required")
	}
	var rendered bytes.Buffer
	if err := omnaradInstallTemplate.Execute(&rendered, omnaradInstallTemplateData{
		ReleaseURL: releaseURL,
		APIURL:     apiURL,
	}); err != nil {
		return nil, err
	}
	return rendered.Bytes(), nil
}

func shellQuote(value string) string {
	var quoted strings.Builder
	quoted.Grow(len(value) + 2)
	quoted.WriteByte('\'')
	for _, r := range value {
		if r == '\'' {
			quoted.WriteString(`'"'"'`)
			continue
		}
		quoted.WriteRune(r)
	}
	quoted.WriteByte('\'')
	return quoted.String()
}
