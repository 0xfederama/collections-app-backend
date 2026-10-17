package lookup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/clock"
	"github.com/0xfederama/collections-app-backend/internal/database"
	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/items/kinds"
	"github.com/0xfederama/collections-app-backend/internal/storage"
)

const maxLookupTitleRunes = 300

// DB is satisfied by *pgxpool.Pool.
type DB interface {
	database.TxBeginner
	sqlc.DBTX
}

// DBStore applies one fetched lookup result to item data and its provenance.
type DBStore struct {
	db          DB
	store       storage.FileStore
	clock       clock.Clock
	items       *items.Service
	log         *slog.Logger
	workspaceID uuid.UUID
}

var _ Store = (*DBStore)(nil)

// NewStore returns a lookup store bound to one workspace.
func NewStore(db DB, fileStore storage.FileStore, clk clock.Clock, itemSvc *items.Service, log *slog.Logger, workspaceID uuid.UUID) *DBStore {
	return &DBStore{db: db, store: fileStore, clock: clk, items: itemSvc, log: log, workspaceID: workspaceID}
}

// ListKind returns the kind of an active list.
func (s *DBStore) ListKind(ctx context.Context, listID uuid.UUID) (string, error) {
	kind, err := sqlc.New(s.db).GetLookupListKind(ctx, sqlc.GetLookupListKindParams{ID: listID, WorkspaceID: s.workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apperr.NotFound("list")
	}
	if err != nil {
		return "", fmt.Errorf("get lookup list kind: %w", err)
	}
	return kind, nil
}

// Read returns the current provenance record, including the item's current
// version. A missing record is not an error.
func (s *DBStore) Read(ctx context.Context, itemID uuid.UUID) (Record, bool, error) {
	r, err := sqlc.New(s.db).GetTMDBImport(ctx, sqlc.GetTMDBImportParams{ItemID: itemID, WorkspaceID: s.workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("get TMDB import: %w", err)
	}
	fields := map[string]any{}
	if err := json.Unmarshal(r.ImportedFields, &fields); err != nil {
		return Record{}, false, fmt.Errorf("decode TMDB import fields: %w", err)
	}
	return Record{
		ItemID: r.ItemID, ItemVersion: int(r.ItemVersion), TMDBID: int(r.TmdbID), Kind: r.Kind,
		ImportedFields: fields, ImportedTitle: r.ImportedTitle, OriginalTitle: r.OriginalTitle,
		AttachmentID: nullUUIDPtr(r.AttachmentID), PosterPath: valueOrEmpty(r.PosterPath),
		PosterFetchedAt: r.PosterFetchedAt, FetchedAt: r.FetchedAt,
	}, true, nil
}

type preparedPoster struct {
	id            uuid.UUID
	key, thumbKey string
	processed     *images.Processed
}

// Apply atomically changes the item, attachment row and TMDB provenance.
// Processed poster bytes are written before the transaction and old files are
// removed only after commit.
func (s *DBStore) Apply(ctx context.Context, p ApplyParams) (items.Item, error) {
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
	err = database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := q.LockTree(ctx, s.workspaceID); err != nil {
			return fmt.Errorf("lock tree: %w", err)
		}
		if p.Mode == ModeCreate {
			var txErr error
			itemID, usedPoster, txErr = s.applyCreate(ctx, q, p, prepared)
			return txErr
		}
		var txErr error
		usedPoster, oldFiles, txErr = s.applyExisting(ctx, q, p, prepared)
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
	return s.items.Get(ctx, itemID)
}

func validateApply(p ApplyParams) error {
	if p.Kind != kinds.Movie && p.Kind != kinds.TVShow {
		return apperr.Validation(apperr.FieldError{Field: "kind", Message: "lookup is available only for movie and tv_show"})
	}
	if p.TMDBID <= 0 {
		return apperr.Validation(apperr.FieldError{Field: "tmdb_id", Message: "must be greater than zero"})
	}
	if p.TMDBID > math.MaxInt32 {
		return apperr.Validation(apperr.FieldError{Field: "tmdb_id", Message: "is too large"})
	}
	switch p.Mode {
	case ModeCreate, ModeFill, ModeRematch, ModeRefresh, ModeOwned:
	default:
		return fmt.Errorf("unknown lookup mode %q", p.Mode)
	}
	if _, err := cleanLookupTitle(p.Title); err != nil {
		return err
	}
	if p.Mode == ModeCreate && p.CreateInput == nil {
		return fmt.Errorf("lookup create input is missing")
	}
	return nil
}

func (s *DBStore) applyCreate(ctx context.Context, q *sqlc.Queries, p ApplyParams, poster *preparedPoster) (uuid.UUID, bool, error) {
	kind, err := q.GetLookupListKind(ctx, sqlc.GetLookupListKindParams{ID: p.ListID, WorkspaceID: s.workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, apperr.NotFound("list")
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("get lookup list: %w", err)
	}
	if kind != p.Kind {
		return uuid.Nil, false, apperr.KindMismatch()
	}
	if p.CreateInput.Price != nil {
		return uuid.Nil, false, apperr.Validation(apperr.FieldError{Field: "price", Message: "items of this kind have no price"})
	}
	original, err := cleanLookupTitle(p.OriginalTitle)
	if p.OriginalTitle == "" {
		original, err = cleanLookupTitle(p.CreateInput.Title)
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	title, _ := cleanLookupTitle(p.Title)
	website, err := cleanLookupWebsite(p.CreateInput.Website)
	if err != nil {
		return uuid.Nil, false, err
	}
	metadata := cloneLookupMap(p.Fields)
	metadata["tmdb_id"] = p.TMDBID
	for path, value := range flatten(p.CreateInput.Metadata) {
		setPath(metadata, path, value)
	}
	metadata["tmdb_id"] = p.TMDBID
	metadata, err = kinds.Validate(kind, metadata)
	if err != nil {
		return uuid.Nil, false, err
	}
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("encode lookup metadata: %w", err)
	}
	next, err := q.NextItemSortOrder(ctx, sqlc.NextItemSortOrderParams{ListID: p.ListID, WorkspaceID: s.workspaceID})
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("next item sort order: %w", err)
	}
	var attachmentID uuid.NullUUID
	if poster != nil {
		if err := s.insertAttachment(ctx, q, poster); err != nil {
			return uuid.Nil, false, err
		}
		attachmentID = uuid.NullUUID{UUID: poster.id, Valid: true}
	}
	now := s.clock.Now()
	id, err := q.InsertLookupItem(ctx, sqlc.InsertLookupItemParams{
		WorkspaceID: s.workspaceID, ListID: p.ListID, Title: title, WebsiteUrl: website,
		Metadata: metaJSON, ImageAttachmentID: attachmentID, SortOrder: next, Now: now,
	})
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("insert lookup item: %w", err)
	}
	requested := flatten(p.Fields)
	for path := range flatten(p.CreateInput.Metadata) {
		delete(requested, path)
	}
	snapshots := snapshotPresent(metadata, requested)
	snapshots["tmdb_id"] = metadata["tmdb_id"]
	var posterFetchedAt *time.Time
	if poster != nil {
		posterFetchedAt = &now
	}
	if err := s.saveProvenance(ctx, q, id, p, original, snapshots, &title, attachmentID, now, poster != nil, p.PosterPath, posterFetchedAt); err != nil {
		return uuid.Nil, false, err
	}
	return id, poster != nil, nil
}

func (s *DBStore) applyExisting(ctx context.Context, q *sqlc.Queries, p ApplyParams, poster *preparedPoster) (bool, []string, error) {
	cur, err := q.LockLookupItem(ctx, sqlc.LockLookupItemParams{ID: p.ItemID, WorkspaceID: s.workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, apperr.NotFound("item")
	}
	if err != nil {
		return false, nil, fmt.Errorf("lock lookup item: %w", err)
	}
	if int(cur.Version) != p.ExpectedVersion {
		return false, nil, apperr.VersionConflict(int(cur.Version))
	}
	if cur.Kind != p.Kind {
		return false, nil, apperr.KindMismatch()
	}

	old, hasOld, err := readImportTx(ctx, q, p.ItemID, s.workspaceID)
	if err != nil {
		return false, nil, err
	}
	metadata := map[string]any{}
	if err := json.Unmarshal(cur.Metadata, &metadata); err != nil {
		return false, nil, fmt.Errorf("decode item metadata: %w", err)
	}
	candidate := cloneLookupMap(p.Fields)
	candidate["tmdb_id"] = p.TMDBID
	merged, snapshots := mergeMetadata(p.Mode, metadata, candidate, old, hasOld)
	if p.Mode == ModeOwned && !p.RefreshFields && hasOld {
		merged = cloneLookupMap(metadata)
		snapshots = cloneLookupMap(old.ImportedFields)
	}
	merged, err = kinds.Validate(cur.Kind, merged)
	if err != nil {
		return false, nil, err
	}
	snapshots = snapshotPresent(merged, snapshots)
	metaJSON, err := json.Marshal(merged)
	if err != nil {
		return false, nil, fmt.Errorf("encode lookup metadata: %w", err)
	}

	title, importedTitle, originalTitle := mergeTitle(p, cur.Title, old, hasOld)
	if p.Mode == ModeOwned && !p.RefreshFields && hasOld {
		title, importedTitle, originalTitle = cur.Title, old.ImportedTitle, old.OriginalTitle
	}
	attachmentID := cur.ImageAttachmentID
	imageURL := cur.ImageUrl
	posterOwned := old.AttachmentID != nil && cur.ImageAttachmentID.Valid && cur.ImageAttachmentID.UUID == *old.AttachmentID
	usedPoster := false
	var oldAttachment uuid.NullUUID
	posterFetchedAt := old.PosterFetchedAt
	posterPath := old.PosterPath
	if p.Mode == ModeOwned && posterOwned && p.RefreshPoster {
		oldAttachment = cur.ImageAttachmentID
		imageURL = nil
		if poster != nil {
			if err := s.insertAttachment(ctx, q, poster); err != nil {
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
	} else if !cur.ImageAttachmentID.Valid && cur.ImageUrl == nil && poster != nil && (p.Mode != ModeOwned || old.AttachmentID == nil) {
		if err := s.insertAttachment(ctx, q, poster); err != nil {
			return false, nil, err
		}
		attachmentID = uuid.NullUUID{UUID: poster.id, Valid: true}
		usedPoster, posterPath = true, p.PosterPath
		now := s.clock.Now()
		posterFetchedAt = &now
	} else if !posterOwned {
		posterFetchedAt, posterPath = nil, ""
	}

	visibleChanged := title != cur.Title || !sameJSON(metaJSON, cur.Metadata) || imageURLChanged(imageURL, cur.ImageUrl) || attachmentID != cur.ImageAttachmentID
	now := s.clock.Now()
	if visibleChanged {
		if err := q.UpdateLookupItem(ctx, sqlc.UpdateLookupItemParams{
			Title: title, Metadata: metaJSON, ImageUrl: imageURL, ImageAttachmentID: attachmentID,
			Now: now, ID: p.ItemID, WorkspaceID: s.workspaceID,
		}); err != nil {
			return false, nil, fmt.Errorf("update lookup item: %w", err)
		}
	}
	fetchedAt := now
	if hasOld && (p.Mode == ModeFill || (p.Mode == ModeOwned && !p.RefreshFields)) {
		// Fill does not replace nonempty values, so it cannot certify their
		// freshness. New empty fields filled in this pass conservatively share
		// the earlier deadline.
		fetchedAt = old.FetchedAt
	}
	if err := s.saveProvenance(ctx, q, p.ItemID, p, originalTitle, snapshots, importedTitle, attachmentID, fetchedAt, usedPoster, posterPath, posterFetchedAt); err != nil {
		return false, nil, err
	}
	var oldFiles []string
	if oldAttachment.Valid && (!attachmentID.Valid || oldAttachment.UUID != attachmentID.UUID) {
		row, err := q.DeleteAttachment(ctx, sqlc.DeleteAttachmentParams{ID: oldAttachment.UUID, WorkspaceID: s.workspaceID})
		if err != nil {
			return false, nil, fmt.Errorf("delete replaced lookup attachment: %w", err)
		}
		oldFiles = []string{row.StorageKey, row.ThumbnailStorageKey}
	}
	return usedPoster, oldFiles, nil
}

func (s *DBStore) saveProvenance(ctx context.Context, q *sqlc.Queries, itemID uuid.UUID, p ApplyParams, originalTitle string, snapshots map[string]any, importedTitle *string, attachmentID uuid.NullUUID, now time.Time, newPoster bool, retainedPosterPath string, posterFetchedAt *time.Time) error {
	// streaming.country comes from workspace settings and is required whenever
	// any streaming leaf remains. It is structural context rather than TMDB
	// content, so retaining it lets a user-edited platform leaf survive expiry.
	delete(snapshots, "streaming.country")
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
		refresh := addMonthsClamped(*posterFetchedAt, 5)
		expires := addMonthsClamped(*posterFetchedAt, 6)
		posterRefresh = &refresh
		posterExpiry = &expires
	} else {
		attachmentID = uuid.NullUUID{}
		posterFetchedAt = nil
	}
	if err := q.UpsertTMDBImport(ctx, sqlc.UpsertTMDBImportParams{
		WorkspaceID: s.workspaceID, ItemID: itemID, TmdbID: int32(p.TMDBID), Kind: p.Kind, //nolint:gosec // validated positive TMDB id
		ImportedFields: fieldsJSON, ImportedTitle: importedTitle, OriginalTitle: originalTitle,
		AttachmentID: attachmentID, PosterPath: posterPath, PosterFetchedAt: posterFetchedAt,
		PosterRefreshAfter: posterRefresh, PosterExpiresAt: posterExpiry,
		FetchedAt: now, RefreshAfter: addMonthsClamped(now, 5), ExpiresAt: addMonthsClamped(now, 6),
	}); err != nil {
		return fmt.Errorf("save TMDB import: %w", err)
	}
	return nil
}

func readImportTx(ctx context.Context, q *sqlc.Queries, itemID, workspaceID uuid.UUID) (Record, bool, error) {
	r, err := q.GetTMDBImport(ctx, sqlc.GetTMDBImportParams{ItemID: itemID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("get TMDB import: %w", err)
	}
	fields := map[string]any{}
	if err := json.Unmarshal(r.ImportedFields, &fields); err != nil {
		return Record{}, false, fmt.Errorf("decode TMDB import fields: %w", err)
	}
	return Record{ItemID: r.ItemID, ItemVersion: int(r.ItemVersion), TMDBID: int(r.TmdbID), Kind: r.Kind,
		ImportedFields: fields, ImportedTitle: r.ImportedTitle, OriginalTitle: r.OriginalTitle,
		AttachmentID: nullUUIDPtr(r.AttachmentID), PosterPath: valueOrEmpty(r.PosterPath),
		PosterFetchedAt: r.PosterFetchedAt, FetchedAt: r.FetchedAt}, true, nil
}

func mergeMetadata(mode Mode, current, candidate map[string]any, old Record, hasOld bool) (map[string]any, map[string]any) {
	out := cloneLookupMap(current)
	flatCandidate := flatten(candidate)
	snapshots := map[string]any{}
	switch mode {
	case ModeRematch, ModeRefresh:
		if hasOld {
			removeUnchangedOwned(out, old.ImportedFields)
		}
		for path, value := range flatCandidate {
			setPath(out, path, value)
			snapshots[path] = value
		}
	case ModeOwned:
		if hasOld {
			for path, previous := range old.ImportedFields {
				currentValue, present := getPath(out, path)
				if !present || !equalValue(currentValue, previous) {
					continue
				}
				if fresh, ok := flatCandidate[path]; ok {
					setPath(out, path, fresh)
					snapshots[path] = fresh
				} else {
					deletePath(out, path)
				}
			}
		}
		for path, value := range flatCandidate {
			if _, alreadyOwned := snapshots[path]; alreadyOwned {
				continue
			}
			if _, present := getPath(out, path); !present {
				setPath(out, path, value)
				snapshots[path] = value
			}
		}
	default: // fill
		if hasOld {
			for path, previous := range old.ImportedFields {
				if currentValue, present := getPath(out, path); present && equalValue(currentValue, previous) {
					snapshots[path] = currentValue
				}
			}
		}
		for path, value := range flatCandidate {
			if _, present := getPath(out, path); !present {
				setPath(out, path, value)
				snapshots[path] = value
			}
		}
	}
	// The provider identity is part of every successful match, including fill.
	// Maintenance relies on this owned snapshot to detect user removal/opt-out.
	setPath(out, "tmdb_id", flatCandidate["tmdb_id"])
	snapshots["tmdb_id"] = flatCandidate["tmdb_id"]
	return out, snapshots
}

func mergeTitle(p ApplyParams, current string, old Record, hasOld bool) (string, *string, string) {
	title, _ := cleanLookupTitle(p.Title)
	original := p.OriginalTitle
	if original == "" {
		original = current
	}
	if hasOld {
		original = old.OriginalTitle
	}
	switch p.Mode {
	case ModeOwned:
		if hasOld && old.ImportedTitle != nil && current == *old.ImportedTitle {
			return title, &title, original
		}
		return current, nil, original
	case ModeFill:
		if hasOld && old.ImportedTitle != nil && current == *old.ImportedTitle {
			return current, old.ImportedTitle, original
		}
		return current, nil, original
	case ModeRematch, ModeRefresh:
		if hasOld && old.ImportedTitle != nil && current != *old.ImportedTitle {
			original = p.OriginalTitle
			if original == "" {
				original = current
			}
		}
		return title, &title, original
	default:
		return current, nil, original
	}
}

func removeUnchangedOwned(metadata map[string]any, snapshots map[string]any) {
	for path, previous := range snapshots {
		if current, ok := getPath(metadata, path); ok && equalValue(current, previous) {
			deletePath(metadata, path)
		}
	}
}

func (s *DBStore) preparePoster(p *images.Processed) (*preparedPoster, error) {
	if p == nil {
		return nil, nil
	}
	id := uuid.New()
	key, thumb := lookupStorageKeys(id, p.Ext)
	if _, err := s.store.Put(key, bytes.NewReader(p.Original)); err != nil {
		return nil, fmt.Errorf("store lookup poster: %w", err)
	}
	if _, err := s.store.Put(thumb, bytes.NewReader(p.Thumbnail)); err != nil {
		s.deleteFiles(context.Background(), key)
		return nil, fmt.Errorf("store lookup poster thumbnail: %w", err)
	}
	return &preparedPoster{id: id, key: key, thumbKey: thumb, processed: p}, nil
}

func (s *DBStore) insertAttachment(ctx context.Context, q *sqlc.Queries, p *preparedPoster) error {
	img := p.processed
	if err := q.InsertAttachment(ctx, sqlc.InsertAttachmentParams{
		ID: p.id, WorkspaceID: s.workspaceID, StorageKey: p.key, ThumbnailStorageKey: p.thumbKey,
		MediaType: img.MediaType, SizeBytes: int64(len(img.Original)), Width: int32(img.Width), Height: int32(img.Height), //nolint:gosec // Process enforces image bounds
		Sha256: img.SHA256[:], Now: s.clock.Now(),
	}); err != nil {
		return fmt.Errorf("insert lookup attachment: %w", err)
	}
	return nil
}

// discardPrepared protects the ambiguous-commit case: only remove prepared
// files after confirming no committed attachment row owns them. Any read error
// leaves them for the orphan sweeper.
func (s *DBStore) discardPrepared(ctx context.Context, p *preparedPoster) {
	_, err := sqlc.New(s.db).GetAttachment(ctx, sqlc.GetAttachmentParams{ID: p.id, WorkspaceID: s.workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		s.deleteFiles(ctx, p.key, p.thumbKey)
	}
}

func (s *DBStore) deleteFiles(ctx context.Context, keys ...string) {
	for _, key := range keys {
		if key == "" {
			continue
		}
		if err := s.store.Delete(key); err != nil {
			s.log.WarnContext(ctx, "lookup image cleanup failed; the sweeper will retry", "err", err)
		}
	}
}

func cleanLookupTitle(raw string) (string, error) {
	title := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(title); n < 1 || n > maxLookupTitleRunes {
		return title, apperr.Validation(apperr.FieldError{Field: "title", Message: "must be 1 to 300 characters"})
	}
	return title, nil
}

func cleanLookupWebsite(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	s := strings.TrimSpace(*raw)
	u, err := url.ParseRequestURI(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(s) > 2048 {
		return nil, apperr.Validation(apperr.FieldError{Field: "website", Message: "must be an http or https URL of at most 2048 characters"})
	}
	return &s, nil
}

func lookupStorageKeys(id uuid.UUID, ext string) (string, string) {
	s := id.String()
	base := "images/" + s[:2] + "/" + s[2:4] + "/" + s
	return base + "." + ext, base + ".thumb.jpg"
}

func flatten(in map[string]any) map[string]any {
	out := map[string]any{}
	var walk func(string, any)
	walk = func(prefix string, value any) {
		if nested, ok := value.(map[string]any); ok {
			for key, child := range nested {
				path := key
				if prefix != "" {
					path = prefix + "." + key
				}
				walk(path, child)
			}
			return
		}
		if prefix != "" {
			out[prefix] = value
		}
	}
	for key, value := range in {
		walk(key, value)
	}
	return out
}

func getPath(in map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	cur := in
	for i, part := range parts {
		value, ok := cur[part]
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return value, true
		}
		cur, ok = value.(map[string]any)
		if !ok {
			return nil, false
		}
	}
	return nil, false
}

func setPath(in map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	cur := in
	for _, part := range parts[:len(parts)-1] {
		next, ok := cur[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[part] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = value
}

func deletePath(in map[string]any, path string) {
	parts := strings.Split(path, ".")
	parents := []map[string]any{in}
	cur := in
	for _, part := range parts[:len(parts)-1] {
		next, ok := cur[part].(map[string]any)
		if !ok {
			return
		}
		cur = next
		parents = append(parents, cur)
	}
	delete(cur, parts[len(parts)-1])
	for i := len(parts) - 2; i >= 0; i-- {
		child, ok := parents[i][parts[i]].(map[string]any)
		if !ok || len(child) != 0 {
			break
		}
		delete(parents[i], parts[i])
	}
}

func snapshotPresent(metadata map[string]any, requested map[string]any) map[string]any {
	out := map[string]any{}
	for path := range requested {
		if value, ok := getPath(metadata, path); ok {
			out[path] = value
		}
	}
	return out
}

func cloneLookupMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	b, _ := json.Marshal(in)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}

func equalValue(a, b any) bool {
	na, nb := normalizeValue(a), normalizeValue(b)
	return reflect.DeepEqual(na, nb)
}

func normalizeValue(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func sameJSON(a, b []byte) bool {
	var av, bv any
	return json.Unmarshal(a, &av) == nil && json.Unmarshal(b, &bv) == nil && reflect.DeepEqual(av, bv)
}

func imageURLChanged(a, b *string) bool {
	if a == nil || b == nil {
		return a != nil || b != nil
	}
	return *a != *b
}

func nullUUIDPtr(id uuid.NullUUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	return &id.UUID
}

func valueOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
