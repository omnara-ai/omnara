package docker

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/stretchr/testify/require"
)

type testCertificates struct {
	caPEM      string
	clientCert string
	clientKey  string
	pool       *x509.CertPool
}

func newTestCertificates(t *testing.T) testCertificates {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "docker test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	clientDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "omnara"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, caCert, &clientKey.PublicKey, caKey)
	require.NoError(t, err)
	clientKeyDER, err := x509.MarshalPKCS8PrivateKey(clientKey)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return testCertificates{
		caPEM:      string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		clientCert: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER})),
		clientKey:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: clientKeyDER})),
		pool:       pool,
	}
}

func credentialJSON(t *testing.T, ca, cert, key string) string {
	t.Helper()
	raw, err := json.Marshal(providerCredential{CA: ca, Cert: cert, Key: key})
	require.NoError(t, err)
	return string(raw)
}

func TestDockerClientPresentsCertificateOnlyToListedPrivateHosts(t *testing.T) {
	clientCerts := newTestCertificates(t)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+apiVersion+"/_ping" || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("OK"))
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientCerts.pool}
	server.StartTLS()
	defer server.Close()
	serverCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	credential := credentialJSON(t, serverCA, clientCerts.clientCert, clientCerts.clientKey)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)
	listed, err := ssrf.ParseAllowlist([]string{"127.0.0.1:" + port})
	require.NoError(t, err)
	config := json.RawMessage(`{"api_base_url":"` + server.URL + `"}`)

	for _, allowlist := range []*ssrf.Allowlist{listed, nil} {
		machineProvider, err := newProvider(config, providers.RuntimeConfig{
			ProviderAuthToken: credential,
			EndpointAllowlist: allowlist,
		})
		require.NoError(t, err)
		err = machineProvider.CheckConnection(context.Background(), testMachineProvisioning(t, testImage, 1, 1024))
		if allowlist != nil {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ssrf.ErrBlockedAddress)
		}
	}
}

func TestDockerPullReportsErrorsInsideTheProgressStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+apiVersion+"/images/create" || r.URL.Query().Get("fromImage") != testImage {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"status":"Pulling from library/buildpack-deps"}` + "\n" +
			`{"error":"unexpected EOF","errorDetail":{"message":"unexpected EOF"}}` + "\n"))
	}))
	defer server.Close()
	client := &restClient{baseURL: server.URL + "/" + apiVersion, httpClient: server.Client()}
	require.ErrorContains(t, client.PullImage(context.Background(), testImage), "unexpected EOF")
}

func TestDockerPullRejectsOversizedProgressRecords(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"` + strings.Repeat("a", maxPullRecordBytes) + `"}` + "\r\n"))
	}))
	defer server.Close()
	client := &restClient{baseURL: server.URL + "/" + apiVersion, httpClient: server.Client()}
	require.ErrorIs(t, client.PullImage(context.Background(), testImage), bufio.ErrTooLong)
}

func TestDockerPullDefaultsUntaggedImagesToLatest(t *testing.T) {
	var tags []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tags = append(tags, r.URL.Query().Get("tag"))
	}))
	defer server.Close()
	client := &restClient{baseURL: server.URL + "/" + apiVersion, httpClient: server.Client()}
	digest := "ubuntu@sha256:" + strings.Repeat("a", 64)
	for _, image := range []string{"ubuntu", "localhost:5000/team/agent", "ubuntu:24.04", digest} {
		require.NoError(t, client.PullImage(context.Background(), image))
	}
	require.Equal(t, []string{"latest", "latest", "", ""}, tags)
}
