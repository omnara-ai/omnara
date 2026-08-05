package storeutil

import (
	"context"
	"fmt"

	"github.com/omnara-ai/omnara/internal/notifications"
)

func CommitTxWithNotifications(
	ctx context.Context,
	tx interface{ Commit(context.Context) error },
	txNotifications *notifications.TxNotifications,
	publisher notifications.PostCommitPublisher,
	operation string,
) error {
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit %s: %w", operation, err)
	}
	txNotifications.Flush(context.WithoutCancel(ctx), publisher)
	return nil
}
