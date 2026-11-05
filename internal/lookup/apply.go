package lookup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// Apply atomically changes the item, attachment row and lookup provenance.
// Processed poster bytes are written before the transaction and old files are
// removed only after commit.
func (s *DBStore) Apply(ctx context.Context, p ApplyParams) (items.Item, error) {
	ctx = scope.With(ctx, s.workspaceID)
	if err := validateApply(p); err != nil {
		return items.Item{}, err
	}
	prepared, err := s.preparePoster(p.Poster)
	if err != nil {
		return items.Item{}, err
	}
	usedPoster := false
	var oldFiles []string
	itemID := p.ItemID
	err = s.repo.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return fmt.Errorf("lock tree: %w", err)
		}
		if p.Mode == ModeCreate || p.Mode == ModeCreateUntracked {
			var txErr error
			itemID, usedPoster, txErr = s.applyCreate(ctx, r, p, prepared)
			return txErr
		}
		var txErr error
		usedPoster, oldFiles, txErr = s.applyExisting(ctx, r, p, prepared)
		return txErr
	})
	if err != nil {
		if prepared != nil {
			s.discardPrepared(context.WithoutCancel(ctx), prepared)
		}
		return items.Item{}, err
	}
	if prepared != nil && !usedPoster {
		s.deleteFiles(context.WithoutCancel(ctx), prepared.key, prepared.thumbKey)
	}
	s.deleteFiles(context.WithoutCancel(ctx), oldFiles...)
	return s.items.Get(scope.With(ctx, s.workspaceID), itemID)
}

func (s *DBStore) applyCreate(ctx context.Context, r Repo, p ApplyParams, poster *preparedPoster) (uuid.UUID, bool, error) {
	typ, err := r.ListType(ctx, p.ListID)
	if err != nil {
		return uuid.Nil, false, err
	}
	if typeKey(typ) != p.Type {
		return uuid.Nil, false, apperr.TypeMismatch()
	}
	if p.CreateInput.Price != nil {
		if err := items.ValidatePrice(p.CreateInput.Price); err != nil {
			return uuid.Nil, false, err
		}
		if err := priceAllowed(typ); err != nil {
			return uuid.Nil, false, err
		}
	}
	original, err := cleanLookupTitle(p.OriginalTitle)
	if p.OriginalTitle == "" {
		original, err = cleanLookupTitle(p.CreateInput.Title)
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	title, _ := cleanLookupTitle(p.Title)
	if typeKey(typ) == types.Book {
		// Books keep the title the user typed (Italian stays Italian); only the
		// movie/TV flow adopts the provider's title.
		if typed, terr := cleanLookupTitle(p.CreateInput.Title); terr == nil {
			title = typed
		}
	}
	website, err := cleanLookupWebsite(p.CreateInput.Website)
	if err != nil {
		return uuid.Nil, false, err
	}
	untracked := p.Mode == ModeCreateUntracked
	var idKey string
	var idValue any
	metadata := cloneLookupMap(p.Fields)
	if !untracked {
		idKey, idValue = identity(p.Provider, p.SourceID)
		metadata[idKey] = idValue
	}
	for path, value := range flatten(p.CreateInput.Metadata) {
		if path == "streaming.link" {
			continue // server-managed: only the TMDB lookup sets it
		}
		setPath(metadata, path, value)
	}
	if !untracked {
		metadata[idKey] = idValue
	}
	fillOnly(metadata, p.FillOnly, false)
	fields, err := typeFields(typ)
	if err != nil {
		return uuid.Nil, false, err
	}
	metadata, err = items.ValidateImport(typ, fields, metadata)
	if err != nil {
		return uuid.Nil, false, err
	}
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("encode lookup metadata: %w", err)
	}
	// The new item must not repeat an entry of the list (F-026): same provider
	// id, or same stored title when one side has no id. The tree lock taken by
	// Apply makes the check race-free.
	entries, err := r.ListTitles(ctx, p.ListID)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("list titles: %w", err)
	}
	if items.EntryTaken(entries, items.TitleEntry{Title: title, Metadata: metaJSON}) {
		return uuid.Nil, false, apperr.DuplicateTitle()
	}
	next, err := r.NextSortOrder(ctx, p.ListID)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("next item sort order: %w", err)
	}
	var attachmentID uuid.NullUUID
	if poster != nil {
		if err := s.insertAttachment(ctx, r, poster); err != nil {
			return uuid.Nil, false, err
		}
		attachmentID = uuid.NullUUID{UUID: poster.id, Valid: true}
	}
	now := s.clock.Now()
	id, err := r.InsertItem(ctx, newItem{
		ListID: p.ListID, Title: title, Website: website, Price: p.CreateInput.Price, Metadata: metaJSON,
		ImageAttachmentID: attachmentID, SortOrder: next, Now: now,
	})
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("insert lookup item: %w", err)
	}
	if untracked {
		return id, poster != nil, nil
	}
	requested := flatten(p.Fields)
	for path := range flatten(p.CreateInput.Metadata) {
		if path != "streaming.link" {
			delete(requested, path)
		}
	}
	snapshots := snapshotPresent(metadata, requested)
	snapshots[idKey] = metadata[idKey]
	var posterFetchedAt *time.Time
	if poster != nil {
		posterFetchedAt = &now
	}
	if err := s.saveProvenance(ctx, r, id, p, original, snapshots, &title, attachmentID, now, poster != nil, p.PosterPath, posterFetchedAt); err != nil {
		return uuid.Nil, false, err
	}
	return id, poster != nil, nil
}

