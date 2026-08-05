package omnarad

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

const (
	installLockFileName = "install.lock"
	installLockWait     = 30 * time.Second
	installLockPoll     = 100 * time.Millisecond
)

func acquireInstallLock(ctx context.Context, home string) (*localstore.Lock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, installLockWait)
	defer cancel()
	ticker := time.NewTicker(installLockPoll)
	defer ticker.Stop()
	for {
		lock, acquired, err := tryAcquireInstallLock(home)
		if err != nil {
			return nil, err
		}
		if acquired {
			return lock, nil
		}
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("wait for install lock: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func tryAcquireInstallLock(home string) (*localstore.Lock, bool, error) {
	store, err := localstore.New(home)
	if err != nil {
		return nil, false, err
	}
	lock, err := localstore.TryAcquireLock(filepath.Join(store.HomeDir(), installLockFileName))
	if errors.Is(err, localstore.ErrLockHeld) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("acquire install lock: %w", err)
	}
	return lock, true, nil
}
