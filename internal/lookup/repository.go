package lookup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// DB is satisfied by *pgxpool.Pool.
type DB interface {
	database.TxBeginner
	sqlc.DBTX
}

// Repo is the data lookup needs. Methods take the workspace from the context
// (scope). Outside InTx each call is one statement on the pool; inside InTx the
// calls share one transaction. Errors are returned as the driver gave them,
// except a missing row, which is apperr.NotFound where the rule needs one.
// Declared here, next to its Postgres implementation, so the apply and maintenance
// rules run against a fake in tests.
type Repo interface {
	// InTx runs fn in one transaction: commit on nil, rollback on error.
	InTx(ctx context.Context, fn func(Repo) error) error

	// ListType returns the type of an active list (nil for a list with no type). NotFound when missing.
	ListType(ctx context.Context, listID uuid.UUID) (*string, error)
	// Import returns the provenance of an item, with the item's current version. ok is false when there is none.
	Import(ctx context.Context, itemID uuid.UUID) (rec Record, ok bool, err error)
	// LockTree takes the workspace tree lock for the rest of the transaction.
	LockTree(ctx context.Context) error
	// NextSortOrder returns the sort order after the last item of the list.
	NextSortOrder(ctx context.Context, listID uuid.UUID) (int32, error)
	// ListTitles returns the titles of the active items of a list (F-026 duplicate_title).
	ListTitles(ctx context.Context, listID uuid.UUID) ([]string, error)
	// LockItem locks an active item (FOR UPDATE) with its list type. NotFound when missing.
	LockItem(ctx context.Context, itemID uuid.UUID) (lockedItem, error)
	// InsertItem creates an item and returns its id.
	InsertItem(ctx context.Context, in newItem) (uuid.UUID, error)
	// UpdateItem writes the title, metadata and image columns of an item and bumps its version.
	UpdateItem(ctx context.Context, itemID uuid.UUID, u itemUpdate) error
	// SaveImport upserts the provenance row of an item.
	SaveImport(ctx context.Context, in importSave) error

	// InsertAttachment stores a new attachment row created at now. SEAM: moves to images.
	InsertAttachment(ctx context.Context, a images.Attachment, now time.Time) error
	// AttachmentExists reports whether an attachment row exists. SEAM: moves to images.
	AttachmentExists(ctx context.Context, id uuid.UUID) (bool, error)
	// DeleteAttachment removes an attachment row and returns its file keys. SEAM: moves to images.
	DeleteAttachment(ctx context.Context, id uuid.UUID) (images.FileKeys, error)

	// ScheduledItems lists up to limit movie and TV items due for the weekly TMDB refresh (D-123):
	// fetched before fetchedBefore, or never fetched (a typed tmdb_id), oldest first.
	ScheduledItems(ctx context.Context, fetchedBefore time.Time, limit int32) ([]scheduledItem, error)
	// ListItems returns the active items of a list in refresh order (the list refresh, refreshListLookups).
	ListItems(ctx context.Context, listID uuid.UUID) ([]listedItem, error)
}

// lockedItem is an item row read under FOR UPDATE.
type lockedItem struct {
	Version           int
	Title             string
	Type              *string
	Metadata          []byte
	ImageURL          *string
	ImageAttachmentID uuid.NullUUID
}

// newItem is an item created by an import. Price is validated by the caller.
type newItem struct {
	ListID            uuid.UUID
	Title             string
	Website           *string
	Price             *items.Price
	Metadata          []byte
	ImageAttachmentID uuid.NullUUID
	SortOrder         int32
	Now               time.Time
}

// itemUpdate is the visible state an import writes to an existing item.
type itemUpdate struct {
	Title             string
	Metadata          []byte
	ImageURL          *string
	ImageAttachmentID uuid.NullUUID
	Now               time.Time
}

// importSave is one lookup_imports row. Deadlines are nil for Open Library (D-095);
// TMDB rows carry the legacy deadlines (see legacyDeadlines).
type importSave struct {
	ItemID          uuid.UUID
	Provider        string
	SourceID        string
	Type            string
	ImportedFields  []byte
	ImportedTitle   *string
	OriginalTitle   string
	AttachmentID    uuid.NullUUID
	PosterPath      *string
	PosterFetchedAt *time.Time
	// PosterRefreshAfter and PosterExpiresAt are the poster deadlines.
	PosterRefreshAfter *time.Time
	PosterExpiresAt    *time.Time
	FetchedAt          time.Time
	RefreshAfter       *time.Time
	ExpiresAt          *time.Time
}

