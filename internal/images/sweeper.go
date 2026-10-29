package images

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
)

// orphanAge is how old a file or an unreferenced attachment must be before the
// sweeper touches it. It keeps the sweeper away from an upload that is still
// between "files written" and "transaction committed".
const orphanAge = time.Hour

// SweepInterval is the pause between two sweeper runs (a run also happens at startup).
const SweepInterval = 24 * time.Hour

// SweepResult counts what a run removed.
type SweepResult struct {
	Files       int // files with no attachment row
	Attachments int // attachment rows nothing references (their files go too)
}

// Sweep removes what crashes and failed deletions leave behind (ARCHITECTURE §4):
//
//  1. attachment rows no item points to and older than an hour, with their
//     files (for example after an item was purged);
//  2. files under images/ that no attachment row names and that are older than
//     an hour (a crash between writing the files and committing, or a delete
//     that failed after a replacement).
//
// A deletion that fails is logged and retried by the next run. Each pass
// returns the first error after trying everything.
func (s *Service) Sweep(ctx context.Context) (SweepResult, error) {
	var res SweepResult
	cutoff := s.clock.Now().Add(-orphanAge)
	q := sqlc.New(s.db)

	ids, err := q.ListUnreferencedAttachments(ctx, sqlc.ListUnreferencedAttachmentsParams{WorkspaceID: s.workspaceID, Cutoff: cutoff})
	if err != nil {
		return res, fmt.Errorf("list unreferenced attachments: %w", err)
	}
	var firstErr error
	for _, id := range ids {
		n, err := s.sweepAttachment(ctx, q, id)
		res.Attachments += n
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	known, err := q.ListAttachmentKeys(ctx, s.workspaceID)
	if err != nil {
		return res, fmt.Errorf("list attachment keys: %w", err)
	}
	inUse := make(map[string]bool, len(known))
	for _, k := range known {
		inUse[k] = true
	}
	var orphans []string
	err = s.store.Walk("images", func(key string, modTime time.Time) error {
		if !inUse[key] && modTime.Before(cutoff) {
			orphans = append(orphans, key)
		}
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("walk images: %w", err)
	}
	for _, key := range orphans {
		if err := s.store.Delete(key); err != nil {
			s.log.WarnContext(ctx, "sweeper could not delete an orphan file; will retry", "err", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("delete orphan file: %w", err)
			}
			continue
		}
		res.Files++
	}
	return res, firstErr
}

// sweepAttachment deletes one unreferenced attachment: the row first (guarded
// by NOT EXISTS, so an item that grabbed it meanwhile keeps it), then the files.
// A file that cannot be deleted becomes an orphan file for the second pass.
func (s *Service) sweepAttachment(ctx context.Context, q *sqlc.Queries, id uuid.UUID) (int, error) {
	row, err := q.DeleteAttachment(ctx, sqlc.DeleteAttachmentParams{ID: id, WorkspaceID: s.workspaceID})
	if err != nil {
		return 0, nil //nolint:nilerr // no row: referenced again or already gone
	}
	s.deleteFiles(ctx, row.StorageKey, row.ThumbnailStorageKey)
	return 1, nil
}

// RunSweeper sweeps once now and then every interval until ctx is cancelled.
// It logs counts only, never paths.
func (s *Service) RunSweeper(ctx context.Context, interval time.Duration) {
	run := func() {
		res, err := s.Sweep(ctx)
		if err != nil && ctx.Err() == nil {
			s.log.WarnContext(ctx, "image sweeper finished with errors", "err", err,
				"files_removed", res.Files, "attachments_removed", res.Attachments)
			return
		}
		if res.Files > 0 || res.Attachments > 0 {
			s.log.InfoContext(ctx, "image sweeper removed leftovers", "files_removed", res.Files, "attachments_removed", res.Attachments)
		}
	}
	run()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
