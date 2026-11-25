package memorystore

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
)

// VisitContents borrows the confined root from VisitSearchStores without closing it.
// Windows memory search passes these bounded file contents to the search process over stdin.
func (s SearchStore) VisitContents(
	ctx context.Context, pattern string, matcher *regexp.Regexp,
	visit func(path string, content []byte) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, rest, _ := strings.Cut(memoryGlobPattern(pattern), "/")
	storePattern, relativePattern, _ := strings.Cut(rest, "/")
	matches, err := doublestar.Match(storePattern, s.Name)
	if err != nil || !matches {
		return err
	}
	if storePattern == "**" {
		relativePattern = "**"
	}
	if relativePattern == "" {
		return nil
	}
	remaining := memoryListingTraversalLimit
	view := globFS{root: s.Root, checkCanceled: ctx.Err, remaining: &remaining}
	literal := !strings.ContainsAny(pattern, "*?")
	return globFiles(ctx, view, relativePattern, func(name string, item fs.DirEntry) error {
		full := Root + "/" + s.Name + "/" + name
		if item.IsDir() || !matcher.MatchString(full) {
			return nil
		}
		info, err := item.Info()
		if errors.Is(err, fs.ErrNotExist) && !literal {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Size() > daemonprotocol.MaxFileTransferBytes {
			return nil
		}
		content, err := memoryops.Read(s.Root, name)
		if errors.Is(err, fs.ErrNotExist) && !literal {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return visit(full, content)
	})
}
