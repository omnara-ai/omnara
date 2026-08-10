package unikraft

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnikraftRESTClientCreatePayloadAndDelete(t *testing.T) {
	var sawCreate bool
	var sawDelete bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("authorization header = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/instances":
			sawCreate = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			if _, ok := body["scale_to_zero"]; ok {
				t.Fatalf("create body must omit scale_to_zero: %+v", body)
			}
			if _, ok := body["timeout_s"]; ok {
				t.Fatalf("create body must omit timeout_s: %+v", body)
			}
			if body["restart_policy"] != "never" {
				t.Fatalf("restart_policy = %v", body["restart_policy"])
			}
			if body["vcpus"] != float64(1) || body["cpu"] != nil {
				t.Fatalf("create body cpu fields = %+v, want vcpus only", body)
			}
			if body["volumes"] != nil {
				t.Fatalf("create body must omit volumes: %+v", body)
			}
			_, _ = w.Write([]byte(`{"status":"success","data":{"instances":[{"uuid":"uuid-1","name":"omnara-test"}]}}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/instances/uuid-1":
			sawDelete = true
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read delete body: %v", err)
			}
			if len(raw) != 0 {
				t.Fatalf("delete body = %s, want empty", raw)
			}
			_, _ = w.Write([]byte(`{"status":"success","data":{"instances":[{"uuid":"uuid-1"}]}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client, err := newTestRESTClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("new rest client: %v", err)
	}
	if _, err := client.CreateInstance(context.Background(), createInstanceRequest{
		Name:          "omnara-test",
		Image:         "image",
		Args:          []string{},
		Env:           map[string]string{"X": "Y"},
		MemoryMB:      1024,
		VCPUs:         1,
		Autostart:     true,
		RestartPolicy: "never",
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	if err := client.DeleteInstanceByUUID(context.Background(), "uuid-1"); err != nil {
		t.Fatalf("delete instance: %v", err)
	}
	if !sawCreate || !sawDelete {
		t.Fatalf("saw create=%v delete=%v", sawCreate, sawDelete)
	}
}

func TestUnikraftRESTClientValidatesBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantErr bool
	}{
		{name: "origin", baseURL: "https://api.example.test"},
		{name: "origin with trailing slash", baseURL: "https://api.example.test/"},
		{name: "local http origin", baseURL: "http://127.0.0.1:1234", wantErr: true},
		{name: "remote http origin", baseURL: "http://api.example.test", wantErr: true},
		{name: "path", baseURL: "https://api.example.test/v1", wantErr: true},
		{name: "query", baseURL: "https://api.example.test?debug=1", wantErr: true},
		{name: "fragment", baseURL: "https://api.example.test#frag", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := normalizeAPIBaseURL(tt.baseURL)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("normalize api base url: %v", err)
			}
		})
	}
}

func TestUnikraftRESTClientNameLookupUsesExactServerSideFilter(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/instances" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		if got := r.URL.Query().Get("details"); got != "false" {
			t.Fatalf("details query = %q, want false", got)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read lookup body: %v", err)
		}
		var lookup []struct {
			Name string `json:"name,omitempty"`
		}
		if err := json.Unmarshal(raw, &lookup); err != nil {
			t.Fatalf("decode lookup body: %v", err)
		}
		if len(lookup) != 1 || lookup[0].Name != "omnara-wanted" {
			t.Fatalf("lookup body = %+v, want exact name lookup", lookup)
		}
		_, _ = w.Write(
			[]byte(
				`{"status":"success","data":{"instances":[{"status":"success","uuid":"uuid-wanted","name":"omnara-wanted"}]}}`,
			),
		)
	}))
	defer server.Close()

	client, err := newTestRESTClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("new rest client: %v", err)
	}
	instance, found, err := client.GetInstanceByName(context.Background(), "omnara-wanted")
	if err != nil {
		t.Fatalf("get instance by name: %v", err)
	}
	if !found || instance.UUID != "uuid-wanted" {
		t.Fatalf("lookup = %+v found=%t, want uuid-wanted", instance, found)
	}
}

func TestUnikraftRESTClientNameLookupTreatsItemNotFoundAsMissing(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/instances" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write(
			[]byte(
				`{"status":"error","message":"Failed to perform all operations",` +
					`"data":{"instances":[{"status":"error","name":"omnara-missing","error":8}]}}`,
			),
		)
	}))
	defer server.Close()

	client, err := newTestRESTClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("new rest client: %v", err)
	}
	_, found, err := client.GetInstanceByName(context.Background(), "omnara-missing")
	if err != nil {
		t.Fatalf("get instance by name: %v", err)
	}
	if found {
		t.Fatal("expected missing instance to be classified as not found")
	}
}

