package httpapi

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
)

const spaIndexFile = "index.html"

var spaContentSecurityPolicy = strings.Join([]string{
	"default-src 'self'",
	"script-src 'self' 'sha256-4Z+0IbR8cDetVQawCZYyJN7DAZJUmjFGeS+nwKwqD8c='",
	"style-src 'self' 'unsafe-inline'",
	"img-src 'self' data:",
	"font-src 'self' data:",
	"connect-src 'self'",
	"object-src 'none'",
	"base-uri 'none'",
	"frame-ancestors 'none'",
}, "; ")

var spaReservedPrefixes = []string{
	"api/",
	"install/",
	".well-known/",
	"healthz/",
}

var spaReservedExact = []string{
	"api",
	"install",
	".well-known",
	"healthz",
}

func (s *Server) registerRootRoutes(mux *http.ServeMux) {
	if webIndexAvailable(s.webAssets) {
		mux.Handle("/", s.serveSPA(s.webAssets))
		return
	}
	mux.HandleFunc("GET /{$}", s.rootRoute)
	mux.HandleFunc("/", s.notFound)
}

// rootRoute serves an empty 404 with a CSP that still permits same-origin
// fetch, so the root page can host browser-console calls against the API.
func (s *Server) rootRoute(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; connect-src 'self'")
	w.WriteHeader(http.StatusNotFound)
}

func webIndexAvailable(assets fs.FS) bool {
	if assets == nil {
		return false
	}
	f, err := assets.Open(spaIndexFile)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func (s *Server) serveSPA(assets fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(assets))
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			s.notFound(w, r)
			return
		}
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean != "" && clean != spaIndexFile {
			if spaPathHasHiddenSegment(clean) {
				s.notFound(w, r)
				return
			}
			if file, err := assets.Open(clean); err == nil {
				info, statErr := file.Stat()
				_ = file.Close()
				if statErr == nil && !info.IsDir() {
					if strings.HasPrefix(clean, "assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					}
					w.Header().Set("X-Content-Type-Options", "nosniff")
					fileServer.ServeHTTP(w, r)
					return
				}
			}
			if spaPathIsStaticAsset(clean) {
				s.notFound(w, r)
				return
			}
		}
		if spaPathIsReserved(clean) {
			s.notFound(w, r)
			return
		}
		s.serveSPAIndex(w, assets)
	}
}

func spaPathIsReserved(clean string) bool {
	for _, exact := range spaReservedExact {
		if clean == exact {
			return true
		}
	}
	for _, prefix := range spaReservedPrefixes {
		if strings.HasPrefix(clean, prefix) {
			return true
		}
	}
	return false
}

func spaPathHasHiddenSegment(clean string) bool {
	for _, segment := range strings.Split(clean, "/") {
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

func spaPathIsStaticAsset(clean string) bool {
	return clean == "assets" || strings.HasPrefix(clean, "assets/") || path.Ext(clean) != ""
}

func (s *Server) serveSPAIndex(w http.ResponseWriter, assets fs.FS) {
	data, err := fs.ReadFile(assets, spaIndexFile)
	if err != nil {
		s.log.Error("read spa index", "error", err)
		apierror.Write(w, openapi.ErrorCodeInternalError, "web assets unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", spaContentSecurityPolicy)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
