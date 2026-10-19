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
	"math"
	"math/big"
	"strconv"
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
	ID     uuid.UUID
	ListID uuid.UUID
	Title  string
	// Type is read-only: it is the type of the item's list (F-007); nil when the list has no type.
	Type    *string
	Image   *Image
	Price   *Price
	Website *string
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
// Icon and IconColor are the list's own, set only by the search paths (F012-B1).
type PathSegment struct {
	Type      string // "folder" or "list"
	ID        uuid.UUID
	Name      string
	Icon      *string
	IconColor *string
}

// ErrInvalidSort means a sort mode is neither a built-in mode nor a defined field of the list type.
var ErrInvalidSort = errors.New("items: sort is not available on this list")
var ErrInvalidFilter = errors.New("items: field filter is not valid for this list")

// ListParams are the query options of ListItems. Zero values mean the defaults.
type ListParams struct {
	Sort        string // manual, title, updated, price or a field key; "" = the list's sort_mode
	Q           string // case-insensitive title substring, plus director on movie lists
	FilterField string
	FilterValue string
	FilterMin   string
	FilterMax   string
	Limit       int
	Offset      int
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
	sortKey, sortType := "", ""
	// A list with no type has no definition: no price and no fields.
	var typeDefinition sqlc.TypeDefinition
	if list.Type != nil {
		typeDefinition, err = q.GetTypeDefinitionByKey(ctx, sqlc.GetTypeDefinitionByKeyParams{WorkspaceID: s.workspaceID, Key: *list.Type})
		if errors.Is(err, pgx.ErrNoRows) {
			return ItemPage{}, ErrInvalidSort
		}
		if err != nil {
			return ItemPage{}, fmt.Errorf("load list type definition: %w", err)
		}
	}
	if p.Sort == "price" && !typeDefinition.AllowsPrice {
		return ItemPage{}, ErrInvalidSort
	}
	if p.Sort != "manual" && p.Sort != "title" && p.Sort != "updated" && p.Sort != "price" {
		sortType, err = s.fieldType(ctx, q, list.Type, p.Sort)
		if errors.Is(err, ErrInvalidSort) {
			return ItemPage{}, ErrInvalidSort
		}
		if err != nil {
			return ItemPage{}, fmt.Errorf("load sort field definition: %w", err)
		}
		sortKey = p.Sort
	}
	filterType := ""
	if p.FilterField != "" {
		filterType, err = s.fieldType(ctx, q, list.Type, p.FilterField)
		if errors.Is(err, ErrInvalidSort) {
			return ItemPage{}, ErrInvalidFilter
		}
		if err != nil {
			return ItemPage{}, fmt.Errorf("load filter field definition: %w", err)
		}
		if !validFilter(filterType, p.FilterValue, p.FilterMin, p.FilterMax) {
			return ItemPage{}, ErrInvalidFilter
		}
	} else if p.FilterValue != "" || p.FilterMin != "" || p.FilterMax != "" {
		return ItemPage{}, ErrInvalidFilter
	}
	filterValue := p.FilterValue
	if filterType == "text" || filterType == "link" || filterType == "streaming" {
		filterValue = escapeLike(filterValue)
	}
	filter := sqlc.ListItemsParams{
		ListID: listID, WorkspaceID: s.workspaceID, Q: like, Sort: p.Sort,
		SortKey: sortKey, SortType: sortType, FilterKey: p.FilterField, FilterType: filterType,
		FilterValue: filterValue, FilterMinSet: p.FilterMin != "", FilterMin: p.FilterMin,
		FilterMaxSet: p.FilterMax != "", FilterMax: p.FilterMax,
		PageLimit: int32(p.Limit), PageOffset: int32(p.Offset), //nolint:gosec // bounded above
	}
	rows, err := q.ListItems(ctx, filter)
	if err != nil {
		return ItemPage{}, fmt.Errorf("list items: %w", err)
	}
	total, err := q.CountItems(ctx, sqlc.CountItemsParams{
		ListID: listID, WorkspaceID: s.workspaceID, Q: like, FilterKey: p.FilterField, FilterType: filterType,
		FilterValue: filterValue, FilterMinSet: p.FilterMin != "", FilterMin: p.FilterMin,
		FilterMaxSet: p.FilterMax != "", FilterMax: p.FilterMax,
	})
	if err != nil {
		return ItemPage{}, fmt.Errorf("count items: %w", err)
	}
	page := ItemPage{Items: make([]Item, 0, len(rows)), Limit: p.Limit, Offset: p.Offset, Total: int(total)}
	for _, r := range rows {
		page.Items = append(page.Items, itemFromRow(r))
	}
	return page, nil
}

func (s *Service) fieldType(ctx context.Context, q *sqlc.Queries, typ *string, key string) (string, error) {
	if typ == nil || key == "" || key == "manual" || key == "title" || key == "updated" || key == "price" {
		return "", ErrInvalidSort
	}
	def, err := q.GetTypeDefinitionByKey(ctx, sqlc.GetTypeDefinitionByKeyParams{WorkspaceID: s.workspaceID, Key: *typ})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidSort
	}
	if err != nil {
		return "", err
	}
	fields, err := q.ListTypeFields(ctx, sqlc.ListTypeFieldsParams{WorkspaceID: s.workspaceID, TypeID: def.ID})
	if err != nil {
		return "", err
	}
	for _, f := range fields {
		if f.Key == key {
			return f.FieldType, nil
		}
	}
	return "", ErrInvalidSort
}

func validFilter(fieldType, value, min, max string) bool {
	switch fieldType {
	case "text", "link", "streaming":
		return value != "" && min == "" && max == ""
	case "boolean":
		return (value == "true" || value == "false") && min == "" && max == ""
	case "number":
		return value == "" && (min != "" || max != "") && validNumber(min) && validNumber(max) && validNumberRange(min, max)
	case "date":
		return value == "" && (min != "" || max != "") && validDate(min) && validDate(max) && (min == "" || max == "" || min <= max)
	default:
		return false
	}
}

func validNumberRange(min, max string) bool {
	if min == "" || max == "" {
		return true
	}
	lo, ok := new(big.Rat).SetString(min)
	if !ok {
		return false
	}
	hi, ok := new(big.Rat).SetString(max)
	return ok && lo.Cmp(hi) <= 0
}

func validNumber(raw string) bool {
	if raw == "" {
		return true
	}
	v, err := strconv.ParseFloat(raw, 64)
	return err == nil && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func validDate(raw string) bool {
	if raw == "" {
		return true
	}
	_, err := time.Parse("2006-01-02", raw)
	return err == nil
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

// FromRecentRow maps a recent-items row to an Item (same read model as search).
func FromRecentRow(r sqlc.ListRecentItemsRow) Item { return itemFromRow(sqlc.ListItemsRow(r)) }

// EscapeLike is escapeLike for other packages that build LIKE patterns.
func EscapeLike(q string) string { return escapeLike(q) }

// itemFromRow is the one mapper from the read-model row to an Item.
func itemFromRow(r sqlc.ListItemsRow) Item {
	it := Item{
		ID: r.ID, ListID: r.ListID, Title: r.Title, Type: r.Type,
		Price: priceFromRow(r.PriceMin, r.PriceMax, r.Currency), Website: r.WebsiteUrl,
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
