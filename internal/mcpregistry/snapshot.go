package mcpregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const snapshotMaxBytes = 256 * 1024 * 1024

type Snapshot struct {
	GeneratedAt    time.Time        `json:"generated_at"`
	Servers        []SnapshotServer `json:"servers"`
	RemoteURLIndex map[string][]int `json:"remote_url_index"`
}

type SnapshotServer struct {
	Server Server       `json:"server"`
	Search SearchFields `json:"search"`
}

type SearchFields struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	RemoteURLs  string `json:"remote_urls"`
}

func BuildSnapshot(servers []Server, generatedAt time.Time) Snapshot {
	snapshot := Snapshot{
		GeneratedAt:    generatedAt.UTC(),
		Servers:        make([]SnapshotServer, 0, len(servers)),
		RemoteURLIndex: map[string][]int{},
	}
	for _, server := range servers {
		if server.Remotes == nil {
			server.Remotes = []Remote{}
		}
		if server.Icons == nil {
			server.Icons = []Icon{}
		}
		position := len(snapshot.Servers)
		remoteURLs := make([]string, 0, len(server.Remotes))
		for _, remote := range server.Remotes {
			normalized := normalizeRemoteURL(remote.URL)
			if normalized == "" {
				continue
			}
			remoteURLs = append(remoteURLs, normalized)
			snapshot.RemoteURLIndex[normalized] = append(snapshot.RemoteURLIndex[normalized], position)
		}
		snapshot.Servers = append(snapshot.Servers, SnapshotServer{
			Server: server,
			Search: SearchFields{
				Name:        strings.ToLower(server.Name),
				Title:       strings.ToLower(server.Title),
				Description: strings.ToLower(server.Description),
				RemoteURLs:  strings.Join(remoteURLs, " "),
			},
		})
	}
	return snapshot
}

func WriteSnapshot(path string, snapshot Snapshot) error {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode registry snapshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("prepare registry snapshot directory: %w", err)
	}
	staging := path + ".staging"
	if err := os.WriteFile(staging, encoded, 0o644); err != nil {
		return fmt.Errorf("write registry snapshot: %w", err)
	}
	if err := os.Rename(staging, path); err != nil {
		_ = os.Remove(staging)
		return fmt.Errorf("publish registry snapshot: %w", err)
	}
	return nil
}

func LoadSnapshot(path string) (*Registry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat registry snapshot: %w", err)
	}
	if info.Size() > snapshotMaxBytes {
		return nil, fmt.Errorf("registry snapshot %s exceeds %d bytes", path, snapshotMaxBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read registry snapshot: %w", err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, fmt.Errorf("decode registry snapshot: %w", err)
	}
	return NewRegistry(snapshot)
}

var remoteURLScheme = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)

func normalizeRemoteURL(raw string) string {
	trimmed := remoteURLScheme.ReplaceAllString(strings.TrimSpace(raw), "")
	return strings.ToLower(strings.TrimRight(trimmed, "/"))
}

var errEmptySnapshot = errors.New("registry snapshot contains no servers")
