package items

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// DB is satisfied by *pgxpool.Pool: plain queries plus transactions.
type DB interface {
	database.TxBeginner
	sqlc.DBTX
}

// Repo is the item data the service needs. Methods work on active rows only and
// take the workspace from the context (scope). Rows map to the pure model in
// model.go; no rule lives here.
type Repo interface {
	// LockTree serializes structural writes of the workspace until the transaction ends (D-060).
	LockTree(ctx context.Context) error
	// Get returns an active item with its list type and attachment dimensions. ContentVersion is 0:
	// the service fills it with MaxContentVersions. Path is not set (see PathOf).
	Get(ctx context.Context, id uuid.UUID) (Item, error)
	// GetForUpdate locks the item row (FOR UPDATE, no joins). Only the stored columns are set:
	// Type, Image and ContentVersion stay zero.
	GetForUpdate(ctx context.Context, id uuid.UUID) (Item, error)
	// ListByList returns the active items of a list in sort_order, id order (same fields as Get).
	ListByList(ctx context.Context, listID uuid.UUID) ([]Item, error)
	// Insert writes a new item from it (ListID, Title, Price, Website, Metadata, SortOrder) and returns its id.
	Insert(ctx context.Context, it Item, now time.Time) (uuid.UUID, error)
	// Update writes the editable columns of the active item it.ID (price, website and metadata included)
	// and bumps its version. Image columns are not touched.
	Update(ctx context.Context, it Item, now time.Time) error
	// Move puts an active item at the end of another list: its list and sort order change, version bumps.
	Move(ctx context.Context, id, listID uuid.UUID, sortOrder int, now time.Time) error
	// SetSortOrder sets the position of one active item. The version stays (DATA_MODEL §7).
	SetSortOrder(ctx context.Context, id uuid.UUID, sortOrder int, now time.Time) error
	// NextSortOrder is the sort order after the last active item of a list.
	NextSortOrder(ctx context.Context, listID uuid.UUID) (int, error)
	// MaxContentVersions maps each item of ids that has saved content to its highest version.
	MaxContentVersions(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]int, error)
	// ListInfo returns an active list's name, folder, sort mode, type and icon. NotFound otherwise.
	ListInfo(ctx context.Context, listID uuid.UUID) (ListInfo, error)
	// PathOf returns the folders above an active list (root first), then the list itself.
	PathOf(ctx context.Context, listID uuid.UUID) ([]PathSegment, error)
}

// ListInfo is what an item operation needs from its list.
type ListInfo struct {
	ID        uuid.UUID
	Name      string
	FolderID  *uuid.UUID
	SortMode  string
	Type      *string
	Icon      *string
	IconColor *string
}

// Postgres implements the repository on a pool.
type Postgres struct{ db DB }

func NewPostgres(db DB) *Postgres { return &Postgres{db: db} }

// The reads below run outside a transaction: one plain query each on the pool,
// with the same mapping as the Repo methods of the same name.

func (p *Postgres) reads() *repo { return &repo{q: sqlc.New(p.db)} }

func (p *Postgres) Get(ctx context.Context, id uuid.UUID) (Item, error) {
	return p.reads().Get(ctx, id)
}

func (p *Postgres) ListByList(ctx context.Context, listID uuid.UUID) ([]Item, error) {
	return p.reads().ListByList(ctx, listID)
}

func (p *Postgres) ListInfo(ctx context.Context, listID uuid.UUID) (ListInfo, error) {
	return p.reads().ListInfo(ctx, listID)
}

func (p *Postgres) PathOf(ctx context.Context, listID uuid.UUID) ([]PathSegment, error) {
	return p.reads().PathOf(ctx, listID)
}

func (p *Postgres) MaxContentVersions(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]int, error) {
	return p.reads().MaxContentVersions(ctx, ids)
}

// InTx runs fn in one transaction: commit on nil, rollback on error.
func (p *Postgres) InTx(ctx context.Context, fn func(Repo) error) error {
	return database.InTx(ctx, p.db, func(q *sqlc.Queries) error {
		return fn(&repo{q: q})
	})
}

// repo runs the queries of one transaction.
type repo struct{ q *sqlc.Queries }

var _ Repo = (*repo)(nil)

func (r *repo) LockTree(ctx context.Context) error {
	return r.q.LockTree(ctx, scope.WorkspaceID(ctx))
}

