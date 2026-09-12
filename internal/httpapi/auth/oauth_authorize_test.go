package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testResource = "https://omnara.test/mcp"

func validAuthorizeValues() url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {"https://client.example/oauth/client.json"},
		"redirect_uri":          {"http://127.0.0.1:3000/callback"},
		"state":                 {"xyz"},
		"code_challenge":        {strings.Repeat("a", 43)},
		"code_challenge_method": {"S256"},
		"resource":              {testResource},
	}
}

func resolveAuthorizeValues(values url.Values) (oauthAuthorizeRequest, *oauthAuthorizeError) {
	request, authErr := parseOAuthAuthorizeRequest(values)
	if authErr != nil {
		return oauthAuthorizeRequest{}, authErr
	}
	if authErr := request.validateGrantParams(values, []string{testResource}); authErr != nil {
		return oauthAuthorizeRequest{}, authErr
	}
	return request, nil
}

func TestParseOAuthAuthorizeRequest(t *testing.T) {
	request, authErr := resolveAuthorizeValues(validAuthorizeValues())
	if authErr != nil {
		t.Fatalf("valid request rejected: %v", authErr)
	}
	if request.ClientID != "https://client.example/oauth/client.json" || request.State != "xyz" ||
		request.Resource != testResource {
		t.Fatalf("parsed request = %+v", request)
	}

	for name, tc := range map[string]struct {
		mutate       func(url.Values)
		wantCode     string
		wantRedirect bool
	}{
		"http client id": {
			mutate:   func(v url.Values) { v.Set("client_id", "http://client.example/client.json") },
			wantCode: "invalid_client",
		},
		"client id without path": {
			mutate:   func(v url.Values) { v.Set("client_id", "https://client.example") },
			wantCode: "invalid_client",
		},
		"client id with fragment": {
			mutate:   func(v url.Values) { v.Set("client_id", "https://client.example/c.json#x") },
			wantCode: "invalid_client",
		},
		"non loopback http redirect": {
			mutate:   func(v url.Values) { v.Set("redirect_uri", "http://client.example/callback") },
			wantCode: "invalid_request",
		},
		"redirect with fragment": {
			mutate:   func(v url.Values) { v.Set("redirect_uri", "https://client.example/cb#frag") },
			wantCode: "invalid_request",
		},
		"invalid utf8 client id": {
			mutate:   func(v url.Values) { v.Set("client_id", "https://client.example/c.json?x=\xff") },
			wantCode: "invalid_client",
		},
		"invalid utf8 redirect": {
			mutate:   func(v url.Values) { v.Set("redirect_uri", "https://client.example/cb?x=\xff") },
			wantCode: "invalid_request",
		},
		"custom scheme redirect": {
			mutate:   func(v url.Values) { v.Set("redirect_uri", "myapp://callback") },
			wantCode: "invalid_request",
		},
		"repeated parameter": {
			mutate:   func(v url.Values) { v["state"] = []string{"a", "b"} },
			wantCode: "invalid_request",
		},
		"wrong response type": {
			mutate:       func(v url.Values) { v.Set("response_type", "token") },
			wantCode:     "unsupported_response_type",
			wantRedirect: true,
		},
		"plain pkce": {
			mutate:       func(v url.Values) { v.Set("code_challenge_method", "plain") },
			wantCode:     "invalid_request",
			wantRedirect: true,
		},
		"missing pkce": {
			mutate:       func(v url.Values) { v.Del("code_challenge") },
			wantCode:     "invalid_request",
			wantRedirect: true,
		},
		"scope": {
			mutate:       func(v url.Values) { v.Set("scope", "files:read") },
			wantCode:     "invalid_scope",
			wantRedirect: true,
		},
		"missing resource": {
			mutate:       func(v url.Values) { v.Del("resource") },
			wantCode:     "invalid_target",
			wantRedirect: true,
		},
		"foreign resource": {
			mutate:       func(v url.Values) { v.Set("resource", "https://other.example/mcp") },
			wantCode:     "invalid_target",
			wantRedirect: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			values := validAuthorizeValues()
			tc.mutate(values)
			_, authErr := resolveAuthorizeValues(values)
			if authErr == nil {
				t.Fatal("invalid request accepted")
			}
			if authErr.code != tc.wantCode {
				t.Fatalf("error code = %q (%s), want %q", authErr.code, authErr.description, tc.wantCode)
			}
			if (authErr.redirectURI != "") != tc.wantRedirect {
				t.Fatalf("redirectURI = %q, want redirect %v", authErr.redirectURI, tc.wantRedirect)
			}
		})
	}

	trailing := validAuthorizeValues()
	trailing.Set("resource", testResource+"/")
	if _, authErr := resolveAuthorizeValues(trailing); authErr != nil {
		t.Fatalf("trailing slash resource rejected: %v", authErr)
	}
	upper := validAuthorizeValues()
	upper.Set("redirect_uri", "http://LOCALHOST:8080/cb")
	if _, authErr := resolveAuthorizeValues(upper); authErr != nil {
		t.Fatalf("localhost redirect rejected: %v", authErr)
	}
}

