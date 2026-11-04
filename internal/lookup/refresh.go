package lookup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// listRefreshBatchSize bounds one list refresh. Fifty items are about fifty TMDB
// detail calls and a few posters, roughly 25 seconds at the client's pacing, inside
// the server's 60-second write timeout. The oldest fetches go first, so repeated
// calls reach the whole list.
const listRefreshBatchSize = 50

// fetchError marks a failed TMDB fetch (the details or a poster). An explicit refresh
// answers it with ProviderError; the weekly pass and the list refresh classify the
// wrapped sentinel (ErrProviderRateLimited, ErrMatchNotFound, ...) through Unwrap.
type fetchError struct{ err error }

func (e fetchError) Error() string { return e.err.Error() }
func (e fetchError) Unwrap() error { return e.err }

// RefreshScheduled is the weekly maintenance refresh of one movie or TV item
// (D-123), and the per-item step of the list refresh. It fetches the TMDB details of
// the item's stored tmdb_id and overwrites every fetched field and the title, user
// edits included. Books and lookup_disabled items are never refreshed. Provider
// errors come back unchanged (ErrProviderRateLimited, ErrMatchNotFound, others) so
// the pass in maintenance.go can classify them; nothing is written unless the details
// fetch and any poster download succeeded.
func (s *Service) RefreshScheduled(ctx context.Context, itemID uuid.UUID, expectedVersion int) error {
	ctx = scope.With(ctx, s.workspaceID)
	it, err := s.items.Get(ctx, itemID)
	if err != nil {
		return err
	}
	if it.Version != expectedVersion {
		return apperr.VersionConflict(it.Version)
	}
	provider := s.providers[typeKey(it.Type)]
	if provider == nil || provider.Name() != ProviderTMDB {
		return errNotScheduled // books are never refreshed (D-095, D-123)
	}
	if !provider.Enabled() {
		return apperr.LookupDisabled()
	}
	if lookupDisabled(it.Metadata) {
		return errNotScheduled
	}
	id := metadataSourceID(provider.Name(), it.Metadata)
	if id == "" {
		return fmt.Errorf("scheduled refresh: item has no valid %s id", providerLabel(provider.Name()))
	}
	_, err = s.refreshTMDB(ctx, itemID, it, id, expectedVersion, provider)
	return err
}

// refreshExplicit is the refresh mode of POST /items/{id}/lookup: the weekly rule for
// one movie or TV item, on the TMDB id the item has or the one the caller selected.
// Unlike the weekly pass it also runs on a lookup_disabled item, because the user asked
// for it. Provider failures answer the lookup errors (ProviderError).
func (s *Service) refreshExplicit(ctx context.Context, itemID uuid.UUID, it items.Item, expectedVersion int, provider Provider, selection Selection) (items.Item, error) {
	if provider.Name() != ProviderTMDB {
		return items.Item{}, apperr.Validation(apperr.FieldError{Field: "mode", Message: "refresh is available only for movie and TV items"})
	}
	id := metadataSourceID(provider.Name(), it.Metadata)
	if selected := selection.forProvider(provider.Name()); selected != nil {
		id = *selected
	}
	if id == "" {
		return items.Item{}, apperr.Validation(apperr.FieldError{Field: sourceIDField(provider.Name()), Message: "is required when the item has no TMDB id"})
	}
	if err := provider.ValidateSourceID(id); err != nil {
		return items.Item{}, err
	}
	refreshed, err := s.refreshTMDB(ctx, itemID, it, id, expectedVersion, provider)
	var fetch fetchError
	if errors.As(err, &fetch) {
		return items.Item{}, ProviderError(fetch.err)
	}
	return refreshed, err
}

