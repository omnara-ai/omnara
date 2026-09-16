package channelconnector

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveAddressScopeIsInstallationOnly(t *testing.T) {
	request := operationRequest()
	request.Kind = OperationResolveAddress
	request.Scope.AgentID, request.Scope.ChannelID = "", ""
	request.Payload = json.RawMessage(`{"provider_ref":"123456789"}`)
	raw, err := prepareOperation(request, time.Now().Add(time.Minute))
	require.NoError(t, err)
	var envelope struct {
		Scope map[string]string `json:"scope"`
	}
	require.NoError(t, json.Unmarshal(raw, &envelope))
	require.Equal(t, map[string]string{
		"project_id": "project", "integration_app_id": "app", "integration_install_id": "install",
	}, envelope.Scope)
	for _, mutate := range []func(*OperationRequest){
		func(r *OperationRequest) { r.Scope.AgentID = "agent" },
		func(r *OperationRequest) { r.Scope.ChannelID = "channel" },
		func(r *OperationRequest) { r.Scope.IntegrationInstallID = "" },
		func(r *OperationRequest) { r.Artifacts = []OperationArtifact{{ID: "artifact"}} },
	} {
		invalid := request
		mutate(&invalid)
		_, err := prepareOperation(invalid, time.Now().Add(time.Minute))
		require.Error(t, err)
	}
	for _, kind := range []OperationKind{OperationSend, OperationRead, OperationInteraction} {
		r := request
		r.Kind = kind
		_, err := prepareOperation(r, time.Now().Add(time.Minute))
		require.Error(t, err, "agent operations still require agent/channel: %s", kind)
	}
}

func TestResolveAddressTransportFailureHasNoPublicationAmbiguity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	request := operationRequest()
	request.Kind = OperationResolveAddress
	request.Scope.AgentID, request.Scope.ChannelID = "", ""
	client := operationClient(t, operationConfig(t, server.URL))
	result, err := client.Execute(operationContext(t), request)
	assertOperationError(t, result, err, OperationFailed, "http_rejected")
}
