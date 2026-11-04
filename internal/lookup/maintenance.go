package lookup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

const (
	// MaintenanceInterval is how often a weekly pass runs (D-123). Each run is
	// small, so the TMDB load stays flat over the week.
	MaintenanceInterval = time.Hour
	// scheduleInterval is how long after its last refresh a movie or TV item is due.
	scheduleInterval = 7 * 24 * time.Hour
	// scheduleBatchSize bounds the refreshes of one run: 10 an hour is about 1,680
	// items a week, each one a few paced TMDB calls.
	scheduleBatchSize = 10
	// transientRetry is when an item whose refresh failed for a TMDB or network reason
	// is tried again, and matchRetry when TMDB no longer has its id.
	transientRetry = 24 * time.Hour
	matchRetry     = scheduleInterval
)

// errNotScheduled marks an item the weekly refresh leaves alone: a book, a
// lookup_disabled item, or one that stopped being eligible after the list was read.
var errNotScheduled = errors.New("item is not scheduled for refresh")

// RefreshFunc refreshes one scheduled item at the version the pass read.
type RefreshFunc func(ctx context.Context, itemID uuid.UUID, version int) error

// MaintenanceResult counts one weekly pass.
type MaintenanceResult struct {
	Refreshed int
	// Failed counts refreshes that failed and are retried later (see retryAt).
	Failed int
	// Stopped is set when the pass ended early: TMDB rate limited it, or lookup is disabled.
	Stopped bool
}

// Maintain runs one weekly pass (D-123): it refreshes up to scheduleBatchSize
// movie and TV items that are due, oldest refresh first, and returns. Books,
// trashed and lookup_disabled items are never listed. Values are only ever
// overwritten by a successful refresh: a failure leaves the item and its
// provenance as they were.
//
// Retry state lives in memory (retryAt). A transient failure (TMDB down, a 5xx,
// a network error) is retried after a day and a 404 after a week, so a broken
// item does not take a slot in every run. A restart forgets it, which costs one
// extra attempt. Maintain is not safe for concurrent use: cmd/api runs it once at
// startup and then only from Run's goroutine.
func (s *DBStore) Maintain(ctx context.Context, refresh RefreshFunc) (MaintenanceResult, error) {
	ctx = scope.With(ctx, s.workspaceID)
	var result MaintenanceResult
	now := s.clock.Now()
	for id, at := range s.retryAt {
		if !at.After(now) {
			delete(s.retryAt, id)
		}
	}
	// Items waiting for a retry are still listed, so ask for room for them: the
	// run keeps scheduleBatchSize attempts.
	due, err := s.repo.ScheduledItems(ctx, now.Add(-scheduleInterval), int32(scheduleBatchSize+len(s.retryAt)))
	if err != nil {
		return result, fmt.Errorf("list TMDB items due for refresh: %w", err)
	}
	attempts := 0
	for _, item := range due {
		if attempts == scheduleBatchSize {
			break
		}
		if _, waiting := s.retryAt[item.ItemID]; waiting {
			continue
		}
		attempts++
		err := refresh(ctx, item.ItemID, item.ItemVersion)
		if err == nil {
			result.Refreshed++
			continue
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if stopsRun(err) {
			result.Stopped = true
			s.log.WarnContext(ctx, "weekly TMDB refresh stopped for this run", "err", err)
			return result, nil
		}
		if leavesItem(err) {
			continue // changed or trashed meanwhile: still due, the next run reads it again
		}
		retry := transientRetry
		if errors.Is(err, ErrMatchNotFound) {
			retry = matchRetry
		}
		s.retryAt[item.ItemID] = s.clock.Now().Add(retry)
		result.Failed++
		s.log.WarnContext(ctx, "weekly TMDB refresh failed; retried later", "item_id", item.ItemID, "retry_in", retry, "err", err)
	}
	return result, nil
}

// stopsRun reports the failures that end a run at once: a rate limit (TMDB asks
// for a pause) and a disabled lookup. Nothing else is tried until the next run.
func stopsRun(err error) bool {
	var disabled *apperr.LookupDisabledError
	return errors.Is(err, ErrProviderRateLimited) || errors.Is(err, ErrDisabled) || errors.As(err, &disabled)
}

// leavesItem reports the outcomes that are not failures of the item: it is no
// longer eligible, its version moved on, or it was trashed. It stays due.
func leavesItem(err error) bool {
	var conflict *apperr.VersionConflictError
	var missing *apperr.NotFoundError
	return errors.Is(err, errNotScheduled) || errors.As(err, &conflict) || errors.As(err, &missing)
}

// Run waits for each interval and runs a weekly pass until ctx is cancelled.
// Startup maintenance is deliberately separate so cmd/api can finish it
// before accepting requests.
func (s *DBStore) Run(ctx context.Context, interval time.Duration, refresh RefreshFunc) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := s.Maintain(ctx, refresh)
			if err != nil {
				if ctx.Err() == nil {
					s.log.WarnContext(ctx, "weekly TMDB refresh failed; will retry", "err", err)
				}
				continue
			}
			if result.Refreshed > 0 || result.Failed > 0 || result.Stopped {
				s.log.InfoContext(ctx, "weekly TMDB refresh run finished",
					"refreshed", result.Refreshed, "failed", result.Failed, "stopped", result.Stopped)
			}
		}
	}
}
