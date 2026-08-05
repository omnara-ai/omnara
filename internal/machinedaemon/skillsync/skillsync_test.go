package skillsync

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

func buildSkillArchive(t *testing.T, body string) ([]byte, string) {
	t.Helper()
	skillMd := "---\nname: canary\ndescription: test skill\n---\n" + body
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := []struct {
		name    string
		content string
	}{
		{"canary/SKILL.md", skillMd},
		{"canary/payload.txt", body},
	}
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: f.name,
			Mode: 0o644,
			Size: int64(len(f.content)),
		}); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(f.content)); err != nil {
			t.Fatalf("write tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), "sha256:" + hex.EncodeToString(sum[:])
}

const testMachineToken = "omnara_mdt_test_token"

type blockingSender struct {
	reports chan daemonprotocol.SkillReport
	release chan struct{}
}

func (s blockingSender) SendSkillReport(ctx context.Context, report daemonprotocol.SkillReport) error {
	select {
	case s.reports <- report:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestManagerOwnsDownloadTimeoutAndUsesInjectedTransport(t *testing.T) {
	transport := &http.Transport{}
	manager := NewManager(
		localstore.MachineStore{}, nil, transport, "https://api.example.com", testMachineToken, nil,
	)
	if manager.httpClient.Timeout != downloadHTTPTimeout {
		t.Fatalf("download timeout = %s, want %s", manager.httpClient.Timeout, downloadHTTPTimeout)
	}
	if manager.httpClient.Transport != transport {
		t.Fatal("manager did not retain the injected transport")
	}
}

func newTestManager(t *testing.T, archive []byte, downloads *atomic.Int64) (*Manager, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testMachineToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	store, err := localstore.Machine(t.TempDir(), "inst-1", "mach-1")
	if err != nil {
		t.Fatalf("create machine store: %v", err)
	}
	m := NewManager(store, nil, server.Client().Transport, server.URL, testMachineToken, nil)
	dir, err := store.SkillDir("skl_test", "skr_test")
	if err != nil {
		t.Fatalf("skill dir: %v", err)
	}
	return m, dir
}

func TestInstallDownloadsExtractsAndRecordsDigest(t *testing.T) {
	archive, digest := buildSkillArchive(t, "v1")
	var downloads atomic.Int64
	m, dir := newTestManager(t, archive, &downloads)

	report := m.install(context.Background(), daemonprotocol.SkillOffer{
		RequestID:  "r1",
		SkillID:    "skl_test",
		RevisionID: "skr_test",
		Digest:     digest,
	})
	if report.State != daemonprotocol.SkillStateReady {
		t.Fatalf("install failed: %+v", report)
	}
	payload, err := os.ReadFile(filepath.Join(dir, "payload.txt"))
	if err != nil || string(payload) != "v1" {
		t.Fatalf("payload = %q, err = %v", payload, err)
	}
	marker, err := os.ReadFile(dir + ".digest")
	if err != nil || string(marker) != digest {
		t.Fatalf("digest marker = %q, err = %v", marker, err)
	}
	if downloads.Load() != 1 {
		t.Fatalf("downloads = %d, want 1", downloads.Load())
	}
}

func TestManagerIsNotIdleUntilInstallReportIsSent(t *testing.T) {
	archive, digest := buildSkillArchive(t, "v1")
	downloadStarted := make(chan struct{}, 1)
	downloadRelease := make(chan struct{})
	releaseDownload := sync.OnceFunc(func() { close(downloadRelease) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloadStarted <- struct{}{}
		<-downloadRelease
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(releaseDownload)
	store, err := localstore.Machine(t.TempDir(), "inst-1", "mach-1")
	if err != nil {
		t.Fatalf("create machine store: %v", err)
	}
	reportRelease := make(chan struct{})
	releaseReport := sync.OnceFunc(func() { close(reportRelease) })
	t.Cleanup(releaseReport)
	sender := blockingSender{reports: make(chan daemonprotocol.SkillReport, 1), release: reportRelease}
	m := NewManager(store, sender, server.Client().Transport, server.URL, testMachineToken, nil)
	if !m.Idle() {
		t.Fatal("new manager must be idle")
	}
	go m.HandleOffer(context.Background(), daemonprotocol.SkillOffer{
		RequestID:         "r1",
		SkillID:           "skl_test",
		RevisionID:        "skr_test",
		Digest:            digest,
		DownloadToken:     "download-token",
		DownloadExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
	select {
	case <-downloadStarted:
	case <-time.After(time.Second):
		t.Fatal("skill download did not start")
	}
	if m.Idle() {
		t.Fatal("manager must not be idle during installation")
	}
	releaseDownload()
	select {
	case report := <-sender.reports:
		if report.State != daemonprotocol.SkillStateReady {
			t.Fatalf("skill report = %+v, want ready", report)
		}
	case <-time.After(time.Second):
		t.Fatal("skill report was not sent")
	}
	if m.Idle() {
		t.Fatal("manager must not be idle while sending the install report")
	}
	releaseReport()
	deadline := time.Now().Add(time.Second)
	for !m.Idle() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !m.Idle() {
		t.Fatal("manager did not become idle after sending the install report")
	}
}

func TestDownloadStatusErrorIncludesResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid token", http.StatusForbidden)
	}))
	defer server.Close()
	store, err := localstore.Machine(t.TempDir(), "inst-1", "mach-1")
	if err != nil {
		t.Fatalf("create machine store: %v", err)
	}
	m := NewManager(store, nil, server.Client().Transport, server.URL, testMachineToken, nil)
	_, err = m.download(context.Background(), daemonprotocol.SkillOffer{SkillID: "skl_test", RevisionID: "skr_test"})
	if err == nil || !strings.Contains(err.Error(), "invalid token") {
		t.Fatalf("download error = %v, want response body detail", err)
	}
}

func TestDownloadUsesOfferCapabilityAndDaemonToken(t *testing.T) {
	requests := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		_, _ = w.Write([]byte("archive"))
	}))
	defer server.Close()
	store, err := localstore.Machine(t.TempDir(), "inst-1", "mach-1")
	if err != nil {
		t.Fatalf("create machine store: %v", err)
	}
	m := NewManager(store, nil, server.Client().Transport, server.URL, testMachineToken, nil)
	_, err = m.download(context.Background(), daemonprotocol.SkillOffer{
		SkillID:           "skl_test",
		RevisionID:        "skr_test",
		DownloadToken:     "download-capability",
		DownloadExpiresAt: 12345,
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	req := <-requests
	if got := req.Header.Get("Authorization"); got != "Bearer "+testMachineToken {
		t.Fatalf("authorization = %q", got)
	}
	query := req.URL.Query()
	if query.Get("revision_id") != "skr_test" ||
		query.Get("expires_at") != "12345" ||
		query.Get("download_token") != "download-capability" {
		t.Fatalf("download query = %v", query)
	}
}

func TestDownloadDoesNotFollowRedirects(t *testing.T) {
	var redirectedRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	store, err := localstore.Machine(t.TempDir(), "inst-1", "mach-1")
	if err != nil {
		t.Fatalf("create machine store: %v", err)
	}
	m := NewManager(store, nil, source.Client().Transport, source.URL, testMachineToken, nil)

	_, err = m.download(context.Background(), daemonprotocol.SkillOffer{
		SkillID:    "skl_test",
		RevisionID: "skr_test",
	})
	if err == nil {
		t.Fatal("expected redirect response to fail")
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf("redirected requests = %d, want 0", redirectedRequests.Load())
	}
}

func TestInstallSkipsDownloadWhenDigestUnchanged(t *testing.T) {
	archive, digest := buildSkillArchive(t, "v1")
	var downloads atomic.Int64
	m, _ := newTestManager(t, archive, &downloads)

	offer := daemonprotocol.SkillOffer{
		RequestID:  "r1",
		SkillID:    "skl_test",
		RevisionID: "skr_test",
		Digest:     digest,
	}
	for _, requestID := range []string{"r1", "r2"} {
		offer.RequestID = requestID
		if report := m.install(context.Background(), offer); report.State != daemonprotocol.SkillStateReady {
			t.Fatalf("install %s failed: %+v", requestID, report)
		}
	}
	if downloads.Load() != 1 {
		t.Fatalf("downloads = %d, want 1 (second offer must skip)", downloads.Load())
	}
}

func TestInstallSerializesConcurrentOffersForSameRevision(t *testing.T) {
	archive, digest := buildSkillArchive(t, "v1")
	var downloads atomic.Int64
	requestStarted := make(chan struct{}, 2)
	releaseDownload := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		requestStarted <- struct{}{}
		<-releaseDownload
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	store, err := localstore.Machine(t.TempDir(), "inst-1", "mach-1")
	if err != nil {
		t.Fatalf("create machine store: %v", err)
	}
	m := NewManager(store, nil, server.Client().Transport, server.URL, testMachineToken, nil)
	offer := daemonprotocol.SkillOffer{
		SkillID:    "skl_test",
		RevisionID: "skr_test",
		Digest:     digest,
	}

	const concurrentInstalls = 8
	start := make(chan struct{})
	reports := make(chan daemonprotocol.SkillReport, concurrentInstalls)
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(concurrentInstalls)
	done.Add(concurrentInstalls)
	for i := range concurrentInstalls {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			current := offer
			current.RequestID = fmt.Sprintf("r%d", i)
			reports <- m.install(context.Background(), current)
		}()
	}
	ready.Wait()
	close(start)

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		close(releaseDownload)
		t.Fatal("first download did not start")
	}
	select {
	case <-requestStarted:
		close(releaseDownload)
		t.Fatal("a duplicate download started before the first install completed")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseDownload)
	done.Wait()
	close(reports)

	for report := range reports {
		if report.State != daemonprotocol.SkillStateReady {
			t.Fatalf("concurrent install failed: %+v", report)
		}
	}
	if downloads.Load() != 1 {
		t.Fatalf("downloads = %d, want 1", downloads.Load())
	}
}

