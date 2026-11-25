package memorystore

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"strings"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
)

// VisitContents reads matching regular files through the store's confined root.
// The visitor receives logical paths and bounded contents, so a search process
// can use stdin without receiving access to the memory filesystem. The root is
// borrowed from VisitSearchStores and must remain open throughout this call.
func (s SearchStore) VisitContents(
	ctx context.Context, pattern string, matcher *regexp.Regexp,
	visit func(path string, content []byte) error,
) error {
	remaining := memoryListingTraversalLimit
	view := storeFS{
		globFS: globFS{root: s.Root, checkCanceled: ctx.Err, remaining: &remaining},
		stores: []dbsqlc.ListAttachedMemoryStoresRow{{Name: s.Name}},
		opened: s.Name,
	}
	literal := !strings.ContainsAny(pattern, "*?")
	return globFiles(ctx, &view, memoryGlobPattern(pattern), func(name string, item fs.DirEntry) error {
		full := "/" + name
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
		relative := strings.TrimPrefix(name, "memory/"+s.Name+"/")
		content, err := memoryops.Read(s.Root, relative)
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
