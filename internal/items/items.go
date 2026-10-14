// Package items owns items: create, read, list, update, move and reorder
// (API.md §5 Items, DATA_MODEL §2 and §7). It knows nothing about HTTP: it
// returns typed errors from internal/apperr. Deleting items is the trash
// service's job (B-10), images are B-09, Markdown content is B-08.
//
// Reads and writes work on active rows only (deleted_at IS NULL); a trashed or
// missing item or list is a NotFound. Create, move and reorder take the same
// per-workspace tree lock as folders and lists (D-060, see lockTree).
//
// The read model (Item, built by itemFromRow) is shared by getItem, listItems
// and everything that writes an item, so later tasks reuse it unchanged.
package items

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/clock"
	"github.com/0xfederama/collections-app-backend/internal/database"
	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
)

// Page sizes of listItems (API.md §5).
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Sort orders of listItems.
const (
	SortManual  = "manual"
	SortTitle   = "title"
	SortUpdated = "updated"
)

// DB is satisfied by *pgxpool.Pool: plain queries plus transactions.
type DB interface {
	database.TxBeginner
	sqlc.DBTX
}

// Service is the items service of one workspace.
type Service struct {
	db          DB
	clock       clock.Clock
	workspaceID uuid.UUID
}

// New returns a Service bound to the single workspace.
func New(db DB, clk clock.Clock, workspaceID uuid.UUID) *Service {
	return &Service{db: db, clock: clk, workspaceID: workspaceID}
}

// Item is an item as the API shows it. Path is filled by Get only.
type Item struct {
	ID                 uuid.UUID
	ListID             uuid.UUID
	Title              string
	Kind               string
	Summary            *string
	Status             *string
	Image              *Image
	Price              *Price
	OfficialWebsiteURL *string
	// Metadata is the stored JSON object, already validated at write time.
	Metadata       json.RawMessage
	ContentVersion int
	SortOrder      int
	Version        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Path           []PathSegment
}

// Image is the item picture: an external URL or an uploaded attachment.
type Image struct {
	Source string // "url" or "upload"
	// URL is the external address, or the attachment file URL.
	URL string
	// The fields below are set for uploads only.
	AttachmentID     uuid.UUID
	ThumbnailURL     string
	Width, Height    int
	OriginalFilename *string
}

// Image sources.
const (
	ImageSourceURL    = "url"
	ImageSourceUpload = "upload"
)

// PathSegment is one ancestor of an item: its folders root first, then its list.
type PathSegment struct {
	Type string // "folder" or "list"
	ID   uuid.UUID
	Name string
}

// ListParams are the query options of ListItems. Zero values mean the defaults.
type ListParams struct {
	Sort   string // manual, title or updated; "" = the list's sort_mode
	Q      string // case-insensitive title substring; "" = no filter
	Limit  int
	Offset int
}

// ItemPage is one page of items and the total number of matches.
type ItemPage struct {
	Items         []Item
	Limit, Offset int
	Total         int
}

// Get returns an active item with its path.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Item, error) {
	return s.get(ctx, id, true)
}

func (s *Service) get(ctx context.Context, id uuid.UUID, withPath bool) (Item, error) {
	q := sqlc.New(s.db)
	row, err := q.GetItem(ctx, sqlc.GetItemParams{ID: id, WorkspaceID: s.workspaceID})
	if err != nil {
		return Item{}, notFoundOr(err, "item")
	}
	it := itemFromRow(sqlc.ListItemsRow(row))
	if withPath {
		if it.Path, err = s.path(ctx, q, it.ListID); err != nil {
			return Item{}, err
		}
	}
	return it, nil
}

// path lists the folders above a list (root first) and then the list itself.
func (s *Service) path(ctx context.Context, q *sqlc.Queries, listID uuid.UUID) ([]PathSegment, error) {
	list, err := q.GetActiveListForItems(ctx, sqlc.GetActiveListForItemsParams{ID: listID, WorkspaceID: s.workspaceID})
	if err != nil {
		return nil, notFoundOr(err, "list")
	}
	path := []PathSegment{}
	if list.FolderID.Valid {
		folders, err := q.FolderPath(ctx, sqlc.FolderPathParams{FolderID: list.FolderID.UUID, WorkspaceID: s.workspaceID})
		if err != nil {
			return nil, fmt.Errorf("item path: %w", err)
		}
		for _, f := range folders {
			path = append(path, PathSegment{Type: "folder", ID: f.ID, Name: f.Name})
		}
	}
	return append(path, PathSegment{Type: "list", ID: list.ID, Name: list.Name}), nil
}