func TestInstallRevisionChangeUsesNewPathAndRedownloads(t *testing.T) {
	archiveV1, digestV1 := buildSkillArchive(t, "v1")
	archiveV2, digestV2 := buildSkillArchive(t, "v2")

	var downloads atomic.Int64
	current := &atomic.Pointer[[]byte]{}
	current.Store(&archiveV1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		_, _ = w.Write(*current.Load())
	}))
	defer server.Close()

	store, err := localstore.Machine(t.TempDir(), "inst-1", "mach-1")
	if err != nil {
		t.Fatalf("create machine store: %v", err)
	}
	m := NewManager(store, nil, server.Client().Transport, server.URL, testMachineToken, nil)
	dirV1, err := store.SkillDir("skl_test", "skr_v1")
	if err != nil {
		t.Fatalf("skill v1 dir: %v", err)
	}
	dirV2, err := store.SkillDir("skl_test", "skr_v2")
	if err != nil {
		t.Fatalf("skill v2 dir: %v", err)
	}

	offer := daemonprotocol.SkillOffer{SkillID: "skl_test", RevisionID: "skr_v1"}
	offer.RequestID, offer.Digest = "r1", digestV1
	if report := m.install(context.Background(), offer); report.State != daemonprotocol.SkillStateReady {
		t.Fatalf("install v1 failed: %+v", report)
	}
	current.Store(&archiveV2)
	offer.RequestID, offer.Digest, offer.RevisionID = "r2", digestV2, "skr_v2"
	if report := m.install(context.Background(), offer); report.State != daemonprotocol.SkillStateReady {
		t.Fatalf("install v2 failed: %+v", report)
	}
	payload, err := os.ReadFile(filepath.Join(dirV2, "payload.txt"))
	if err != nil || string(payload) != "v2" {
		t.Fatalf("payload = %q, err = %v (want v2)", payload, err)
	}
	marker, _ := os.ReadFile(dirV2 + ".digest")
	if string(marker) != digestV2 {
		t.Fatalf("digest marker = %q, want %q", marker, digestV2)
	}
	if downloads.Load() != 2 {
		t.Fatalf("downloads = %d, want 2", downloads.Load())
	}
	v1Payload, err := os.ReadFile(filepath.Join(dirV1, "payload.txt"))
	if err != nil || string(v1Payload) != "v1" {
		t.Fatalf("v1 payload = %q, err = %v", v1Payload, err)
	}
}

