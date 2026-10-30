package trash

import (
	"context"
	"time"
)

// PurgeExpired is the auto-purge job (F012-B3): it deletes for good every batch
// trashed more than the workspace's trash_retention_days ago, with the same rules
// as PurgeAll. A NULL retention (the default, or no settings row) purges nothing
// and returns 0. The retention is read on the pool before the transaction; the
// batches are read under the tree lock. It returns the number of batches removed.
func (t *Service) PurgeExpired(ctx context.Context) (int, error) {
	days, err := t.store.RetentionDays(ctx)
	if err != nil {
		return 0, err
	}
	if days == nil {
		return 0, nil
	}
	cutoff := t.now().Add(-time.Duration(*days) * 24 * time.Hour)

	var (
		keys    []string
		removed int
	)
	err = t.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return err
		}
		ids, err := r.ExpiredBatchIDs(ctx, cutoff)
		if err != nil {
			return err
		}
		keys, err = t.purgeBatches(ctx, r, ids)
		if err != nil {
			return err
		}
		removed = len(ids)
		return nil
	})
	if err != nil {
		return 0, err
	}
	t.deleteFiles(ctx, keys)
	return removed, nil
}

// RunAutoPurge runs PurgeExpired once at start-up and then every interval until
// ctx is cancelled, logging the count of each run (the images sweeper's shape).
// A failed run is logged and retried by the next one.
func (t *Service) RunAutoPurge(ctx context.Context, interval time.Duration) {
	run := func() {
		n, err := t.PurgeExpired(ctx)
		if err != nil {
			if ctx.Err() == nil {
				t.log.WarnContext(ctx, "trash auto-purge failed; will retry", "err", err)
			}
			return
		}
		t.log.InfoContext(ctx, "trash auto-purge finished", "purged", n)
	}
	run()
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			run()
		}
	}
}