func (s *DBStore) applyExisting(ctx context.Context, r Repo, p ApplyParams, poster *preparedPoster) (bool, []string, error) {
	cur, err := r.LockItem(ctx, p.ItemID)
	if err != nil {
		return false, nil, err
	}
	if cur.Version != p.ExpectedVersion {
		return false, nil, apperr.VersionConflict(cur.Version)
	}
	if typeKey(cur.Type) != p.Type {
		return false, nil, apperr.TypeMismatch()
	}

	old, hasOld, err := r.Import(ctx, p.ItemID)
	if err != nil {
		return false, nil, err
	}
	metadata := map[string]any{}
	if err := json.Unmarshal(cur.Metadata, &metadata); err != nil {
		return false, nil, fmt.Errorf("decode item metadata: %w", err)
	}
	idKey, idValue := identity(p.Provider, p.SourceID)
	candidate := cloneLookupMap(p.Fields)
	candidate[idKey] = idValue
	merged, snapshots := mergeMetadata(p.Mode, metadata, candidate, old, hasOld, idKey)
	if p.Mode == ModeFill || p.Mode == ModeRematch {
		fillOnly(merged, p.FillOnly, true)
	}
	fields, err := typeFields(cur.Type)
	if err != nil {
		return false, nil, err
	}
	merged, err = items.ValidateImport(cur.Type, fields, merged)
	if err != nil {
		return false, nil, err
	}
	snapshots = snapshotPresent(merged, snapshots)
	metaJSON, err := json.Marshal(merged)
	if err != nil {
		return false, nil, fmt.Errorf("encode lookup metadata: %w", err)
	}

	title, importedTitle, originalTitle := mergeTitle(p, cur.Title, old, hasOld)
	attachmentID := cur.ImageAttachmentID
	imageURL := cur.ImageURL
	posterOwned := old.AttachmentID != nil && cur.ImageAttachmentID.Valid && cur.ImageAttachmentID.UUID == *old.AttachmentID
	usedPoster := false
	var oldAttachment uuid.NullUUID
	posterFetchedAt := old.PosterFetchedAt
	posterPath := old.PosterPath
	if p.Mode == ModeScheduled && posterOwned && p.RefreshPoster {
		oldAttachment = cur.ImageAttachmentID
		imageURL = nil
		if poster != nil {
			if err := s.insertAttachment(ctx, r, poster); err != nil {
				return false, nil, err
			}
			attachmentID = uuid.NullUUID{UUID: poster.id, Valid: true}
			usedPoster, posterPath = true, p.PosterPath
			now := s.clock.Now()
			posterFetchedAt = &now
		} else {
			attachmentID = uuid.NullUUID{}
			posterFetchedAt, posterPath = nil, ""
		}
	} else if !cur.ImageAttachmentID.Valid && cur.ImageURL == nil && poster != nil && (p.Mode != ModeScheduled || old.AttachmentID == nil) {
		if err := s.insertAttachment(ctx, r, poster); err != nil {
			return false, nil, err
		}
		attachmentID = uuid.NullUUID{UUID: poster.id, Valid: true}
		usedPoster, posterPath = true, p.PosterPath
		now := s.clock.Now()
		posterFetchedAt = &now
	} else if !posterOwned {
		posterFetchedAt, posterPath = nil, ""
	}

	visibleChanged := title != cur.Title || !sameJSON(metaJSON, cur.Metadata) || imageURLChanged(imageURL, cur.ImageURL) || attachmentID != cur.ImageAttachmentID
	now := s.clock.Now()
	if visibleChanged {
		if err := r.UpdateItem(ctx, p.ItemID, itemUpdate{
			Title: title, Metadata: metaJSON, ImageURL: imageURL, ImageAttachmentID: attachmentID, Now: now,
		}); err != nil {
			return false, nil, fmt.Errorf("update lookup item: %w", err)
		}
	}
	fetchedAt := now
	if hasOld && p.Mode == ModeFill {
		// Fill does not replace nonempty values, so it cannot certify their
		// freshness. New empty fields filled in this pass conservatively share
		// the earlier fetch time, so the weekly refresh counts from the last real one.
		fetchedAt = old.FetchedAt
	}
	if err := s.saveProvenance(ctx, r, p.ItemID, p, originalTitle, snapshots, importedTitle, attachmentID, fetchedAt, usedPoster, posterPath, posterFetchedAt); err != nil {
		return false, nil, err
	}
	var oldFiles []string
	if oldAttachment.Valid && (!attachmentID.Valid || oldAttachment.UUID != attachmentID.UUID) {
		row, err := r.DeleteAttachment(ctx, oldAttachment.UUID)
		if err != nil {
			return false, nil, fmt.Errorf("delete replaced lookup attachment: %w", err)
		}
		oldFiles = []string{row.StorageKey, row.ThumbnailStorageKey}
	}
	return usedPoster, oldFiles, nil
}

