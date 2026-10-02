package webaccess

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/webaccess/telemlineage"
)

func telemProviderForServer(t *testing.T, server *httptest.Server) TelemProvider {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse Telem test server URL: %v", err)
	}
	return TelemProvider{
		APIKey: "tlm_test",
		HTTPClient: &http.Client{Transport: rewriteExaTransport{
			target: target,
			base:   server.Client().Transport,
		}},
	}
}

func telemRunJSON(provider, status string, urls ...string) map[string]any {
	results := make([]any, 0, len(urls))
	for _, u := range urls {
		results = append(results, map[string]any{
			"url":     u,
			"title":   provider + " " + u,
			"summary": "summary of " + u,
		})
	}
	return map[string]any{
		"preprocessor_name": provider,
		"status":            status,
		"output_payload":    map[string]any{"schema_version": 2, "results": results},
	}
}

func writeTelemRuns(t *testing.T, w http.ResponseWriter, runs ...map[string]any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(map[string]any{"preprocessor_runs": runs}); err != nil {
		t.Errorf("encode telem response: %v", err)
	}
}

func TestTelemProviderMapsRequestAndResponse(t *testing.T) {
	var captured map[string]any
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/search" {
			t.Errorf("request = %s %s, want POST /v1/search", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tlm_test" {
			t.Errorf("authorization = %q", got)
		}
		capturedBody, _ = io.ReadAll(r.Body)
		if err := json.Unmarshal(capturedBody, &captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writeTelemRuns(t, w,
			telemRunJSON("exa", "succeeded", "https://a.example/1", "https://a.example/2", "https://shared.example/"),
			telemRunJSON("brave", "failed", "https://failed.example/"),
			telemRunJSON("tavily", "succeeded", "https://shared.example/", "https://b.example/1"),
		)
	}))
	defer server.Close()

	provider := telemProviderForServer(t, server)
	provider.ProvidersInclude = []string{"exa", "tavily"}
	provider.ProvidersExclude = []string{"you"}
	provider.AutoRouting = "accuracy"
	resp, err := provider.Search(context.Background(), SearchRequest{Query: "go releases", NumResults: 4})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	var sent telemSearchRequest
	if err := json.Unmarshal(capturedBody, &sent); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	// The default tier, not a field list, keeps provider cost at the standard
	// rate.
	if sent.UserInput.Query != "go releases" || sent.Search.NumResults != 4 || sent.Search.Tier != "default" {
		t.Fatalf("request = %+v", sent)
	}
	if search, _ := captured["search"].(map[string]any); search["fields"] != nil {
		t.Fatalf("search block sent a field list: %v", captured["search"])
	}
	// Auto-routing picks from the include list minus the exclude list, so
	// both go with it.
	if sent.Search.AutoRouting != "accuracy" {
		t.Fatalf("auto_routing = %q, want accuracy", sent.Search.AutoRouting)
	}
	if p := sent.Search.Providers; p == nil || !slices.Equal(p.Include, []string{"exa", "tavily"}) ||
		!slices.Equal(p.Exclude, []string{"you"}) {
		t.Fatalf("providers = %+v", sent.Search.Providers)
	}
	if _, ok := captured["metadata"]; ok {
		t.Fatalf("metadata sent without lineage: %v", captured["metadata"])
	}

	// Providers are interleaved by rank, failed runs are skipped, duplicate
	// URLs are dropped and the list is cut to NumResults.
	var got []string
	for _, result := range resp.Results {
		got = append(got, result.URL)
	}
	want := []string{"https://a.example/1", "https://shared.example/", "https://a.example/2", "https://b.example/1"}
	if resp.Provider != "telem" || !slices.Equal(got, want) {
		t.Fatalf("results = %s %v, want telem %v", resp.Provider, got, want)
	}
	first := resp.Results[0]
	if first.Title != "exa https://a.example/1" || first.Snippet != "summary of https://a.example/1" {
		t.Fatalf("first result = %+v", first)
	}
}

func TestTelemProviderSendsLineageMetadata(t *testing.T) {
	var captured struct {
		Metadata map[string]any `json:"metadata"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		writeTelemRuns(t, w)
	}))
	defer server.Close()

	lineage := telemlineage.Lineage{Call: telemlineage.Call{
		AgentID:            uuid.New(),
		ModelCallContextID: uuid.New(),
		ToolCallID:         uuid.New(),
	}}
	if _, err := telemProviderForServer(t, server).Search(
		context.Background(),
		SearchRequest{Query: "q", NumResults: 1, Lineage: lineage},
	); err != nil {
		t.Fatalf("search: %v", err)
	}
	want := telemlineage.Metadata(lineage, "search")
	if captured.Metadata["session_key"] != want["session_key"] || captured.Metadata["node_key"] != want["node_key"] ||
		captured.Metadata["kind"] != "search" {
		t.Fatalf("metadata = %v, want keys from %v", captured.Metadata, want)
	}
	for _, content := range []string{"message_history", "goal"} {
		if _, ok := captured.Metadata[content]; ok {
			t.Fatalf("metadata carries %q: %v", content, captured.Metadata)
		}
	}
}

func TestTelemProviderOmitsProvidersWhenUnset(t *testing.T) {
	var captured struct {
		Search map[string]json.RawMessage `json:"search"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		writeTelemRuns(t, w)
	}))
	defer server.Close()

	if _, err := telemProviderForServer(t, server).Search(
		context.Background(),
		SearchRequest{Query: "q", NumResults: 1},
	); err != nil {
		t.Fatalf("search: %v", err)
	}
	if _, ok := captured.Search["providers"]; ok {
		t.Fatalf("providers sent although unset: %s", captured.Search["providers"])
	}
	if _, ok := captured.Search["auto_routing"]; ok {
		t.Fatalf("auto_routing sent although off: %s", captured.Search["auto_routing"])
	}
}

