// Package skillsync materializes skill archives on a machine.
package skillsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/skills"
)

const (
	MaxArchiveBytes     = skills.MaxArchiveBytes
	downloadHTTPTimeout = 60 * time.Second
)

type Sender interface {
	SendSkillReport(ctx context.Context, report daemonprotocol.SkillReport) error
}

type Manager struct {
	store          localstore.MachineStore
	httpClient     *http.Client
	sender         Sender
	apiURL         string
	machineToken   string
	log            *slog.Logger
	installLocksMu sync.Mutex
	installLocks   map[skillRevision]*installLock
	active         atomic.Int64
}

type skillRevision struct {
	skillID    string
	revisionID string
}

type installLock struct {
	mu   sync.Mutex
	refs int
}

// NewManager wires the skill installer. The transport supplies connection
// behavior; skill downloads own their timeout and redirect policy.
func NewManager(
	store localstore.MachineStore,
	sender Sender,
	httpTransport http.RoundTripper,
	apiURL, machineToken string,
	log *slog.Logger,
) *Manager {
	if httpTransport == nil {
		httpTransport = http.DefaultTransport
	}
	httpClient := outboundhttp.CloneWithoutRedirects(&http.Client{
		Transport: httpTransport,
		Timeout:   downloadHTTPTimeout,
	})
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		store:        store,
		httpClient:   httpClient,
		sender:       sender,
		apiURL:       strings.TrimRight(apiURL, "/"),
		machineToken: machineToken,
		log:          log,
		installLocks: make(map[skillRevision]*installLock),
	}
}

func (m *Manager) HandleOffer(ctx context.Context, offer daemonprotocol.SkillOffer) {
	if offer.SkillID == "" || offer.RevisionID == "" || offer.Digest == "" ||
		offer.DownloadToken == "" || offer.DownloadExpiresAt == 0 {
		m.log.Warn(
			"skill_offer missing required fields",
			"skill_id", offer.SkillID,
			"have_revision_id", offer.RevisionID != "",
			"have_digest", offer.Digest != "",
			"have_download_capability", offer.DownloadToken != "" && offer.DownloadExpiresAt != 0,
		)
		return
	}
	m.active.Add(1)
	defer m.active.Add(-1)
	report := m.install(ctx, offer)
	if err := m.sender.SendSkillReport(ctx, report); err != nil {
		m.log.Warn("send skill_report failed", "skill_id", offer.SkillID, "err", err)
	}
}

func (m *Manager) Idle() bool {
	return m.active.Load() == 0
}

func (m *Manager) install(ctx context.Context, offer daemonprotocol.SkillOffer) daemonprotocol.SkillReport {
	unlock := m.lockInstall(offer.SkillID, offer.RevisionID)
	defer unlock()

	dir, err := m.store.SkillDir(offer.SkillID, offer.RevisionID)
	if err != nil {
		return failReport(offer.RequestID, offer.SkillID, "invalid_skill_path", err)
	}
	if installedDigestMatches(dir, offer.Digest) {
		return daemonprotocol.SkillReport{
			RequestID: offer.RequestID,
			SkillID:   offer.SkillID,
			State:     daemonprotocol.SkillStateReady,
		}
	}
	archive, err := m.download(ctx, offer)
	if err != nil {
		return failReport(offer.RequestID, offer.SkillID, "download_failed", err)
	}
	if err := skills.VerifyDigest(archive, offer.Digest); err != nil {
		return failReport(offer.RequestID, offer.SkillID, "digest_mismatch", err)
	}
	format, ok := skills.DetectFormat("", archive)
	if !ok {
		return failReport(
			offer.RequestID,
			offer.SkillID,
			"unknown_archive_format",
			errors.New("could not detect archive format from downloaded bytes"),
		)
	}
	stagingDir := dir + ".staging"
	_ = os.RemoveAll(stagingDir)
	if err := skills.ExtractInto(format, archive, stagingDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return failReport(offer.RequestID, offer.SkillID, "extract_failed", err)
	}
	// Drop the marker before touching the install dir so an interrupted swap
	// forces a re-download instead of claiming stale content is current.
	_ = os.Remove(installedDigestPath(dir))
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		_ = os.RemoveAll(stagingDir)
		return failReport(offer.RequestID, offer.SkillID, "mkdir_failed", err)
	}
	if err := os.Rename(stagingDir, dir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return failReport(offer.RequestID, offer.SkillID, "install_rename_failed", err)
	}
	if err := os.WriteFile(installedDigestPath(dir), []byte(offer.Digest), 0o644); err != nil {
		// Non-fatal: the install is complete; the next offer just re-downloads.
		m.log.Warn("write skill digest marker failed", "skill_id", offer.SkillID, "err", err)
	}
	return daemonprotocol.SkillReport{
		RequestID: offer.RequestID,
		SkillID:   offer.SkillID,
		State:     daemonprotocol.SkillStateReady,
	}
}

// lockInstall serializes materialization of one skill revision. Offers for
// different revisions remain independent, and unused locks are removed once
// all current waiters finish.
func (m *Manager) lockInstall(skillID, revisionID string) func() {
	key := skillRevision{skillID: skillID, revisionID: revisionID}
	m.installLocksMu.Lock()
	lock := m.installLocks[key]
	if lock == nil {
		lock = &installLock{}
		m.installLocks[key] = lock
	}
	lock.refs++
	m.installLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		m.installLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(m.installLocks, key)
		}
		m.installLocksMu.Unlock()
	}
}

// installedDigestPath is the sidecar file recording the digest of the archive
// currently extracted at dir, e.g.
// {skillsDir}/{skillID}/revisions/{revisionID}.digest.
func installedDigestPath(dir string) string {
	return dir + ".digest"
}

func installedDigestMatches(dir, digest string) bool {
	if digest == "" {
		return false
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false
	}
	recorded, err := os.ReadFile(installedDigestPath(dir))
	if err != nil {
		return false
	}
	return string(recorded) == digest
}

func (m *Manager) download(ctx context.Context, offer daemonprotocol.SkillOffer) ([]byte, error) {
	downloadURL := m.apiURL + "/daemon/skills/" + url.PathEscape(offer.SkillID) +
		"/archive?revision_id=" + url.QueryEscape(offer.RevisionID) +
		"&expires_at=" + strconv.FormatInt(offer.DownloadExpiresAt, 10) +
		"&download_token=" + url.QueryEscape(offer.DownloadToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.machineToken)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail != "" {
			return nil, fmt.Errorf("skill download status %d: %s", resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("skill download status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read download body: %w", err)
	}
	if len(body) > MaxArchiveBytes {
		return nil, fmt.Errorf("download body exceeds %d bytes", MaxArchiveBytes)
	}
	return body, nil
}

func failReport(requestID, skillID, code string, err error) daemonprotocol.SkillReport {
	if err == nil {
		err = errors.New(code)
	}
	return daemonprotocol.SkillReport{
		RequestID: requestID,
		SkillID:   skillID,
		State:     daemonprotocol.SkillStateFailed,
		ErrorCode: code,
		Error:     err.Error(),
	}
}