func (r *repo) Get(ctx context.Context, id uuid.UUID) (Item, error) {
	row, err := r.q.ReadItem(ctx, sqlc.ReadItemParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return Item{}, notFoundOr(err, "item")
	}
	return readItem(row), nil
}

func (r *repo) GetForUpdate(ctx context.Context, id uuid.UUID) (Item, error) {
	row, err := r.q.LockItemRow(ctx, sqlc.LockItemRowParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return Item{}, notFoundOr(err, "item")
	}
	return Item{
		ID: row.ID, ListID: row.ListID, Title: row.Title,
		Price: rowPrice(row.PriceMin, row.PriceMax, row.Currency), Website: row.WebsiteUrl,
		Metadata: json.RawMessage(row.Metadata), SortOrder: int(row.SortOrder), Version: int(row.Version),
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func (r *repo) ListByList(ctx context.Context, listID uuid.UUID) ([]Item, error) {
	rows, err := r.q.ReadListItems(ctx, sqlc.ReadListItemsParams{ListID: listID, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	out := make([]Item, 0, len(rows))
	for _, row := range rows {
		out = append(out, readItem(sqlc.ReadItemRow(row)))
	}
	return out, nil
}

func (r *repo) Insert(ctx context.Context, it Item, now time.Time) (uuid.UUID, error) {
	minV, maxV, currency := PriceParts(it.Price)
	id, err := r.q.InsertItemRow(ctx, sqlc.InsertItemRowParams{
		WorkspaceID: scope.WorkspaceID(ctx), ListID: it.ListID, Title: it.Title,
		PriceMin: minV, PriceMax: maxV, Currency: currency, WebsiteUrl: it.Website,
		Metadata: metadataJSON(it.Metadata), SortOrder: int32(it.SortOrder), Now: now,
	})
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("insert item: %w", err)
	}
	return id, nil
}

func (r *repo) Update(ctx context.Context, it Item, now time.Time) error {
	minV, maxV, currency := PriceParts(it.Price)
	_, err := r.q.UpdateItemRow(ctx, sqlc.UpdateItemRowParams{
		Title: it.Title, PriceMin: minV, PriceMax: maxV, Currency: currency, WebsiteUrl: it.Website,
		Metadata: metadataJSON(it.Metadata), Now: now, ID: it.ID, WorkspaceID: scope.WorkspaceID(ctx),
	})
	if err != nil {
		return notFoundOr(err, "item")
	}
	return nil
}

func (r *repo) Move(ctx context.Context, id, listID uuid.UUID, sortOrder int, now time.Time) error {
	_, err := r.q.MoveItemRow(ctx, sqlc.MoveItemRowParams{
		ListID: listID, SortOrder: int32(sortOrder), Now: now, ID: id, WorkspaceID: scope.WorkspaceID(ctx),
	})
	if err != nil {
		return notFoundOr(err, "item")
	}
	return nil
}

func (r *repo) SetSortOrder(ctx context.Context, id uuid.UUID, sortOrder int, now time.Time) error {
	if err := r.q.SetItemRowSortOrder(ctx, sqlc.SetItemRowSortOrderParams{
		SortOrder: int32(sortOrder), Now: now, ID: id, WorkspaceID: scope.WorkspaceID(ctx),
	}); err != nil {
		return fmt.Errorf("set item sort order: %w", err)
	}
	return nil
}

func (r *repo) NextSortOrder(ctx context.Context, listID uuid.UUID) (int, error) {
	next, err := r.q.NextItemSortOrderRow(ctx, sqlc.NextItemSortOrderRowParams{ListID: listID, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return 0, fmt.Errorf("next item sort order: %w", err)
	}
	return int(next), nil
}

func (r *repo) MaxContentVersions(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]int, error) {
	out := make(map[uuid.UUID]int, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.q.MaxItemContentVersions(ctx, sqlc.MaxItemContentVersionsParams{WorkspaceID: scope.WorkspaceID(ctx), ItemIds: ids})
	if err != nil {
		return nil, fmt.Errorf("max content versions: %w", err)
	}
	for _, row := range rows {
		out[row.ItemID] = int(row.MaxVersion)
	}
	return out, nil
}

func (r *repo) ListInfo(ctx context.Context, listID uuid.UUID) (ListInfo, error) {
	row, err := r.q.ReadItemListInfo(ctx, sqlc.ReadItemListInfoParams{ID: listID, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return ListInfo{}, notFoundOr(err, "list")
	}
	return ListInfo{
		ID: row.ID, Name: row.Name, FolderID: ptrUUID(row.FolderID), SortMode: row.SortMode,
		Type: row.Type, Icon: row.Icon, IconColor: row.IconColor,
	}, nil
}

// PathOf loads the active folders once and walks the parents in Go, as the
// lists repository does. A missing or trashed folder ends the chain, and a
// repeated folder ends it too, so corrupt cycles terminate.
func (r *repo) PathOf(ctx context.Context, listID uuid.UUID) ([]PathSegment, error) {
	list, err := r.ListInfo(ctx, listID)
	if err != nil {
		return nil, err
	}
	path := []PathSegment{}
	if list.FolderID != nil {
		folders, err := r.q.ListActiveFolders(ctx, scope.WorkspaceID(ctx))
		if err != nil {
			return nil, fmt.Errorf("item path: list folders: %w", err)
		}
		path = folderPath(folders, *list.FolderID)
	}
	return append(path, PathSegment{Type: "list", ID: list.ID, Name: list.Name}), nil
}

// folderPath returns folderID and its ancestors among rows (active folders), root first.
func folderPath(rows []sqlc.Folder, folderID uuid.UUID) []PathSegment {
	byID := make(map[uuid.UUID]sqlc.Folder, len(rows))
	for _, f := range rows {
		byID[f.ID] = f
	}
	path := []PathSegment{}
	seen := make(map[uuid.UUID]bool, len(rows))
	id := folderID
	for {
		f, ok := byID[id]
		if !ok || seen[id] {
			break
		}
		seen[id] = true
		path = append(path, PathSegment{Type: "folder", ID: f.ID, Name: f.Name})
		if !f.ParentFolderID.Valid {
			break
		}
		id = f.ParentFolderID.UUID
	}
	slices.Reverse(path)
	return path
}

// readItem maps a joined item row (ReadItem, ReadListItems) to the model.
func readItem(r sqlc.ReadItemRow) Item {
	it := Item{
		ID: r.ID, ListID: r.ListID, Title: r.Title, Type: r.Type,
		Price: rowPrice(r.PriceMin, r.PriceMax, r.Currency), Website: r.WebsiteUrl,
		Metadata: json.RawMessage(r.Metadata), SortOrder: int(r.SortOrder), Version: int(r.Version),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	switch {
	case r.ImageAttachmentID.Valid:
		id := r.ImageAttachmentID.UUID
		img := &Image{
			Source: ImageSourceUpload, AttachmentID: id,
			URL: "/api/v1/attachments/" + id.String() + "/file", ThumbnailURL: "/api/v1/attachments/" + id.String() + "/thumbnail",
			OriginalFilename: r.ImageOriginalFilename,
		}
		if r.ImageWidth != nil {
			img.Width = int(*r.ImageWidth)
		}
		if r.ImageHeight != nil {
			img.Height = int(*r.ImageHeight)
		}
		it.Image = img
	case r.ImageUrl != nil:
		it.Image = &Image{Source: ImageSourceURL, URL: *r.ImageUrl}
	}
	return it
}

// rowPrice is the price of a row; nil when both bounds are NULL.
func rowPrice(minV, maxV pgtype.Numeric, currency *string) *Price {
	lo, hi := numericToMoney(minV), numericToMoney(maxV)
	if lo == nil && hi == nil {
		return nil
	}
	p := &Price{Min: lo, Max: hi}
	if currency != nil {
		p.Currency = *currency
	}
	return p
}

// metadataJSON is the stored form of metadata: nil means the empty object.
func metadataJSON(m json.RawMessage) []byte {
	if len(m) == 0 {
		return []byte("{}")
	}
	return m
}

// ptrUUID converts a nullable uuid to a pointer (nil when NULL).
func ptrUUID(id uuid.NullUUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	v := id.UUID
	return &v
}

// notFoundOr maps a missing row to apperr.NotFound and wraps any other error.
func notFoundOr(err error, resource string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound(resource)
	}
	return fmt.Errorf("%s: %w", resource, err)
}