func (s *DBStore) saveProvenance(ctx context.Context, r Repo, itemID uuid.UUID, p ApplyParams, originalTitle string, snapshots map[string]any, importedTitle *string, attachmentID uuid.NullUUID, now time.Time, newPoster bool, retainedPosterPath string, posterFetchedAt *time.Time) error {
	// streaming.country comes from workspace settings and is required whenever
	// any streaming leaf remains. It is structural context rather than TMDB
	// content, so it is not tracked as an imported value.
	delete(snapshots, "streaming.country")
	deadlines := legacyDeadlinesFor(p.Provider)
	fieldsJSON, err := json.Marshal(snapshots)
	if err != nil {
		return fmt.Errorf("encode imported fields: %w", err)
	}
	var posterPath *string
	var posterRefresh, posterExpiry *time.Time
	if attachmentID.Valid && posterFetchedAt != nil {
		path := retainedPosterPath
		if newPoster {
			path = p.PosterPath
		}
		posterPath = &path
		if deadlines != nil {
			refresh := addMonthsClamped(*posterFetchedAt, deadlines.RefreshMonths)
			expires := addMonthsClamped(*posterFetchedAt, deadlines.ExpireMonths)
			posterRefresh = &refresh
			posterExpiry = &expires
		}
	} else {
		attachmentID = uuid.NullUUID{}
		posterFetchedAt = nil
	}
	// Open Library imports carry no deadlines at all (D-095).
	var refreshAfter, expiresAt *time.Time
	if deadlines != nil {
		refresh := addMonthsClamped(now, deadlines.RefreshMonths)
		expires := addMonthsClamped(now, deadlines.ExpireMonths)
		refreshAfter, expiresAt = &refresh, &expires
	}
	if err := r.SaveImport(ctx, importSave{
		ItemID: itemID, Provider: p.Provider, SourceID: p.SourceID, Type: p.Type,
		ImportedFields: fieldsJSON, ImportedTitle: importedTitle, OriginalTitle: originalTitle,
		AttachmentID: attachmentID, PosterPath: posterPath, PosterFetchedAt: posterFetchedAt,
		PosterRefreshAfter: posterRefresh, PosterExpiresAt: posterExpiry,
		FetchedAt: now, RefreshAfter: refreshAfter, ExpiresAt: expiresAt,
	}); err != nil {
		return fmt.Errorf("save lookup import: %w", err)
	}
	return nil
}

// priceAllowed is the 422 on price for a list type without a price (the type's
// allows_price, or no type at all).
func priceAllowed(typ *string) error {
	if typ == nil {
		return items.PriceAllowed(false)
	}
	def, ok := types.Lookup(*typ)
	if !ok {
		return fmt.Errorf("load list type definition: %q is not defined", *typ)
	}
	return items.PriceAllowed(def.AllowsPrice)
}

// typeFields returns the fields a metadata write is checked against. A nil type
// (a list with no type) has none. A type that is not built in is the 422 of the
// metadata rules.
func typeFields(typ *string) ([]types.Field, error) {
	if typ == nil {
		return nil, nil
	}
	def, ok := types.Lookup(*typ)
	if !ok {
		return nil, apperr.Validation(apperr.FieldError{Field: "type", Message: "is not a known item type"})
	}
	return def.Fields, nil
}