func TestTelemProviderFiltersDomainsOnResultHosts(t *testing.T) {
	var sent telemSearchRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sent)
		writeTelemRuns(t, w, telemRunJSON("exa", "succeeded",
			"https://go.dev/doc",
			"https://blog.go.dev/post",
			"https://www.go.dev/dl",
			"https://notgo.dev/x",
			"https://example.com/",
		))
	}))
	defer server.Close()

	resp, err := telemProviderForServer(t, server).Search(context.Background(), SearchRequest{
		Query:      "q",
		NumResults: 2,
		Domains:    []string{"GO.dev", "-blog.go.dev"},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if sent.Search.NumResults != 10 {
		t.Fatalf("num_results with a domain filter = %d, want 10 per provider", sent.Search.NumResults)
	}
	var got []string
	for _, result := range resp.Results {
		got = append(got, result.URL)
	}
	if want := []string{"https://go.dev/doc", "https://www.go.dev/dl"}; !slices.Equal(got, want) {
		t.Fatalf("filtered results = %v, want %v", got, want)
	}
}

func TestTelemProviderDomainFilterNeverAsksForFewerThanRequested(t *testing.T) {
	provider := TelemProvider{}
	cases := []struct {
		numResults int
		domains    []string
		want       int
	}{
		{numResults: 5, want: 5},
		{numResults: 5, domains: []string{"go.dev"}, want: 10},
		{numResults: 15, domains: []string{"go.dev"}, want: 15},
	}
	for _, tc := range cases {
		request := provider.searchRequest(
			SearchRequest{Query: "q", NumResults: tc.numResults, Domains: tc.domains},
			len(tc.domains) > 0,
		)
		if request.Search.NumResults != tc.want {
			t.Errorf("num_results %d, domains %v: sent %d, want %d",
				tc.numResults, tc.domains, request.Search.NumResults, tc.want)
		}
	}
}

func TestTelemProviderErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		retryAfter string
		body       string
		wantCode   string
		retryable  bool
	}{
		{"unauthorized", http.StatusUnauthorized, "", `{"detail":"Invalid API key"}`, ErrorCodeProviderFailed, false},
		{"no credits", http.StatusPaymentRequired, "", `{"detail":"Insufficient prepaid credits"}`,
			ErrorCodeProviderFailed, false},
		{"bad request", http.StatusBadRequest, "", `{"detail":"unknown provider"}`, ErrorCodeProviderFailed, false},
		{"long rate limit", http.StatusTooManyRequests, "60", `{"detail":"Rate limit exceeded"}`,
			ErrorCodeRateLimited, true},
		{"failure after the search ran", http.StatusServiceUnavailable, "",
			`{"detail":{"code":"all_providers_failed","reason":"provider_errors"}}`, ErrorCodeProviderFailed, false},
		{"server error", http.StatusInternalServerError, "", `{"detail":"boom"}`, ErrorCodeProviderFailed, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			_, err := telemProviderForServer(t, server).Search(
				context.Background(),
				SearchRequest{Query: "q", NumResults: 1},
			)
			providerErr, ok := AsProviderError(err)
			if !ok {
				t.Fatalf("expected ProviderError, got %v", err)
			}
			if providerErr.Code != tc.wantCode || providerErr.Retryable != tc.retryable || attempts != 1 {
				t.Fatalf("got code=%s retryable=%v attempts=%d, want code=%s retryable=%v attempts=1",
					providerErr.Code, providerErr.Retryable, attempts, tc.wantCode, tc.retryable)
			}
		})
	}
}

