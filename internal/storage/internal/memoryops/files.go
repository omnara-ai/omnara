package memoryops

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/skills"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Filesystem struct{ root *os.Root }

type StoreRef struct {
	Name    string
	path    string
	staging string
}

func NewStoreRef(orgID, projectID uuid.UUID, name string) (StoreRef, error) {
	if err := skills.ValidateName(name); err != nil {
		return StoreRef{}, err
	}
	org, err := publicid.Encode(publicid.KindOrganization, orgID)
	if err != nil {
		return StoreRef{}, err
	}
	project, err := publicid.Encode(publicid.KindProject, projectID)
	if err != nil {
		return StoreRef{}, err
	}
	storePath := org + "/" + project + "/" + name
	return StoreRef{Name: name, path: storePath, staging: ".staging/" + storePath}, nil
}

func OpenFilesystem(dir string) (*Filesystem, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("memory directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create memory directory: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open memory directory: %w", err)
	}
	files := &Filesystem{root: root}
	for _, name := range []string{".staging", ".locks"} {
		if err = root.MkdirAll(name, 0700); err == nil {
			err = CheckPath(root, name)
		}
		if err != nil {
			_ = root.Close()
			return nil, fmt.Errorf("prepare memory directory: %w", err)
		}
		probe := name + "/.probe-" + uuid.NewString()
		file, probeErr := root.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if probeErr == nil {
			_, probeErr = tryLock(file)
			if probeErr == nil {
				probeErr = file.Sync()
			}
			probeErr = errors.Join(probeErr, file.Close(), root.Remove(probe))
		}
		if probeErr == nil {
			probeErr = syncParents(root, name)
		}
		if probeErr != nil {
			_ = root.Close()
			return nil, fmt.Errorf("check memory directory: %w", probeErr)
		}
	}
	return files, nil
}

func (f *Filesystem) Close() error { return f.root.Close() }

func (f *Filesystem) OpenStore(ref StoreRef) (*os.Root, error) {
	if f == nil {
		return nil, errors.New("memory filesystem is not configured")
	}
	root, err := f.root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if err := CheckPath(root, ref.path); err != nil {
		return nil, err
	}
	return root.OpenRoot(ref.path)
}

func CheckPath(root *os.Root, name string) error {
	parts := strings.Split(name, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("unsupported memory file type: %w", storeerr.ErrConflict)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("memory parent is a file: %w: %w", storeerr.ErrConflict, syscall.ENOTDIR)
		}
	}
	return nil
}

