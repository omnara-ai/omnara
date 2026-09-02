package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"golang.org/x/text/unicode/norm"
)

const resourceListCursorVersion = 1

var errInvalidListQuery = errors.New("invalid list query")

type resourceListCursor struct {
	Version   int    `json:"v"`
	ListKind  string `json:"list"`
	ScopeHash string `json:"scope"`
	QueryHash string `json:"query"`
	SortField string `json:"sort"`
	SortDesc  bool   `json:"desc"`
	IsNull    bool   `json:"null"`
	Key       string `json:"key"`
	ID        string `json:"id"`
}

type resourceListQueryInput struct {
	Name         *string
	Sort         *string
	Cursor       *string
	ListKind     string
	Scope        string
	IDKind       publicid.Kind
	AllowedSorts map[string]struct{}
	Extra        any
}

func parseResourceListQuery(input resourceListQueryInput) (listing.Options, error) {
	if input.ListKind == "" || input.Scope == "" || len(input.AllowedSorts) == 0 {
		return listing.Options{}, fmt.Errorf("%w: list definition is incomplete", errInvalidListQuery)
	}
	options := listing.Options{
		SortField: "created_at",
		SortDesc:  true,
	}
	if input.Name != nil {
		pattern, err := resourceNameGlobToLike(*input.Name)
		if err != nil {
			return listing.Options{}, err
		}
		options.NamePattern = pattern
	}
	if input.Sort != nil {
		sort := *input.Sort
		options.SortDesc = strings.HasPrefix(sort, "-")
		options.SortField = strings.TrimPrefix(sort, "-")
	}
	if _, ok := input.AllowedSorts[options.SortField]; !ok {
		return listing.Options{}, fmt.Errorf("%w: unsupported sort %q", errInvalidListQuery, options.SortField)
	}
	queryHash, err := hashResourceListQuery(options, input.Extra)
	if err != nil {
		return listing.Options{}, err
	}
	if input.Cursor == nil || *input.Cursor == "" {
		return options, nil
	}
	cursor, err := decodeResourceListCursor(*input.Cursor)
	if err != nil {
		return listing.Options{}, errMalformedCursor
	}
	if cursor.Version != resourceListCursorVersion || cursor.ListKind != input.ListKind ||
		cursor.ScopeHash != shortHash(input.Scope) || cursor.QueryHash != queryHash ||
		cursor.SortField != options.SortField || cursor.SortDesc != options.SortDesc {
		return listing.Options{}, errMalformedCursor
	}
	rawID, err := parsePublicID(input.IDKind, cursor.ID)
	if err != nil {
		return listing.Options{}, errMalformedCursor
	}
	options.After = listing.Cursor{
		Set: true, IsNull: cursor.IsNull, Key: cursor.Key, ID: rawID,
	}
	return options, nil
}

func encodeResourceListNextCursor(
	hasMore bool,
	after listing.Cursor,
	options listing.Options,
	listKind, scope string,
	idKind publicid.Kind,
	extra any,
) (*string, error) {
	if !hasMore || !after.Set || after.ID == storage.NilID {
		return nil, nil //nolint:nilnil // A nil cursor is the successful end-of-list representation.
	}
	id, err := publicID(idKind, after.ID)
	if err != nil {
		return nil, err
	}
	queryHash, err := hashResourceListQuery(options, extra)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(resourceListCursor{
		Version: resourceListCursorVersion, ListKind: listKind,
		ScopeHash: shortHash(scope), QueryHash: queryHash,
		SortField: options.SortField, SortDesc: options.SortDesc,
		IsNull: after.IsNull, Key: after.Key, ID: id,
	})
	if err != nil {
		return nil, err
	}
	if len(payload) > maxCursorPayloadLength {
		return nil, errors.New("resource list cursor payload is too large")
	}
	token := base64.RawURLEncoding.EncodeToString(payload)
	return &token, nil
}

func decodeResourceListCursor(raw string) (resourceListCursor, error) {
	if len(raw) == 0 || len(raw) > maxCursorTokenLength {
		return resourceListCursor{}, errInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(payload) > maxCursorPayloadLength {
		return resourceListCursor{}, errInvalidCursor
	}
	var cursor resourceListCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return resourceListCursor{}, errInvalidCursor
	}
	if cursor.Version <= 0 || cursor.ListKind == "" || cursor.ScopeHash == "" ||
		cursor.QueryHash == "" || cursor.SortField == "" || cursor.ID == "" {
		return resourceListCursor{}, errInvalidCursor
	}
	return cursor, nil
}

func resourceNameGlobToLike(glob string) (string, error) {
	if glob == "" || !utf8.ValidString(glob) {
		return "", fmt.Errorf("%w: name glob must contain between 1 and 200 characters", errInvalidListQuery)
	}
	glob = norm.NFC.String(glob)
	if utf8.RuneCountInString(glob) > 200 {
		return "", fmt.Errorf("%w: name glob must contain between 1 and 200 characters", errInvalidListQuery)
	}
	var out strings.Builder
	hasLiteral := false
	escaped := false
	for _, r := range glob {
		if escaped {
			if r != '*' && r != '?' && r != '\\' {
				return "", fmt.Errorf("%w: name glob has an invalid escape", errInvalidListQuery)
			}
			writeLikeLiteral(&out, r)
			hasLiteral = true
			escaped = false
			continue
		}
		switch r {
		case '\\':
			escaped = true
		case '*':
			out.WriteByte('%')
		case '?':
			out.WriteByte('_')
		default:
			writeLikeLiteral(&out, r)
			hasLiteral = true
		}
	}
	if escaped {
		return "", fmt.Errorf("%w: name glob has a trailing escape", errInvalidListQuery)
	}
	if !hasLiteral {
		return "", fmt.Errorf("%w: name glob must contain a literal character", errInvalidListQuery)
	}
	return out.String(), nil
}

func writeLikeLiteral(out *strings.Builder, r rune) {
	if r == '%' || r == '_' || r == '\\' {
		out.WriteByte('\\')
	}
	out.WriteRune(r)
}

func hashResourceListQuery(options listing.Options, extra any) (string, error) {
	payload, err := json.Marshal(struct {
		NamePattern string `json:"name"`
		SortField   string `json:"sort"`
		SortDesc    bool   `json:"desc"`
		Extra       any    `json:"extra,omitempty"`
	}{
		NamePattern: options.NamePattern, SortField: options.SortField,
		SortDesc: options.SortDesc, Extra: extra,
	})
	if err != nil {
		return "", err
	}
	return shortHash(string(payload)), nil
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

func sortSet(fields ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		out[field] = struct{}{}
	}
	return out
}

// defaultResourceSorts covers named resources exposing created and modified
// timestamps; lists with extra sortable columns declare their own set.
var defaultResourceSorts = sortSet("name", "created_at", "updated_at")

// optionalString adapts generated enum pointers (e.g. sort params) to the
// *string fields of resourceListQueryInput.
func optionalString[T ~string](value *T) *string {
	if value == nil {
		return nil
	}
	s := string(*value)
	return &s
}