func TestUnikraftRESTClientNameLookupRejectsTopLevelErrors(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/instances" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write(
			[]byte(
				`{"status":"error","errors":[{"status":404}],` +
					`"data":{"instances":[{"status":"error","name":"omnara-missing","error":8}]}}`,
			),
		)
	}))
	defer server.Close()

	client, err := newTestRESTClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("new rest client: %v", err)
	}
	_, found, err := client.GetInstanceByName(context.Background(), "omnara-missing")
	if err == nil || found {
		t.Fatalf("lookup with top-level errors = found %t error %v, want an error", found, err)
	}
}

func TestUnikraftRESTClientNameLookupRejectsAmbiguousResponses(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{
			name:     "success envelope with no item",
			response: `{"status":"success","data":{"instances":[]}}`,
		},
		{
			name:     "error envelope with no item",
			response: `{"status":"error","data":{"instances":[]}}`,
		},
		{
			name: "duplicate exact matches",
			response: `{"status":"success","data":{"instances":[` +
				`{"status":"success","uuid":"first","name":"omnara-duplicate"},` +
				`{"status":"success","uuid":"second","name":"omnara-duplicate"}]}}`,
		},
		{
			name: "success row with a different name",
			response: `{"status":"success","data":{"instances":[` +
				`{"status":"success","uuid":"uuid-1","name":"another-name"}]}}`,
		},
		{
			name: "not found row with a different name",
			response: `{"status":"error","data":{"instances":[` +
				`{"status":"error","name":"another-name","error":8}]}}`,
		},
		{
			name: "not found row without a name",
			response: `{"status":"error","data":{"instances":[` +
				`{"status":"error","error":8}]}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/instances" {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
				}
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client, err := newTestRESTClient(server.URL, "test-token", server.Client())
			if err != nil {
				t.Fatalf("new rest client: %v", err)
			}
			_, found, err := client.GetInstanceByName(context.Background(), "omnara-duplicate")
			if err == nil || found {
				t.Fatalf("ambiguous lookup = found %t error %v, want an error", found, err)
			}
		})
	}
}

func TestUnikraftRESTClientRuntimeBatchLookupRequestsStateAndPreservesPartialResults(
	t *testing.T,
) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/instances" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		if got := r.URL.Query().Get("details"); got != "true" {
			t.Fatalf("details query = %q, want true", got)
		}
		var lookups []instanceUUIDLookup
		if err := json.NewDecoder(r.Body).Decode(&lookups); err != nil {
			t.Fatalf("decode batch lookup: %v", err)
		}
		if len(lookups) != 2 || lookups[0].UUID != "uuid-running" ||
			lookups[1].UUID != "uuid-missing" {
			t.Fatalf("batch lookup = %+v", lookups)
		}
		_, _ = w.Write([]byte(
			`{"status":"partial_success","data":{"instances":[` +
				`{"status":"success","uuid":"uuid-running","name":"owned","state":"running"},` +
				`{"status":"error","uuid":"uuid-missing","error":8}]}}`,
		))
	}))
	defer server.Close()

	client, err := newTestRESTClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("new rest client: %v", err)
	}
	batch, err := client.GetInstancesByUUIDs(
		context.Background(),
		[]string{"uuid-running", "uuid-missing"},
	)
	if err != nil {
		t.Fatalf("get instances by uuid: %v", err)
	}
	if batch.cleanEnvelope() {
		t.Fatal("partial-success batch must not be authoritative for exact confirmation")
	}
	if !batch.successfulItemsAuthoritative() {
		t.Fatal("partial-success batch must preserve authoritative per-item results")
	}
	if batch.EnvelopeStatus != responseStatusPartialSuccess || batch.HasEnvelopeErrors {
		t.Fatalf("batch envelope = %+v, want partial success without envelope errors", batch)
	}
	if len(batch.Instances) != 2 || batch.Instances[0].State != "running" ||
		batch.Instances[0].Status != "success" ||
		batch.Instances[1].Error != instanceNotFoundErrorCode {
		t.Fatalf("batch results = %+v", batch.Instances)
	}
}

func TestUnikraftRESTClientRuntimeBatchPreservesMalformedErrorEnvelopeForFailOpenHandling(
	t *testing.T,
) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(
			`{"status":"error","data":{"instances":[` +
				`{"status":"success","uuid":"uuid-standby","name":"owned","state":"standby"}` +
				`]}}`,
		))
	}))
	defer server.Close()

	client, err := newTestRESTClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("new rest client: %v", err)
	}
	batch, err := client.GetInstancesByUUIDs(context.Background(), []string{"uuid-standby"})
	if err != nil {
		t.Fatalf("get instances by uuid: %v", err)
	}
	if batch.cleanEnvelope() || batch.successfulItemsAuthoritative() {
		t.Fatalf("error batch unexpectedly authoritative: %+v", batch)
	}
	if len(batch.Instances) != 1 || batch.Instances[0].State != instanceStateStandby {
		t.Fatalf("batch results = %+v, want preserved standby item", batch.Instances)
	}
}

func TestUnikraftRESTClientUUIDLookupTreatsTypedErrorNotFoundAsMissing(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/instances/uuid-missing" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(
			`{"status":"error","data":{"instances":[` +
				`{"status":"error","uuid":"uuid-missing","error":8}]}}`,
		))
	}))
	defer server.Close()

	client, err := newTestRESTClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("new rest client: %v", err)
	}
	_, found, err := client.GetInstanceByUUID(context.Background(), "uuid-missing")
	if err != nil {
		t.Fatalf("get instance by uuid: %v", err)
	}
	if found {
		t.Fatal("typed per-item not-found must be classified as missing")
	}
}

func TestUnikraftRESTClientUUIDLookupRejectsAmbiguousSuccess(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "missing result", data: `{"instances":[]}`},
		{
			name: "duplicate results",
			data: `{"instances":[` +
				`{"status":"success","uuid":"uuid-1"},` +
				`{"status":"success","uuid":"uuid-1"}]}`,
		},
		{
			name: "missing uuid",
			data: `{"instances":[{"status":"success","state":"running"}]}`,
		},
		{
			name: "mismatched uuid",
			data: `{"instances":[` +
				`{"status":"success","uuid":"uuid-2","state":"running"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"status":"success","data":` + tt.data + `}`))
			}))
			defer server.Close()

			client, err := newTestRESTClient(server.URL, "test-token", server.Client())
			if err != nil {
				t.Fatalf("new rest client: %v", err)
			}
			if _, _, err := client.GetInstanceByUUID(
				context.Background(),
				"uuid-1",
			); err == nil {
				t.Fatal("ambiguous uuid lookup must fail open with an error")
			}
		})
	}
}

func TestUnikraftRESTClientUUIDLookupRejectsMismatchedNotFoundIdentity(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{
			name: "mismatched uuid",
			response: `{"status":"error","data":{"instances":[` +
				`{"status":"error","uuid":"uuid-other","error":8}]}}`,
		},
		{
			name: "missing uuid",
			response: `{"status":"error","data":{"instances":[` +
				`{"status":"error","error":8}]}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/instances/uuid-wanted" {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
				}
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client, err := newTestRESTClient(server.URL, "test-token", server.Client())
			if err != nil {
				t.Fatalf("new rest client: %v", err)
			}
			_, found, err := client.GetInstanceByUUID(context.Background(), "uuid-wanted")
			if err == nil || found {
				t.Fatalf("ambiguous not-found lookup = found %t error %v, want an error", found, err)
			}
		})
	}
}