func (f *Filesystem) Lock(ctx context.Context, ref StoreRef) (*os.File, error) {
	if f == nil {
		return nil, errors.New("memory filesystem is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := ".locks/" + ref.path
	if err := f.root.MkdirAll(path.Dir(name), 0700); err != nil {
		return nil, err
	}
	if err := CheckPath(f.root, path.Dir(name)); err != nil {
		return nil, err
	}
	if err := CheckPath(f.root, name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	file, err := f.root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open memory lock: %w", err)
	}
	for {
		locked, err := tryLock(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("lock memory store: %w", err)
		}
		if locked {
			return file, nil
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func openRegularFile(root *os.Root, name string) (*os.File, error) {
	if err := CheckPath(root, name); err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("memory path is not a regular file: %w", storeerr.ErrConflict)
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func Read(root *os.Root, name string) ([]byte, error) {
	file, err := openRegularFile(root, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	body, err := io.ReadAll(io.LimitReader(file, daemonprotocol.MaxFileTransferBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > daemonprotocol.MaxFileTransferBytes {
		return nil, errors.New("memory file exceeds transfer limit")
	}
	return body, nil
}

func Digest(root *os.Root, name string) (string, error) {
	file, err := openRegularFile(root, name)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, daemonprotocol.MaxFileTransferBytes+1))
	if err != nil {
		return "", err
	}
	if size > daemonprotocol.MaxFileTransferBytes {
		return "", errors.New("memory file exceeds transfer limit")
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}

func (f *Filesystem) Stage(ref StoreRef, content []byte) (string, error) {
	if f == nil {
		return "", errors.New("memory filesystem is not configured")
	}
	dir := ref.staging
	if err := f.root.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := CheckPath(f.root, dir); err != nil {
		return "", err
	}
	name := dir + "/" + uuid.NewString()
	file, err := f.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, err = file.Write(content)
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return "", errors.Join(err, f.Discard(name))
	}
	return name, nil
}

func (f *Filesystem) Discard(staged string) error {
	err := f.root.Remove(staged)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (f *Filesystem) Publish(ctx context.Context, ref StoreRef, root *os.Root, name, staged string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if root == nil {
		if err := f.root.MkdirAll(ref.path, 0700); err != nil {
			return err
		}
		var err error
		root, err = f.OpenStore(ref)
		if err != nil {
			return err
		}
		defer func() { _ = root.Close() }()
	}
	if err := root.MkdirAll(path.Dir(name), 0700); err != nil {
		return err
	}
	if err := CheckPath(root, path.Dir(name)); err != nil {
		return err
	}
	if err := CheckPath(f.root, ref.staging); err != nil {
		return err
	}
	source, err := f.root.Open(ref.staging)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	destination, err := root.Open(path.Dir(name))
	if err != nil {
		return err
	}
	defer func() { _ = destination.Close() }()
	if err := renameFile(source, path.Base(staged), destination, path.Base(name)); err != nil {
		return fmt.Errorf("publish memory file: %w", err)
	}
	return f.syncStoreParents(ref, root, path.Dir(name))
}

func (f *Filesystem) Sync(ref StoreRef, root *os.Root, name string) error {
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	err = errors.Join(file.Sync(), file.Close())
	if err != nil {
		return err
	}
	return f.syncStoreParents(ref, root, path.Dir(name))
}

func (f *Filesystem) syncStoreParents(ref StoreRef, root *os.Root, name string) error {
	if err := syncParents(root, name); err != nil {
		return err
	}
	return syncParents(f.root, path.Dir(ref.path))
}

func syncParents(root *os.Root, name string) error {
	for {
		dir, err := root.Open(name)
		if err != nil {
			return err
		}
		err = errors.Join(dir.Sync(), dir.Close())
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		name = path.Dir(name)
	}
}

func (f *Filesystem) RemoveStore(ref StoreRef) error {
	return f.remove(ref.path, ref.staging)
}

func (f *Filesystem) RemoveScope(orgID uuid.UUID, projectID *uuid.UUID) error {
	if f == nil {
		return nil
	}
	scope, err := publicid.Encode(publicid.KindOrganization, orgID)
	if err != nil {
		return err
	}
	if projectID != nil {
		project, err := publicid.Encode(publicid.KindProject, *projectID)
		if err != nil {
			return err
		}
		scope += "/" + project
	}
	return f.remove(scope, ".staging/"+scope, ".locks/"+scope)
}

func (f *Filesystem) remove(paths ...string) error {
	root, err := f.root.OpenRoot(".")
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	var cleanupErr error
	for _, name := range paths {
		if _, err := root.Lstat(name); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				cleanupErr = errors.Join(cleanupErr, err)
			}
			continue
		}
		err := root.RemoveAll(name)
		if err == nil {
			err = syncParents(f.root, path.Dir(name))
		}
		cleanupErr = errors.Join(cleanupErr, err)
	}
	return cleanupErr
}

func (f *Filesystem) RemoveFile(ref StoreRef, root *os.Root, name string) error {
	if err := root.Remove(name); err != nil {
		return err
	}
	dir := path.Dir(name)
	for dir != "." {
		if err := root.Remove(dir); err != nil {
			if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
				break
			}
			return err
		}
		dir = path.Dir(dir)
	}
	return f.syncStoreParents(ref, root, dir)
}
