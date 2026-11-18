package docker

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestDockerProviderConfigRequiresHTTPSDockerHost(t *testing.T) {
	config, err := parseProviderConfig(json.RawMessage(
		`{"api_base_url":"https://10.0.5.21:2376/","runtime":"runsc","allowed_images":["*"]}`,
	))
	require.NoError(t, err)
	require.Equal(t, "https://10.0.5.21:2376", config.APIBaseURL)
	require.Equal(t, "runsc", config.Runtime)

	for _, raw := range []string{
		`{}`,
		`{"api_base_url":"http://10.0.5.21:2375"}`,
		`{"api_base_url":"tcp://10.0.5.21:2376"}`,
		`{"api_base_url":"https://10.0.5.21:2376/v1.44"}`,
		`{"api_base_url":"https://10.0.5.21:2376","runtime":"run sc"}`,
		`{"api_base_url":"https://10.0.5.21:2376","host":"x"}`,
	} {
		_, err := parseProviderConfig(json.RawMessage(raw))
		require.Error(t, err, raw)
	}
}

func TestDockerCredentialNeedsCAAndMatchingClientPair(t *testing.T) {
	certs := newTestCertificates(t)
	other := newTestCertificates(t)
	tlsConfig, err := parseProviderCredential(credentialJSON(t, certs.caPEM, certs.clientCert, certs.clientKey))
	require.NoError(t, err)
	require.Len(t, tlsConfig.Certificates, 1)

	for name, raw := range map[string]string{
		"empty":         "",
		"not json":      "-----BEGIN CERTIFICATE-----",
		"unknown field": `{"ca":"x","cert":"x","key":"x","extra":"x"}`,
		"bad ca":        credentialJSON(t, "not pem", certs.clientCert, certs.clientKey),
		"mismatched":    credentialJSON(t, certs.caPEM, certs.clientCert, other.clientKey),
	} {
		_, err := parseProviderCredential(raw)
		require.Error(t, err, name)
	}
}

func TestDockerMachinesOnlyUsePoolImageUnlessAllowlisted(t *testing.T) {
	policy := func(providerConfig string) executionstore.MachinePoolProviderPolicy {
		cpu, memory := 2, 4096
		return executionstore.MachinePoolProviderPolicy{
			DefaultProvisioning: testMachineProvisioning(t, testImage, cpu, memory),
			ResourceLimits: executionstore.MachineResourceLimits{
				MaxTotalCPU: &cpu, MaxTotalMemoryMB: &memory, MaxMachineCPU: &cpu, MaxMachineMemoryMB: &memory,
			},
			ProviderConfig: json.RawMessage(providerConfig),
		}
	}
	override := testMachineProvisioning(t, "debian:trixie", 2, 4096)

	require.NoError(t, Definition{}.ValidateMachineProvisioning(
		policy(`{"api_base_url":"https://docker:2376"}`),
		testMachineProvisioning(t, testImage, 2, 4096),
	))
	require.Error(t, Definition{}.ValidateMachineProvisioning(
		policy(`{"api_base_url":"https://docker:2376"}`), override,
	))
	require.NoError(t, Definition{}.ValidateMachineProvisioning(
		policy(`{"api_base_url":"https://docker:2376","allowed_images":["`+testImage+`","debian:trixie"]}`),
		override,
	))
}
