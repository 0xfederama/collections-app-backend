package lookup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// The generic lookup backfill (F-028, D-164). Every provider-linked item is re-read once after the
// import generation was bumped: a movie or TV item by the weekly refresh's rule, a book by the fill
// rule (only empty fields and an empty description). Progress is the import_generation column, so a
// restart resumes and a second pass over a finished backfill is a no-op.

const (
	// BackfillInterval is how often a backfill pass runs: its own ticker, apart from the weekly pass.
	BackfillInterval = 10 * time.Minute
	// backfillBatchSize bounds one pass: 25 items every 10 minutes is about 150 an hour, far under
	// TMDB's and Open Library's limits.
	backfillBatchSize = 25
)

// BackfillResult counts one backfill pass.
type BackfillResult struct {
	// Refreshed counts the items re-read by this pass; Failed the ones that failed and wait for a retry.
	Refreshed int
	Failed    int
	// Remaining counts the eligible items still below the generation after the pass.
	Remaining int
	// Stopped is set when the pass ended early: a rate limit, or lookup is disabled.
	Stopped bool
	// Complete is set by the one pass that found nothing left.
	Complete bool
}

// Backfill runs one pass of the backfill: up to backfillBatchSize items below the current generation,
// oldest fetch first. tmdb re-reads a movie or TV item (RefreshScheduled); openLibrary fills a book
// (Service.BackfillFill). Items waiting for a retry are skipped and count against the batch as the
// weekly pass does. A rate limit or a disabled lookup stops the pass; any other failure waits in
// memory for the weekly pass's retry interval. A cancelled context ends the pass with its error.
func (s *DBStore) Backfill(ctx context.Context, tmdb, openLibrary RefreshFunc) (BackfillResult, error) {
	ctx = scope.With(ctx, s.workspaceID)
	var result BackfillResult
	now := s.clock.Now()
	for id, at := range s.backfillRetryAt {
		if !at.After(now) {
			delete(s.backfillRetryAt, id)
		}
	}
	due, err := s.repo.BackfillItems(ctx, currentImportGeneration, int32(backfillBatchSize+len(s.backfillRetryAt)))
	if err != nil {
		return result, fmt.Errorf("list items for backfill: %w", err)
	}
	attempts := 0
	for _, item := range due {
		if attempts == backfillBatchSize {
			break
		}
		if _, waiting := s.backfillRetryAt[item.ItemID]; waiting {
			continue
		}
		attempts++
		refresh := tmdb
		if item.Provider == ProviderOpenLibrary {
			refresh = openLibrary
		}
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
			s.log.WarnContext(ctx, "lookup backfill stopped for this pass", "err", err)
			break
		}
		if leavesItem(err) {
			continue // changed or trashed meanwhile: still below, the next pass reads it again
		}
		retry := transientRetry
		if errors.Is(err, ErrMatchNotFound) {
			retry = matchRetry
		}
		s.backfillRetryAt[item.ItemID] = s.clock.Now().Add(retry)
		result.Failed++
		s.log.WarnContext(ctx, "lookup backfill failed; retried later", "item_id", item.ItemID, "retry_in", retry, "err", err)
	}
	remaining, err := s.repo.BackfillRemaining(ctx, currentImportGeneration)
	if err != nil {
		return result, fmt.Errorf("count items for backfill: %w", err)
	}
	result.Remaining = remaining
	s.log.InfoContext(ctx, "lookup backfill progress", "done", result.Refreshed, "remaining", remaining,
		"failed", result.Failed, "stopped", result.Stopped)
	if remaining == 0 && !s.backfillDone {
		s.backfillDone = true
		result.Complete = true
		s.log.InfoContext(ctx, "lookup backfill complete")
	}
	return result, nil
}

// RunBackfill runs a backfill pass every interval until ctx is cancelled (cmd/api starts it).
// Passes never overlap: one goroutine runs them.
func (s *DBStore) RunBackfill(ctx context.Context, interval time.Duration, tmdb, openLibrary RefreshFunc) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.Backfill(ctx, tmdb, openLibrary); err != nil && ctx.Err() == nil {
				s.log.WarnContext(ctx, "lookup backfill pass failed; will retry", "err", err)
			}
		}
	}
}

// BackfillFill is the backfill's step for a book (D-164): the Open Library fill of D-095, which writes
// only empty fields and an empty description, at the version the pass read. Provider failures come back
// unchanged (fetchError, ErrProviderRateLimited) so the pass can classify them. A cover that fails is
// not fatal: the book is filled without one, as the lookup does.
func (s *Service) BackfillFill(ctx context.Context, itemID uuid.UUID, expectedVersion int) error {
	ctx = scope.With(ctx, s.workspaceID)
	it, err := s.items.Get(ctx, itemID)
	if err != nil {
		return err
	}
	if it.Version != expectedVersion {
		return apperr.VersionConflict(it.Version)
	}
	provider := s.providers[typeKey(it.Type)]
	if provider == nil || provider.Name() != ProviderOpenLibrary {
		return errNotScheduled
	}
	if !provider.Enabled() {
		return apperr.LookupDisabled()
	}
	if lookupDisabled(it.Metadata) {
		return errNotScheduled
	}
	id := metadataSourceID(provider.Name(), it.Metadata)
	if id == "" {
		return fmt.Errorf("backfill: item has no valid %s id", providerLabel(provider.Name()))
	}
	settings, err := s.settings.Get(ctx)
	if err != nil {
		return err
	}
	details, err := provider.Details(ctx, typeKey(it.Type), id, settings.MetadataLanguage, settings.StreamingCountryCode)
	if err != nil {
		return fetchError{err}
	}
	if details.SourceID != id {
		return apperr.UpstreamUnavailable()
	}
	refreshPoster := it.Image == nil
	var poster *images.Processed
	if refreshPoster && details.ImagePath != "" {
		poster, err = s.scheduledPoster(ctx, provider, details.ImagePath)
		if err != nil {
			if stopsRun(err) {
				return err
			}
			poster = nil
		}
	}
	_, err = s.store.Apply(ctx, ApplyParams{
		Mode: ModeFill, ItemID: itemID, ExpectedVersion: expectedVersion, Type: typeKey(it.Type),
		Provider: provider.Name(), SourceID: details.SourceID,
		Title: details.Title, OriginalTitle: it.Title,
		Fields: details.Fields, FillOnly: defaultStoreFill(provider, details.FillOnly, settings.PreferredStore),
		PosterPath: details.ImagePath, Poster: poster, RefreshPoster: refreshPoster,
		Synopsis: details.Synopsis, HistoryLimit: settings.MarkdownHistoryLimit,
	})
	return err
}