func TestUnikraftRESTClientRejectsLogicalErrorEnvelope(t *testing.T) {
	tests := []struct {
		name string
		call func(*restClient) error
	}{
		{
			name: "create",
			call: func(client *restClient) error {
				_, err := client.CreateInstance(
					context.Background(),
					createInstanceRequest{
						Name:          "omnara-test",
						Image:         "image",
						Args:          []string{},
						Env:           map[string]string{},
						MemoryMB:      1024,
						VCPUs:         1,
						Autostart:     true,
						RestartPolicy: "never",
					},
				)
				return err
			},
		},
		{
			name: "delete",
			call: func(client *restClient) error {
				return client.DeleteInstanceByUUID(context.Background(), "uuid-1")
			},
		},
		{
			name: "get",
			call: func(client *restClient) error {
				_, _, err := client.GetInstanceByUUID(context.Background(), "uuid-1")
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(
					[]byte(
						`{"status":"error","message":"bad env OMNARA_MACHINE_TOKEN=secret","errors":[{"status":409,"message":"secret"}]}`,
					),
				)
			}))
			defer server.Close()

			client, err := newTestRESTClient(server.URL, "test-token", server.Client())
			if err != nil {
				t.Fatalf("new rest client: %v", err)
			}
			err = tt.call(client)
			if err == nil {
				t.Fatal("expected logical error envelope to fail")
			}
			if !strings.Contains(err.Error(), "status 409") {
				t.Fatalf("error = %v, want sanitized provider status", err)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "OMNARA_MACHINE_TOKEN") {
				t.Fatalf("provider error leaked response text: %v", err)
			}
		})
	}
}

func TestUnikraftAPIErrorDoesNotExposeProviderMessage(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(
			[]byte(
				`{"status":"error","message":"bad env OMNARA_MACHINE_TOKEN=secret","errors":[{"status":400,"message":"secret"}]}`,
			),
		)
	}))
	defer server.Close()

	client, err := newTestRESTClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("new rest client: %v", err)
	}
	err = client.DeleteInstanceByUUID(context.Background(), "uuid-1")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "OMNARA_MACHINE_TOKEN") {
		t.Fatalf("provider error leaked response text: %v", err)
	}
}
