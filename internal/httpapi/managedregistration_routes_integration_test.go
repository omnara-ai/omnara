//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const managedRegistrationBody = `{"source":"managed","provider_ref":"native-locator","provider_ref_kind":"thread"}`

func TestManagedChannelRegistrationPublishesDefinitionsAndRegistersUnboundParents(t *testing.T) {
	t.Parallel()
	f := newManagedRegistrationHTTPFixture(t, resolveManagedRegistrationAddress)
	first := f.post(t, f.path, managedRegistrationBody, f.project.AdminToken, http.StatusOK)
	second := f.post(t, f.path, managedRegistrationBody, f.project.AdminToken, http.StatusOK)
	require.Equal(t, first, second, "canonical registration preserves both channel identities")
	require.Equal(t, int64(2), f.calls.Load(), "each explicit request resolves once, without hidden retries")
	childID := mustPublicHTTPID(t, publicid.KindIntegrationTarget, channelReceiptString(t, first, "channel_id"))
	parentID := mustPublicHTTPID(t, publicid.KindIntegrationTarget, channelReceiptString(t, first, "parent_channel_id"))
	child, err := f.project.Store.Integrations().GetIntegrationTarget(t.Context(), f.install.ProjectID, childID)
	require.NoError(t, err)
	parent, err := f.project.Store.Integrations().GetIntegrationTarget(t.Context(), f.install.ProjectID, parentID)
	require.NoError(t, err)
	require.Equal(t, parent.ID, child.ParentChannelID)
	require.Equal(t, uuid.Nil, parent.ParentChannelID)
	require.Equal(t, "canonical-thread", child.ProviderRef)
	require.Equal(t, "canonical-parent", parent.ProviderRef)
	require.Equal(t, "thread", child.ProviderRefKind)
	require.Equal(t, "channel", parent.ProviderRefKind)
	for _, target := range []integrationstore.IntegrationTargetRecord{parent, child} {
		require.Equal(t, f.install.ProjectID, target.ProjectID)
		require.Equal(t, f.install.ID, target.IntegrationInstallID)
	}
	definition, err := f.project.Store.Integrations().GetChannelDefinition(
		t.Context(), f.install.ProjectID, f.install.ID, child.ChannelDefinitionID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.ChannelKindDiscordThread, definition.Kind)
	var definitions int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM integration_channel_definitions
WHERE integration_install_id=$1`, f.install.ID).Scan(&definitions))
	require.Equal(t, 2, definitions, "the resolver's real publication callbacks reuse their definitions")
	requireManagedRegistrationState(t, f, 2)
}

func TestManagedChannelRegistrationRejectsWrongSourceAndProjectBeforeResolver(t *testing.T) {
	t.Parallel()
	f := newManagedRegistrationHTTPFixture(t, resolveManagedRegistrationAddress)
	external, err := f.project.Store.Integrations().CreateExternalIntegrationInstall(t.Context(),
		integrationstore.CreateExternalIntegrationInstallInput{
			OrgID: f.project.OrgUUID, ProjectID: f.project.ProjectUUID,
			InstalledBy: identitystore.NewUserPrincipal(f.project.AdminUserUUID), DisplayName: "Customer connector",
		})
	require.NoError(t, err)
	definition, err := f.project.Store.Integrations().PublishExternalChannelDefinition(t.Context(),
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: f.project.ProjectUUID, IntegrationInstallID: external.ID, ImplementationKey: "customer",
			Kind: integrationstore.ChannelKindExternal, SendParamsSchema: json.RawMessage(`{"type":"object"}`),
			Capabilities: integrationstore.ChannelCapabilities{Send: true, Text: true},
		})
	require.NoError(t, err)
	externalBody := workflowHTTPJSON(t, map[string]any{
		"source": "external", "definition_id": testPublicID(t, publicid.KindChannelDefinition, definition.ID),
		"provider_ref": "customer-address", "provider_ref_kind": "conversation", "name": "Customer channel",
	})
	f.post(t, f.path, externalBody, f.project.AdminToken, http.StatusNotFound)
	installID := testPublicID(t, publicid.KindIntegrationInstall, f.install.ID)
	for _, otherID := range []uuid.UUID{external.ID, f.otherInstall.ID} {
		path := strings.Replace(f.path, installID, testPublicID(t, publicid.KindIntegrationInstall, otherID), 1)
		f.post(t, path, managedRegistrationBody, f.project.AdminToken, http.StatusNotFound)
	}
	require.Zero(t, f.calls.Load())
	requireManagedRegistrationState(t, f, 0)
}

func TestManagedChannelRegistrationRechecksDisconnectAfterResolver(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	releaseResolver := sync.OnceFunc(func() { close(release) })
	resolve := func(ctx context.Context, f *managedRegistrationHTTPFixture) (json.RawMessage, error) {
		address, err := resolveManagedRegistrationAddress(ctx, f)
		if err != nil {
			return nil, err
		}
		close(entered)
		select {
		case <-release:
			return address, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := newManagedRegistrationHTTPFixture(t, resolve)
	t.Cleanup(releaseResolver)
	done := make(chan *httptest.ResponseRecorder, 1)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, f.path,
		strings.NewReader(managedRegistrationBody))
	request.Header.Set("Authorization", "Bearer "+f.project.AdminToken)
	request.Header.Set("Content-Type", "application/json")
	go func() {
		response := httptest.NewRecorder()
		f.handler.ServeHTTP(response, request)
		done <- response
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("resolver could not publish definitions back to core")
	}
	requireManagedRegistrationState(t, f, 0)
	// Deletion must complete while resolution is still paused, proving that
	// the public registration request holds no installation transaction locks.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	raw, status, err := managedRegistrationHTTP(ctx, http.MethodDelete,
		f.coreURL+strings.TrimSuffix(f.path, "/channels"), f.project.AdminToken, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status, string(raw))
	releaseResolver()
	select {
	case response := <-done:
		require.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not finish after resolver returned")
	}
	require.Equal(t, int64(1), f.calls.Load())
	requireManagedRegistrationState(t, f, 0)
}

func TestManagedChannelRegistrationRejectsMalformedResolverAddress(t *testing.T) {
	t.Parallel()
	cases := []string{
		"non_object", "invalid_definition_id", "duplicate_metadata", "unknown_scope_field", "case_alias", "null_parent",
		"nested_parent_case_alias", "nested_parent_null_metadata", "nested_parent_null_name", "nested_parent_parent",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newManagedRegistrationHTTPFixture(t,
				func(ctx context.Context, f *managedRegistrationHTTPFixture) (json.RawMessage, error) {
					if name == "non_object" {
						return json.RawMessage(`[]`), nil
					}
					raw, err := resolveManagedRegistrationAddress(ctx, f)
					if err != nil {
						return nil, err
					}
					var address map[string]json.RawMessage
					if err := json.Unmarshal(raw, &address); err != nil {
						return nil, err
					}
					switch name {
					case "invalid_definition_id":
						address["definition_id"] = json.RawMessage(`"not-a-definition"`)
					case "duplicate_metadata":
						address["provider_metadata"] = json.RawMessage(`{"nested":[{"key":1,"\u006bey":2}]}`)
					case "unknown_scope_field":
						address["project_id"] = json.RawMessage(`"untrusted-project"`)
					case "case_alias":
						address["Provider_Ref"] = address["provider_ref"]
						delete(address, "provider_ref")
					case "null_parent":
						address["parent"] = json.RawMessage(`null`)
					default:
						var parent map[string]json.RawMessage
						if err := json.Unmarshal(address["parent"], &parent); err != nil {
							return nil, err
						}
						switch name {
						case "nested_parent_case_alias":
							parent["Provider_Ref"] = parent["provider_ref"]
							delete(parent, "provider_ref")
						case "nested_parent_null_metadata":
							parent["provider_metadata"] = json.RawMessage(`null`)
						case "nested_parent_null_name":
							parent["display_name"] = json.RawMessage(`null`)
						case "nested_parent_parent":
							parent["parent"] = json.RawMessage(`{}`)
						}
						address["parent"], err = json.Marshal(parent)
						if err != nil {
							return nil, err
						}
					}
					return json.Marshal(address)
				})
			f.post(t, f.path, managedRegistrationBody, f.project.AdminToken, http.StatusServiceUnavailable)
			require.Equal(t, int64(1), f.calls.Load())
			requireManagedRegistrationState(t, f, 0)
		})
	}
}

func TestManagedChannelRegistrationMapsOnlyFixedResolverFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, payload string
		outcome       channelconnector.OperationOutcome
		gatewayStatus int
		status        int
		code          openapi.ErrorCode
	}{
		{"invalid", `{"code":"invalid_address"}`,
			channelconnector.OperationFailed, 200, 400, openapi.ErrorCodeInvalidRequest},
		{"unsupported", `{"code":"unsupported_address"}`,
			channelconnector.OperationFailed, 200, 400, openapi.ErrorCodeInvalidRequest},
		{"unavailable", `{"code":"address_unavailable"}`,
			channelconnector.OperationFailed, 200, 404, openapi.ErrorCodeNotFound},
		{"bare_failure", ``, channelconnector.OperationFailed, 200, 503, openapi.ErrorCodeServiceUnavailable},
		{"outage", ``, channelconnector.OperationFailed, 502, 503, openapi.ErrorCodeServiceUnavailable},
		{"unknown_outcome", `{"code":"invalid_address"}`,
			channelconnector.OperationUnknown, 200, 503, openapi.ErrorCodeServiceUnavailable},
		{"raw_detail", ``, channelconnector.OperationFailed, 200, 503, openapi.ErrorCodeServiceUnavailable},
		{"unknown_code", ``, channelconnector.OperationFailed, 200, 503, openapi.ErrorCodeServiceUnavailable},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			const detail = "private-provider-diagnostic"
			f := newManagedRegistrationGatewayFixture(t, func(
				w http.ResponseWriter, _ *http.Request, f *managedRegistrationHTTPFixture, operation managedRegistrationOperation,
			) {
				if test.name == "outage" {
					http.Error(w, detail+" "+f.token, test.gatewayStatus)
					return
				}
				payload := test.payload
				switch test.name {
				case "raw_detail":
					payload = fmt.Sprintf(`{"code":"invalid_address","detail":%q}`, detail+" "+f.token)
				case "unknown_code":
					payload = fmt.Sprintf(`{"code":%q}`, detail+" "+f.token)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.gatewayStatus)
				assert.NoError(t, json.NewEncoder(w).Encode(channelconnector.OperationResult{
					RequestID: operation.RequestID, Outcome: test.outcome, Payload: json.RawMessage(payload),
				}))
			})
			response := f.post(t, f.path, managedRegistrationBody, f.project.AdminToken, test.status)
			require.Equal(t, string(test.code), response["code"])
			raw := workflowHTTPJSON(t, response)
			require.NotContains(t, raw, detail)
			require.NotContains(t, raw, f.token)
			require.Equal(t, int64(1), f.calls.Load(), "mapping a failure must not retry the resolver")
			requireManagedRegistrationState(t, f, 0)
		})
	}
}

func TestManagedChannelRegistrationReusesExistingUnparentedAddressOverHTTP(t *testing.T) {
	t.Parallel()
	f := newManagedRegistrationHTTPFixture(t, resolveManagedRegistrationAddress)
	raw, err := resolveManagedRegistrationAddress(t.Context(), f)
	require.NoError(t, err)
	var address openapi.ChannelRegistrationTarget
	require.NoError(t, json.Unmarshal(raw, &address))
	input := integrationstore.CreateIntegrationTargetInput{
		ProjectID: f.install.ProjectID, IntegrationInstallID: f.install.ID,
		ChannelDefinitionID: mustPublicHTTPID(t, publicid.KindChannelDefinition, address.DefinitionId),
		ProviderRef:         address.ProviderRef, ProviderRefKind: address.ProviderRefKind,
	}
	existing, err := f.project.Store.Integrations().CreateIntegrationTarget(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, existing.ParentChannelID)
	for range 2 {
		response := f.post(t, f.path, managedRegistrationBody, f.project.AdminToken, http.StatusOK)
		require.Equal(t, testPublicID(t, publicid.KindIntegrationTarget, existing.ID), response["channel_id"])
		require.Nil(t, response["parent_channel_id"], "resolver parentage cannot replace an existing unparented identity")
		requireManagedRegistrationState(t, f, 1)
	}
	stored, err := f.project.Store.Integrations().GetIntegrationTarget(t.Context(), f.install.ProjectID, existing.ID)
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, stored.ParentChannelID)
	require.Equal(t, int64(2), f.calls.Load())
}

func TestManagedChannelRegistrationValidatesParentIDWithoutReparenting(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"live", "wrong_project", "missing", "malformed", "retired"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var resolverParentID atomic.Pointer[string]
			f := newManagedRegistrationHTTPFixture(t,
				func(ctx context.Context, f *managedRegistrationHTTPFixture) (json.RawMessage, error) {
					raw, err := resolveManagedRegistrationAddress(ctx, f)
					if err != nil {
						return nil, err
					}
					var address openapi.ChannelRegistrationTarget
					if err := json.Unmarshal(raw, &address); err != nil {
						return nil, err
					}
					address.Parent, address.ParentChannelId = nil, resolverParentID.Load()
					return json.Marshal(address)
				})
			raw, err := resolveManagedRegistrationAddress(t.Context(), f)
			require.NoError(t, err)
			var address openapi.ChannelRegistrationTarget
			require.NoError(t, json.Unmarshal(raw, &address))
			input := integrationstore.CreateIntegrationTargetInput{
				ProjectID: f.install.ProjectID, IntegrationInstallID: f.install.ID,
				ChannelDefinitionID: mustPublicHTTPID(t, publicid.KindChannelDefinition, address.DefinitionId),
				ProviderRef:         address.ProviderRef, ProviderRefKind: address.ProviderRefKind,
			}
			existing, err := f.project.Store.Integrations().CreateIntegrationTarget(t.Context(), input)
			require.NoError(t, err)
			require.Equal(t, uuid.Nil, existing.ParentChannelID)
			parent := managedRegistrationParentIDFixture(t, f, *address.Parent, name == "wrong_project")
			id := testPublicID(t, publicid.KindIntegrationTarget, parent.ID)
			status := http.StatusNotFound
			switch name {
			case "live":
				status = http.StatusOK
			case "missing":
				id = testPublicID(t, publicid.KindIntegrationTarget, uuid.New())
			case "malformed":
				id, status = "not-a-channel-id", http.StatusServiceUnavailable
			case "retired":
				_, err = f.pool.Exec(t.Context(),
					`UPDATE integration_targets SET deleted_at=statement_timestamp() WHERE id=$1`, parent.ID)
				require.NoError(t, err)
			}
			resolverParentID.Store(&id)
			for range 2 {
				response := f.post(t, f.path, managedRegistrationBody, f.project.AdminToken, status)
				if name == "live" {
					require.Equal(t, testPublicID(t, publicid.KindIntegrationTarget, existing.ID), response["channel_id"])
					require.Nil(t, response["parent_channel_id"])
				} else {
					expected := openapi.ErrorCodeNotFound
					if name == "malformed" {
						expected = openapi.ErrorCodeServiceUnavailable
					}
					require.Equal(t, string(expected), response["code"])
				}
				requireManagedRegistrationState(t, f, 2)
			}
			stored, err := f.project.Store.Integrations().GetIntegrationTarget(t.Context(), f.install.ProjectID, existing.ID)
			require.NoError(t, err)
			require.Equal(t, existing.ID, stored.ID)
			require.Equal(t, uuid.Nil, stored.ParentChannelID, "parent IDs validate authority but cannot reparent this address")
			require.Equal(t, int64(2), f.calls.Load())
		})
	}
}

func managedRegistrationParentIDFixture(
	t *testing.T, f *managedRegistrationHTTPFixture, address openapi.ChannelRegistrationParent, otherProject bool,
) integrationstore.IntegrationTargetRecord {
	t.Helper()
	install := f.install
	definitionID := mustPublicHTTPID(t, publicid.KindChannelDefinition, address.DefinitionId)
	if otherProject {
		install = f.otherInstall
		definition, err := f.project.Store.Integrations().PublishConnectorChannelDefinition(t.Context(),
			integrationstore.PublishChannelDefinitionInput{
				ProjectID: install.ProjectID, IntegrationInstallID: install.ID,
				ImplementationKey: "foreign-parent", Kind: integrationstore.ChannelKindDiscordChannel,
				SendParamsSchema:      json.RawMessage(`{"type":"object","additionalProperties":false}`),
				Capabilities:          integrationstore.ChannelCapabilities{Send: true, Text: true},
				ConnectorCapabilities: []channelconnector.Capability{{ConnectorKey: f.app.ConnectorKey, Provider: f.app.Provider}},
			})
		require.NoError(t, err)
		definitionID = definition.ID
	}
	input := integrationstore.CreateIntegrationTargetInput{
		ProjectID: install.ProjectID, IntegrationInstallID: install.ID, ChannelDefinitionID: definitionID,
		ProviderRef: address.ProviderRef, ProviderRefKind: address.ProviderRefKind,
	}
	parent, err := f.project.Store.Integrations().CreateIntegrationTarget(t.Context(), input)
	require.NoError(t, err)
	return parent
}

type managedRegistrationHTTPFixture struct {
	channelReceiptHTTPFixture
	path, coreURL string
	calls         atomic.Int64
}

type managedRegistrationOperation struct {
	RequestID  string                         `json:"request_id"`
	Kind       channelconnector.OperationKind `json:"kind"`
	Capability channelconnector.Capability    `json:"capability"`
	Scope      map[string]string              `json:"scope"`
	Deadline   time.Time                      `json:"deadline"`
	Payload    json.RawMessage                `json:"payload"`
	Artifacts  []json.RawMessage              `json:"artifacts"`
}

func newManagedRegistrationHTTPFixture(
	t *testing.T, resolve func(context.Context, *managedRegistrationHTTPFixture) (json.RawMessage, error),
) *managedRegistrationHTTPFixture {
	t.Helper()
	return newManagedRegistrationGatewayFixture(t, func(
		w http.ResponseWriter, r *http.Request, f *managedRegistrationHTTPFixture, operation managedRegistrationOperation,
	) {
		payload, err := resolve(r.Context(), f)
		if !assert.NoError(t, err) {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(channelconnector.OperationResult{
			RequestID: operation.RequestID, Outcome: channelconnector.OperationCompleted, Payload: payload,
		}))
	})
}

func newManagedRegistrationGatewayFixture(
	t *testing.T,
	respond func(http.ResponseWriter, *http.Request, *managedRegistrationHTTPFixture, managedRegistrationOperation),
) *managedRegistrationHTTPFixture {
	t.Helper()
	f := &managedRegistrationHTTPFixture{channelReceiptHTTPFixture: newChannelReceiptHTTPFixture(t)}
	f.path = f.project.ProjectPath + "/integration-installs/" +
		testPublicID(t, publicid.KindIntegrationInstall, f.install.ID) + "/channels"
	core := httptest.NewUnstartedServer(nil)
	f.coreURL = "http://" + core.Listener.Addr().String()
	wantScope := map[string]string{
		"project_id":             f.project.ProjectID,
		"integration_app_id":     testPublicID(t, publicid.KindIntegrationApp, f.app.ID),
		"integration_install_id": testPublicID(t, publicid.KindIntegrationInstall, f.install.ID),
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/internal/operations", r.URL.Path)
		assert.Equal(t, "Bearer "+f.token, r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		var operation managedRegistrationOperation
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		decoder.DisallowUnknownFields()
		if !assert.NoError(t, decoder.Decode(&operation)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Equal(t, channelconnector.OperationResolveAddress, operation.Kind)
		assert.Equal(t, channelconnector.Capability{ConnectorKey: f.app.ConnectorKey, Provider: f.app.Provider},
			operation.Capability)
		assert.Equal(t, wantScope, operation.Scope, "resolution carries no agent or channel authority")
		assert.Empty(t, operation.Artifacts)
		assert.JSONEq(t, `{"provider_ref":"native-locator","provider_ref_kind":"thread"}`, string(operation.Payload))
		_, err := uuid.Parse(operation.RequestID)
		assert.NoError(t, err)
		assert.Equal(t, base64.RawURLEncoding.EncodeToString([]byte(operation.RequestID)),
			r.Header.Get(channelconnector.OperationRequestIDHeader))
		assert.True(t, operation.Deadline.After(time.Now()))
		assert.WithinDuration(t, time.Now(), operation.Deadline, 16*time.Second)
		respond(w, r, f, operation)
	}))
	t.Cleanup(gateway.Close)
	operations, err := channelconnector.NewOperationsClient([]channelconnector.Config{{
		ID: "registration", Token: f.token, OperationsURL: gateway.URL + "/internal/operations",
		Capabilities: []channelconnector.Capability{{ConnectorKey: f.app.ConnectorKey, Provider: f.app.Provider}},
	}}, gateway.Client())
	require.NoError(t, err)
	auth, err := channelconnector.NewAuthenticator([]channelconnector.Config{{
		ID: "registration", Token: f.token,
		Capabilities: []channelconnector.Capability{{ConnectorKey: f.app.ConnectorKey, Provider: f.app.Provider}},
	}})
	require.NoError(t, err)
	f.handler = newIntegrationServer(f.pool, WithChannelOperations(operations), WithChannelConnectorAuthenticator(auth),
		WithInternalAPIOrigins([]string{f.coreURL}), WithPublicAPIURL(f.coreURL),
		WithPublicURL("https://omnara.example.test"))
	core.Config.Handler = f.handler
	core.Start()
	t.Cleanup(core.Close)
	return f
}

func resolveManagedRegistrationAddress(
	ctx context.Context, f *managedRegistrationHTTPFixture,
) (json.RawMessage, error) {
	var ids [2]string
	for index, kind := range []openapi.ChannelKind{openapi.ChannelKindDiscordChannel, openapi.ChannelKindDiscordThread} {
		body, err := json.Marshal(openapi.PublishChannelConnectorDefinitionRequest{
			ImplementationKey: strings.ToLower(string(kind)), Kind: kind, Description: "Resolved provider address",
			SendParamsSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Capabilities:     openapi.ChannelCapabilities{Read: true, Send: true, Text: true},
		})
		if err != nil {
			return nil, err
		}
		appID, err := publicid.Encode(publicid.KindIntegrationApp, f.app.ID)
		if err != nil {
			return nil, err
		}
		installID, err := publicid.Encode(publicid.KindIntegrationInstall, f.install.ID)
		if err != nil {
			return nil, err
		}
		url := f.coreURL + "/api/v1/channel-connector/apps/" + appID + "/installations/" + installID +
			"/channel-definitions/publish"
		raw, status, err := managedRegistrationHTTP(ctx, http.MethodPost, url, f.token, body)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("definition publication returned status %d", status)
		}
		var definition struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &definition); err != nil {
			return nil, err
		}
		ids[index] = definition.ID
	}
	return json.Marshal(openapi.ChannelRegistrationTarget{
		DefinitionId: ids[1], ProviderRef: "canonical-thread", ProviderRefKind: "thread",
		Parent: &openapi.ChannelRegistrationParent{
			DefinitionId: ids[0], ProviderRef: "canonical-parent", ProviderRefKind: "channel",
		},
	})
}

func managedRegistrationHTTP(
	ctx context.Context, method, url, token string, body []byte,
) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	return raw, response.StatusCode, err
}

func requireManagedRegistrationState(t *testing.T, f *managedRegistrationHTTPFixture, wantTargets int) {
	t.Helper()
	var targets, agents, profiles, routes, bindings, inputs, receipts int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT
  (SELECT count(*) FROM integration_targets), (SELECT count(*) FROM agents),
  (SELECT count(*) FROM agent_profiles), (SELECT count(*) FROM integration_routes),
  (SELECT count(*) FROM integration_target_bindings), (SELECT count(*) FROM agent_inputs),
  (SELECT count(*) FROM integration_event_receipts)`).
		Scan(&targets, &agents, &profiles, &routes, &bindings, &inputs, &receipts))
	require.Equal(t, wantTargets, targets)
	require.Equal(t, []int{0, 0, 0, 0, 0, 0}, []int{agents, profiles, routes, bindings, inputs, receipts},
		"registration neither requires a profile nor creates execution or binding authority")
}