// scheduledItem is a movie or TV item due for the weekly refresh, with the
// version the pass read.
type scheduledItem struct {
	ItemID      uuid.UUID
	ItemVersion int
}

// listedItem is an active item of a list, with the version and metadata the list
// refresh needs to decide and to refresh it.
type listedItem struct {
	ItemID      uuid.UUID
	ItemVersion int
	Metadata    []byte
}

// Postgres implements Repo on a pool, and runs InTx on one transaction.
type Postgres struct {
	db DB
	repo
}

// NewPostgres returns the Postgres repository of lookup.
func NewPostgres(db DB) *Postgres {
	return &Postgres{db: db, repo: repo{q: sqlc.New(db)}}
}

// InTx runs fn in one transaction: commit on nil, rollback on error.
func (p *Postgres) InTx(ctx context.Context, fn func(Repo) error) error {
	return database.InTx(ctx, p.db, func(q *sqlc.Queries) error {
		return fn(&repo{q: q})
	})
}

var _ Repo = (*Postgres)(nil)

// repo runs the queries on one connection: the pool, or the transaction of InTx.
type repo struct{ q *sqlc.Queries }

// InTx on a transaction-bound repo runs fn inside the same transaction.
func (r *repo) InTx(ctx context.Context, fn func(Repo) error) error {
	return fn(r)
}