func TestTelemProviderRetriesFailuresThatDidNotRunOnce(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		retryAfter string
		body       string
	}{
		{"auth unavailable", http.StatusServiceUnavailable, "", `{"detail":"service unavailable"}`},
		{"no key capacity", http.StatusServiceUnavailable, "1",
			`{"detail":{"code":"all_providers_failed","reason":"no_provider_key_capacity"}}`},
		{"short rate limit", http.StatusTooManyRequests, "1", `{"detail":"Rate limit exceeded"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if attempts == 1 {
					if tc.retryAfter != "" {
						w.Header().Set("Retry-After", tc.retryAfter)
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
					return
				}
				writeTelemRuns(t, w, telemRunJSON("exa", "succeeded", "https://ok.example/"))
			}))
			defer server.Close()

			resp, err := telemProviderForServer(t, server).Search(
				context.Background(),
				SearchRequest{Query: "q", NumResults: 1},
			)
			if err != nil {
				t.Fatalf("search after retry: %v", err)
			}
			if attempts != 2 || len(resp.Results) != 1 {
				t.Fatalf("attempts=%d results=%d, want 2 and 1", attempts, len(resp.Results))
			}
		})
	}
}

func TestTelemProviderDoesNotFollowRedirects(t *testing.T) {
	redirected := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			redirected = true
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	_, err := telemProviderForServer(t, server).Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
	if err == nil || redirected {
		t.Fatalf("redirect followed=%v err=%v, want an error without following", redirected, err)
	}
}

func TestTelemProviderResponseSizeCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, telemMaxResponseSize+1))
	}))
	defer server.Close()

	_, err := telemProviderForServer(t, server).Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
	if providerErr, ok := AsProviderError(err); !ok || providerErr.Code != ErrorCodeProviderFailed {
		t.Fatalf("expected provider failure for oversized response, got %v", err)
	}
}

func TestTelemProviderTimeoutIsNotRetryable(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	provider := telemProviderForServer(t, server)
	provider.HTTPClient.Timeout = 50 * time.Millisecond
	_, err := provider.Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
	providerErr, ok := AsProviderError(err)
	if !ok || providerErr.Retryable {
		t.Fatalf("timeout error = %v, want a non-retryable provider error (the search may already have run)", err)
	}
}

func TestTelemDomainEntriesAreNormalized(t *testing.T) {
	cases := []struct {
		entry, url string
		match      bool
	}{
		{"www.nytimes.com", "https://nytimes.com/a", true},
		{"https://arxiv.org", "https://arxiv.org/abs/1", true},
		{"github.com/golang", "https://github.com/golang/go", true},
		{"arxiv.org.", "https://arxiv.org/abs/1", true},
		{"*.gov", "https://www.nasa.gov/", true},
		{"example.com:8080", "https://example.com/", true},
		{"GO.dev", "https://blog.go.dev/", true},
		{"go.dev", "https://notgo.dev/", false},
	}
	for _, tc := range cases {
		if got := urlMatchesDomains(tc.url, []string{tc.entry}, nil); got != tc.match {
			t.Errorf("include %q, url %q: match = %v, want %v", tc.entry, tc.url, got, tc.match)
		}
		if got := urlMatchesDomains(tc.url, nil, []string{tc.entry}); got == tc.match {
			t.Errorf("exclude %q, url %q: kept = %v, want %v", tc.entry, tc.url, got, !tc.match)
		}
	}
}

func TestTelemDedupeKeyMatchesSpellingsOfOnePage(t *testing.T) {
	same := [][2]string{
		{"https://go.dev/doc/", "https://go.dev/doc"},
		{"https://go.dev/doc#install", "https://go.dev/doc"},
		{"http://GO.dev/doc", "https://go.dev/doc"},
		{"https://www.go.dev/doc", "https://go.dev/doc"},
		{"https://go.dev/doc?b=2&a=1&utm_source=x&fbclid=y&empty=", "https://go.dev/doc?a=1&b=2"},
	}
	for _, pair := range same {
		if telemDedupeKey(pair[0]) != telemDedupeKey(pair[1]) {
			t.Errorf("keys differ: %q -> %q, %q -> %q",
				pair[0], telemDedupeKey(pair[0]), pair[1], telemDedupeKey(pair[1]))
		}
	}
	different := [][2]string{
		{"https://go.dev/doc", "https://go.dev/Doc"},
		{"https://go.dev:8443/doc", "https://go.dev/doc"},
		{"https://go.dev/doc?a=1", "https://go.dev/doc?a=2"},
		// Go's query parser drops pairs holding ";" or a bad escape.
		{"https://go.dev/doc?a=1;b=2", "https://go.dev/doc?a=3;b=4"},
		{"https://go.dev/doc?q=%zz", "https://go.dev/doc?q=%yy"},
	}
	for _, pair := range different {
		if telemDedupeKey(pair[0]) == telemDedupeKey(pair[1]) {
			t.Errorf("keys equal for different pages %q and %q", pair[0], pair[1])
		}
	}
}

func TestTelemProviderCapsSnippetsLikeExa(t *testing.T) {
	long := strings.Repeat("é", exaSnippetChars+50)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeTelemRuns(t, w, map[string]any{
			"preprocessor_name": "exa",
			"status":            "succeeded",
			"output_payload": map[string]any{"results": []any{
				map[string]any{"url": "https://ok.example/", "title": "OK", "summary": long},
			}},
		})
	}))
	defer server.Close()

	resp, err := telemProviderForServer(t, server).Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
	if err != nil || len(resp.Results) != 1 {
		t.Fatalf("search: %v %+v", err, resp)
	}
	if got := utf8.RuneCountInString(resp.Results[0].Snippet); got != exaSnippetChars {
		t.Fatalf("snippet length = %d runes, want %d", got, exaSnippetChars)
	}
}
