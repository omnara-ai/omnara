package mcpregistry

import (
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var ErrInvalidCursor = errors.New("invalid cursor")

const (
	nameWeight        = 1.0
	titleWeight       = 5.0
	descriptionWeight = 2.0
	remoteURLWeight   = 1.0
	urlMatchBonus     = 10.0
)

type Registry struct {
	snapshot Snapshot
}

func NewRegistry(snapshot Snapshot) (*Registry, error) {
	if len(snapshot.Servers) == 0 {
		return nil, errEmptySnapshot
	}
	if snapshot.RemoteURLIndex == nil {
		snapshot.RemoteURLIndex = map[string][]int{}
	}
	for url, positions := range snapshot.RemoteURLIndex {
		for _, position := range positions {
			if position < 0 || position >= len(snapshot.Servers) {
				return nil, fmt.Errorf("registry snapshot remote url index for %q points outside servers", url)
			}
		}
	}
	return &Registry{snapshot: snapshot}, nil
}

func (r *Registry) Len() int {
	return len(r.snapshot.Servers)
}

func (r *Registry) GeneratedAt() time.Time {
	return r.snapshot.GeneratedAt
}

type hit struct {
	position int
	score    float64
}

func (r *Registry) Search(params SearchParams) (SearchPage, error) {
	limit := normalizeLimit(params.Limit)
	offset, err := decodeCursor(params.Cursor)
	if err != nil {
		return SearchPage{}, err
	}
	terms := queryTerms(params.Query)
	hits := r.collect(terms, urlNeedleFromQuery(params.Query), normalizeRemoteURL(params.RemoteURL))
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].score != hits[b].score {
			return hits[a].score > hits[b].score
		}
		return r.snapshot.Servers[hits[a].position].Server.Name < r.snapshot.Servers[hits[b].position].Server.Name
	})
	if offset > len(hits) {
		offset = len(hits)
	}
	end := min(offset+limit, len(hits))
	page := SearchPage{Servers: make([]Server, 0, end-offset)}
	for _, h := range hits[offset:end] {
		page.Servers = append(page.Servers, r.snapshot.Servers[h.position].Server)
	}
	if len(hits) > end {
		next := encodeCursor(end)
		page.NextCursor = &next
	}
	return page, nil
}

func (r *Registry) collect(terms []string, urlNeedle, remoteFilter string) []hit {
	var candidates []int
	if remoteFilter != "" {
		candidates = r.snapshot.RemoteURLIndex[remoteFilter]
	}
	hits := make([]hit, 0, 64)
	consider := func(position int) {
		fields := &r.snapshot.Servers[position].Search
		score, ok := scoreServer(fields, terms, urlNeedle)
		if ok {
			hits = append(hits, hit{position: position, score: score})
		}
	}
	if remoteFilter != "" {
		for _, position := range candidates {
			consider(position)
		}
		return hits
	}
	for position := range r.snapshot.Servers {
		consider(position)
	}
	return hits
}

func scoreServer(fields *SearchFields, terms []string, urlNeedle string) (float64, bool) {
	if len(terms) == 0 {
		return 0, true
	}
	urlMatched := urlNeedle != "" && strings.Contains(fields.RemoteURLs, urlNeedle)
	if !urlMatched {
		for _, term := range terms {
			if !hasTokenPrefix(fields.Name, term) &&
				!hasTokenPrefix(fields.Title, term) &&
				!hasTokenPrefix(fields.Description, term) &&
				!hasTokenPrefix(fields.RemoteURLs, term) {
				return 0, false
			}
		}
	}
	score := 0.0
	for _, term := range terms {
		if hasTokenPrefix(fields.Name, term) {
			score += nameWeight
		}
		if hasTokenPrefix(fields.Title, term) {
			score += titleWeight
		}
		if hasTokenPrefix(fields.Description, term) {
			score += descriptionWeight
		}
		if hasTokenPrefix(fields.RemoteURLs, term) {
			score += remoteURLWeight
		}
	}
	if urlMatched {
		score += urlMatchBonus
	}
	return score, true
}

func hasTokenPrefix(text, term string) bool {
	start := 0
	for {
		index := strings.Index(text[start:], term)
		if index < 0 {
			return false
		}
		absolute := start + index
		if absolute == 0 || !isTokenRune(rune(text[absolute-1])) {
			return true
		}
		start = absolute + 1
		if start >= len(text) {
			return false
		}
	}
}

func isTokenRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func urlNeedleFromQuery(query string) string {
	needle := normalizeRemoteURL(query)
	if !strings.ContainsAny(needle, "./") {
		return ""
	}
	return needle
}

func queryTerms(query string) []string {
	return strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !isTokenRune(r)
	})
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		return MaxSearchLimit
	}
	return limit
}

func encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func decodeCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, ErrInvalidCursor
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil || offset < 0 {
		return 0, ErrInvalidCursor
	}
	return offset, nil
}