// refreshTMDB fetches the TMDB details of id and overwrites the item's title and every
// fetched field (D-123). A poster is replaced only when TMDB's path changed and the
// item still carries the poster this refresh fetched (or it has no image at all); a
// user-supplied image stays. Nothing is written unless the fetch and any poster
// download succeeded.
func (s *Service) refreshTMDB(ctx context.Context, itemID uuid.UUID, it items.Item, id string, expectedVersion int, provider Provider) (items.Item, error) {
	typ := typeKey(it.Type)
	settings, err := s.settings.Get(ctx)
	if err != nil {
		return items.Item{}, err
	}
	details, err := provider.Details(ctx, typ, id, settings.MetadataLanguage, settings.StreamingCountryCode)
	if err != nil {
		return items.Item{}, fetchError{err}
	}
	if details.SourceID != id {
		return items.Item{}, apperr.UpstreamUnavailable()
	}
	record, _, err := s.store.Read(ctx, itemID)
	if err != nil {
		return items.Item{}, err
	}
	// The poster is owned when it is the attachment this import fetched. An owned
	// poster follows TMDB's path; a missing image is filled once, and a user's
	// image or a poster the user removed is never fetched again.
	owned := record.AttachmentID != nil && it.Image != nil && it.Image.Source == items.ImageSourceUpload &&
		it.Image.AttachmentID == *record.AttachmentID
	refreshPoster := owned && record.PosterPath != details.ImagePath ||
		!owned && it.Image == nil && record.AttachmentID == nil
	var poster *images.Processed
	if refreshPoster && details.ImagePath != "" {
		poster, err = s.scheduledPoster(ctx, provider, details.ImagePath)
		if err != nil {
			return items.Item{}, err
		}
	}
	return s.store.Apply(ctx, ApplyParams{
		Mode: ModeScheduled, ItemID: itemID, ExpectedVersion: expectedVersion, Type: typ,
		Provider: provider.Name(), SourceID: details.SourceID,
		Title: details.Title, OriginalTitle: it.Title,
		Fields: details.Fields, PosterPath: details.ImagePath, Poster: poster,
		RefreshPoster: refreshPoster,
	})
}

// scheduledPoster downloads and processes a TMDB poster for a refresh. A download
// failure is a fetchError, so a rate limit can stop a run; the item's current image is
// never touched by a failure here.
func (s *Service) scheduledPoster(ctx context.Context, provider Provider, path string) (*images.Processed, error) {
	raw, err := provider.DownloadImage(ctx, path)
	if err != nil {
		return nil, fetchError{err}
	}
	processed, err := images.Process(bytes.NewReader(raw))
	if err != nil {
		return nil, apperr.UpstreamUnavailable()
	}
	return processed, nil
}

// ListRefreshResult counts one list refresh (refreshListLookups).
type ListRefreshResult struct {
	Updated int
	Skipped int
	Failed  int
}

// RefreshList refreshes the movie and TV items of a list that have a TMDB id, one after
// another, through the TMDB client's pacing (refreshListLookups). Each item gets the
// weekly rule (RefreshScheduled) at the version it had when the list was read, so an
// item changed since is skipped and never overwritten.
//
// Skipped: every item of a book or untyped list, items without a TMDB id, lookup_disabled
// items, items changed during the run, and eligible items beyond listRefreshBatchSize
// (the oldest fetches are taken first, so calling again reaches them).
// Failed: refreshes that failed, and the eligible items not reached once TMDB rate limited
// the run or could not be reached. A failed item keeps its values. A cancelled context ends
// the call with its error; the items refreshed so far stay refreshed.
func (s *Service) RefreshList(ctx context.Context, listID uuid.UUID) (ListRefreshResult, error) {
	ctx = scope.With(ctx, s.workspaceID)
	typ, err := s.store.ListType(ctx, listID)
	if err != nil {
		return ListRefreshResult{}, err
	}
	refs, err := s.store.ListItems(ctx, listID)
	if err != nil {
		return ListRefreshResult{}, fmt.Errorf("list items for refresh: %w", err)
	}
	var result ListRefreshResult
	if typ != types.Movie && typ != types.TVShow {
		result.Skipped = len(refs)
		return result, nil
	}
	provider := s.providers[typ]
	if provider == nil || provider.Name() != ProviderTMDB || !provider.Enabled() {
		return ListRefreshResult{}, apperr.LookupDisabled()
	}
	var due []listedItem
	for _, ref := range refs {
		if metadataSourceID(ProviderTMDB, ref.Metadata) == "" || lookupDisabled(ref.Metadata) {
			result.Skipped++
			continue
		}
		due = append(due, ref)
	}
	if len(due) > listRefreshBatchSize {
		result.Skipped += len(due) - listRefreshBatchSize
		due = due[:listRefreshBatchSize]
	}
	for i, ref := range due {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		err := s.RefreshScheduled(ctx, ref.ItemID, ref.ItemVersion)
		switch {
		case err == nil:
			result.Updated++
		case leavesItem(err):
			result.Skipped++
		case ctx.Err() != nil:
			return result, ctx.Err()
		case stopsList(err):
			result.Failed += len(due) - i
			slog.WarnContext(ctx, "list TMDB refresh stopped; the rest are not refreshed", "list_id", listID, "err", err)
			return result, nil
		default:
			result.Failed++
		}
	}
	return result, nil
}

// stopsList reports the failures that end a list refresh: a rate limit, a disabled
// lookup, or TMDB unreachable (a fetch failed for another reason than a 404 for the id).
func stopsList(err error) bool {
	var fetch fetchError
	return stopsRun(err) || (errors.As(err, &fetch) && !errors.Is(err, ErrMatchNotFound))
}