// List returns one page of the active items of an active list. The default
// sort is the list's own sort_mode; ties break on id.
func (s *Service) List(ctx context.Context, listID uuid.UUID, p ListParams) (ItemPage, error) {
	q := sqlc.New(s.db)
	list, err := q.GetActiveListForItems(ctx, sqlc.GetActiveListForItemsParams{ID: listID, WorkspaceID: s.workspaceID})
	if err != nil {
		return ItemPage{}, notFoundOr(err, "list")
	}
	if p.Sort == "" {
		p.Sort = list.SortMode
	}
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	like := escapeLike(p.Q)
	rows, err := q.ListItems(ctx, sqlc.ListItemsParams{
		ListID: listID, WorkspaceID: s.workspaceID, Q: like, Sort: p.Sort,
		PageLimit: int32(p.Limit), PageOffset: int32(p.Offset), //nolint:gosec // bounded above
	})
	if err != nil {
		return ItemPage{}, fmt.Errorf("list items: %w", err)
	}
	total, err := q.CountItems(ctx, sqlc.CountItemsParams{ListID: listID, WorkspaceID: s.workspaceID, Q: like})
	if err != nil {
		return ItemPage{}, fmt.Errorf("count items: %w", err)
	}
	page := ItemPage{Items: make([]Item, 0, len(rows)), Limit: p.Limit, Offset: p.Offset, Total: int(total)}
	for _, r := range rows {
		page.Items = append(page.Items, itemFromRow(r))
	}
	return page, nil
}

// escapeLike makes q literal inside a LIKE pattern that uses `\` as escape.
func escapeLike(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
}

// lockTree serializes structural writes of the workspace until the transaction
// ends (D-060). Item create, move and reorder take it so they never race with
// a list or folder being trashed or moved; trash delete (B-10) takes it too.
func (s *Service) lockTree(ctx context.Context, q *sqlc.Queries) error {
	if err := q.LockTree(ctx, s.workspaceID); err != nil {
		return fmt.Errorf("lock tree: %w", err)
	}
	return nil
}

func (s *Service) now() time.Time { return s.clock.Now() }

// FromSearchRow maps a search row to an Item (search reuses this read model).
func FromSearchRow(r sqlc.SearchItemsRow) Item { return itemFromRow(sqlc.ListItemsRow(r)) }

// EscapeLike is escapeLike for other packages that build LIKE patterns.
func EscapeLike(q string) string { return escapeLike(q) }

// itemFromRow is the one mapper from the read-model row to an Item.
func itemFromRow(r sqlc.ListItemsRow) Item {
	it := Item{
		ID: r.ID, ListID: r.ListID, Title: r.Title, Kind: r.Kind, Summary: r.Summary, Status: r.Status,
		Price: priceFromRow(r.PriceMin, r.PriceMax, r.Currency), OfficialWebsiteURL: r.OfficialWebsiteUrl,
		Metadata: json.RawMessage(r.Metadata), ContentVersion: int(r.ContentVersion), SortOrder: int(r.SortOrder),
		Version: int(r.Version), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
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

func notFoundOr(err error, resource string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound(resource)
	}
	return fmt.Errorf("%s: %w", resource, err)
}

// sameSet reports whether got holds exactly the ids of want: same length, no
// duplicates, nothing foreign or missing.
func sameSet(want, got []uuid.UUID) bool {
	if len(want) != len(got) {
		return false
	}
	set := make(map[uuid.UUID]bool, len(want))
	for _, id := range want {
		set[id] = true
	}
	for _, id := range got {
		if !set[id] {
			return false
		}
		delete(set, id) // a repeated id finds nothing the second time
	}
	return len(set) == 0
}