func (r *repo) ListType(ctx context.Context, listID uuid.UUID) (*string, error) {
	typ, err := r.q.GetLookupListType(ctx, sqlc.GetLookupListTypeParams{ID: listID, WorkspaceID: scope.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.NotFound("list")
	}
	return typ, err
}

func (r *repo) Import(ctx context.Context, itemID uuid.UUID) (Record, bool, error) {
	row, err := r.q.GetLookupImport(ctx, sqlc.GetLookupImportParams{ItemID: itemID, WorkspaceID: scope.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("get lookup import: %w", err)
	}
	fields := map[string]any{}
	if err := json.Unmarshal(row.ImportedFields, &fields); err != nil {
		return Record{}, false, fmt.Errorf("decode lookup import fields: %w", err)
	}
	return Record{
		ItemID: row.ItemID, ItemVersion: int(row.ItemVersion), Provider: row.Provider, SourceID: row.SourceID, Type: row.Type,
		ImportedFields: fields, ImportedTitle: row.ImportedTitle, OriginalTitle: row.OriginalTitle,
		AttachmentID: nullUUIDPtr(row.AttachmentID), PosterPath: valueOrEmpty(row.PosterPath),
		PosterFetchedAt: row.PosterFetchedAt, FetchedAt: row.FetchedAt,
	}, true, nil
}

func (r *repo) LockTree(ctx context.Context) error {
	return r.q.LockTree(ctx, scope.WorkspaceID(ctx))
}

func (r *repo) NextSortOrder(ctx context.Context, listID uuid.UUID) (int32, error) {
	return r.q.NextItemSortOrder(ctx, sqlc.NextItemSortOrderParams{ListID: listID, WorkspaceID: scope.WorkspaceID(ctx)})
}

func (r *repo) ListTitles(ctx context.Context, listID uuid.UUID) ([]string, error) {
	return r.q.ListActiveItemTitles(ctx, sqlc.ListActiveItemTitlesParams{ListID: listID, WorkspaceID: scope.WorkspaceID(ctx)})
}

func (r *repo) LockItem(ctx context.Context, itemID uuid.UUID) (lockedItem, error) {
	row, err := r.q.LockLookupItem(ctx, sqlc.LockLookupItemParams{ID: itemID, WorkspaceID: scope.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedItem{}, apperr.NotFound("item")
	}
	if err != nil {
		return lockedItem{}, err
	}
	return lockedItem{
		Version: int(row.Version), Title: row.Title, Type: row.Type, Metadata: row.Metadata,
		ImageURL: row.ImageUrl, ImageAttachmentID: row.ImageAttachmentID,
	}, nil
}

func (r *repo) InsertItem(ctx context.Context, in newItem) (uuid.UUID, error) {
	priceMin, priceMax, currency := items.PriceParts(in.Price)
	return r.q.InsertLookupItem(ctx, sqlc.InsertLookupItemParams{
		WorkspaceID: scope.WorkspaceID(ctx), ListID: in.ListID, Title: in.Title, WebsiteUrl: in.Website,
		PriceMin: priceMin, PriceMax: priceMax, Currency: currency, Metadata: in.Metadata,
		ImageAttachmentID: in.ImageAttachmentID, SortOrder: in.SortOrder, Now: in.Now,
	})
}

func (r *repo) UpdateItem(ctx context.Context, itemID uuid.UUID, u itemUpdate) error {
	return r.q.UpdateLookupItem(ctx, sqlc.UpdateLookupItemParams{
		Title: u.Title, Metadata: u.Metadata, ImageUrl: u.ImageURL, ImageAttachmentID: u.ImageAttachmentID,
		Now: u.Now, ID: itemID, WorkspaceID: scope.WorkspaceID(ctx),
	})
}

func (r *repo) SaveImport(ctx context.Context, in importSave) error {
	return r.q.UpsertLookupImport(ctx, sqlc.UpsertLookupImportParams{
		WorkspaceID: scope.WorkspaceID(ctx), ItemID: in.ItemID, Provider: in.Provider, SourceID: in.SourceID, Type: in.Type,
		ImportedFields: in.ImportedFields, ImportedTitle: in.ImportedTitle, OriginalTitle: in.OriginalTitle,
		AttachmentID: in.AttachmentID, PosterPath: in.PosterPath, PosterFetchedAt: in.PosterFetchedAt,
		PosterRefreshAfter: in.PosterRefreshAfter, PosterExpiresAt: in.PosterExpiresAt,
		FetchedAt: in.FetchedAt, RefreshAfter: in.RefreshAfter, ExpiresAt: in.ExpiresAt,
	})
}

func (r *repo) InsertAttachment(ctx context.Context, a images.Attachment, now time.Time) error {
	return r.q.InsertAttachment(ctx, sqlc.InsertAttachmentParams{
		ID: a.ID, WorkspaceID: scope.WorkspaceID(ctx), StorageKey: a.StorageKey, ThumbnailStorageKey: a.ThumbnailStorageKey,
		OriginalFilename: a.OriginalFilename, MediaType: a.MediaType, SizeBytes: a.SizeBytes,
		Width: int32(a.Width), Height: int32(a.Height), //nolint:gosec // Process enforces image bounds
		Sha256: a.SHA256, Now: now,
	})
}

func (r *repo) AttachmentExists(ctx context.Context, id uuid.UUID) (bool, error) {
	_, err := r.q.GetAttachment(ctx, sqlc.GetAttachmentParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r *repo) DeleteAttachment(ctx context.Context, id uuid.UUID) (images.FileKeys, error) {
	row, err := r.q.DeleteAttachment(ctx, sqlc.DeleteAttachmentParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return images.FileKeys{}, err
	}
	return images.FileKeys{StorageKey: row.StorageKey, ThumbnailStorageKey: row.ThumbnailStorageKey}, nil
}

func (r *repo) ListItems(ctx context.Context, listID uuid.UUID) ([]listedItem, error) {
	rows, err := r.q.ListLookupItems(ctx, sqlc.ListLookupItemsParams{WorkspaceID: scope.WorkspaceID(ctx), ListID: listID})
	if err != nil {
		return nil, err
	}
	out := make([]listedItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, listedItem{ItemID: row.ItemID, ItemVersion: int(row.ItemVersion), Metadata: row.Metadata})
	}
	return out, nil
}

func (r *repo) ScheduledItems(ctx context.Context, fetchedBefore time.Time, limit int32) ([]scheduledItem, error) {
	rows, err := r.q.ListScheduledTMDBItems(ctx, sqlc.ListScheduledTMDBItemsParams{
		WorkspaceID: scope.WorkspaceID(ctx), FetchedBefore: fetchedBefore, BatchLimit: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]scheduledItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, scheduledItem{ItemID: row.ItemID, ItemVersion: int(row.ItemVersion)})
	}
	return out, nil
}