func TestParseOAuthAuthorizeRequestDefersGrantValidation(t *testing.T) {
	values := validAuthorizeValues()
	values.Set("response_type", "token")
	values.Set("resource", "https://other.example/mcp")
	if _, authErr := parseOAuthAuthorizeRequest(values); authErr != nil {
		t.Fatalf("grant parameters validated before redirect_uri registration: %v", authErr)
	}
}

func TestFetchClientMetadataValidatesDocument(t *testing.T) {
	var document map[string]any
	var status int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(document)
	}))
	t.Cleanup(server.Close)
	clientID := server.URL + "/client.json"
	handler := &Handler{clientMetadataHTTPClient: server.Client()}

	base := func() map[string]any {
		return map[string]any{
			"client_id":     clientID,
			"client_name":   "  Example Client ",
			"client_uri":    "https://client.example",
			"redirect_uris": []string{"http://127.0.0.1:3000/callback"},
		}
	}
	status = http.StatusOK
	document = base()
	metadata, err := handler.fetchClientMetadata(context.Background(), clientID)
	if err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
	if metadata.ClientName != "Example Client" || metadata.ClientURI != "https://client.example" ||
		len(metadata.RedirectURIs) != 1 {
		t.Fatalf("metadata = %+v", metadata)
	}

	for name, tc := range map[string]struct {
		status   int
		document map[string]any
	}{
		"not found": {status: http.StatusNotFound, document: base()},
		"client id mismatch": {status: http.StatusOK, document: func() map[string]any {
			d := base()
			d["client_id"] = "https://other.example/client.json"
			return d
		}()},
		"missing name": {status: http.StatusOK, document: func() map[string]any {
			d := base()
			delete(d, "client_name")
			return d
		}()},
		"missing redirects": {status: http.StatusOK, document: func() map[string]any {
			d := base()
			d["redirect_uris"] = []string{}
			return d
		}()},
		"confidential client": {status: http.StatusOK, document: func() map[string]any {
			d := base()
			d["token_endpoint_auth_method"] = "client_secret_basic"
			return d
		}()},
		"implicit only": {status: http.StatusOK, document: func() map[string]any {
			d := base()
			d["grant_types"] = []string{"implicit"}
			return d
		}()},
	} {
		t.Run(name, func(t *testing.T) {
			status = tc.status
			document = tc.document
			if _, err := handler.fetchClientMetadata(context.Background(), clientID); err == nil {
				t.Fatal("invalid document accepted")
			}
		})
	}
}

func TestRedirectURIRegisteredIgnoresLoopbackPort(t *testing.T) {
	registered := []string{"http://localhost/callback", "http://127.0.0.1/callback", "https://app.example/cb"}
	for uri, want := range map[string]bool{
		"http://localhost:60351/callback":  true,
		"http://127.0.0.1:8080/callback":   true,
		"http://localhost/callback":        true,
		"https://app.example/cb":           true,
		"https://app.example:8443/cb":      false,
		"http://localhost:8080/other":      false,
		"http://[::1]:8080/callback":       false,
		"http://localhost:8080/callback?x": false,
	} {
		if got := redirectURIRegistered(registered, uri); got != want {
			t.Errorf("redirectURIRegistered(%q) = %v, want %v", uri, got, want)
		}
	}
}

func TestOAuthRedirectURLPreservesQueryAndAddsIssuer(t *testing.T) {
	handler := &Handler{publicURL: "https://omnara.test/"}
	request := httptest.NewRequest(http.MethodGet, "https://omnara.test/x", nil)
	got := handler.oauthRedirectURL(request, "http://127.0.0.1:3000/cb?keep=1", url.Values{"code": {"abc"}}, "st")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	query := parsed.Query()
	if query.Get("keep") != "1" || query.Get("code") != "abc" || query.Get("state") != "st" ||
		query.Get("iss") != "https://omnara.test" {
		t.Fatalf("redirect query = %v", query)
	}
}