func TestInstallRevisionChangeRedownloadsEvenWhenDigestMatches(t *testing.T) {
	archive, digest := buildSkillArchive(t, "same content")
	var downloads atomic.Int64
	m, _ := newTestManager(t, archive, &downloads)

	for i, revisionID := range []string{"skr_v1", "skr_v2"} {
		report := m.install(context.Background(), daemonprotocol.SkillOffer{
			RequestID:  fmt.Sprintf("r%d", i+1),
			SkillID:    "skl_test",
			RevisionID: revisionID,
			Digest:     digest,
		})
		if report.State != daemonprotocol.SkillStateReady {
			t.Fatalf("install %s failed: %+v", revisionID, report)
		}
	}
	if downloads.Load() != 2 {
		t.Fatalf("downloads = %d, want 2 for distinct revisions", downloads.Load())
	}
}

func TestInstallDigestMismatchFailsAndLeavesNoMarker(t *testing.T) {
	archive, _ := buildSkillArchive(t, "v1")
	var downloads atomic.Int64
	m, dir := newTestManager(t, archive, &downloads)

	report := m.install(context.Background(), daemonprotocol.SkillOffer{
		RequestID:  "r1",
		SkillID:    "skl_test",
		RevisionID: "skr_test",
		Digest:     "sha256:" + hex.EncodeToString(make([]byte, 32)),
	})
	if report.State != daemonprotocol.SkillStateFailed || report.ErrorCode != "digest_mismatch" {
		t.Fatalf("report = %+v, want digest_mismatch failure", report)
	}
	if _, err := os.Stat(dir + ".digest"); !os.IsNotExist(err) {
		t.Fatalf("digest marker should not exist after failed install")
	}
}
