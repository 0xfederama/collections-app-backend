// Package items owns items: create, read, list, update, move and reorder
// (API.md §5 Items, DATA_MODEL §2 and §7). The files are layered: model.go, the
// pure item model (with metadata.go, listquery.go and validate.go: the metadata,
// price and patch rules and the list query, no database and no HTTP); service.go,
// the rules over a Store; repository.go, the only file with SQL; handler.go and
// wire.go, the Huma operations. Other features reach items only through the
// interfaces declared in service.go. Deleting items is the trash service's job,
// images are internal/images, Markdown content is internal/content.
//
// Reads and writes work on active rows only (deleted_at IS NULL); a trashed or
// missing item or list is a NotFound. Create, move and reorder take the tree lock
// (D-060, Repo.LockTree).
package items

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
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

// Image sources.
const (
	ImageSourceURL    = "url"
	ImageSourceUpload = "upload"
)

// ErrInvalidSort means a sort mode is neither a built-in mode nor a defined field of the list type.
var ErrInvalidSort = errors.New("items: sort is not available on this list")

// ErrInvalidAvailability means an availability filter on a list whose type has no streaming data.
var ErrInvalidAvailability = errors.New("items: availability filter is only for movie and TV lists")

// ErrInvalidFilter means a field filter is not valid for the list's type.
var ErrInvalidFilter = errors.New("items: field filter is not valid for this list")

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
	// CreatedByID and UpdatedByID are nil when unknown (F-027 decision 12).
	CreatedByID *uuid.UUID
	UpdatedByID *uuid.UUID
	Path        []PathSegment
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

// PathSegment is one ancestor of an item: its folders root first, then its list.
// Icon and IconColor are the list's own, set only by the search paths (F012-B1).
type PathSegment struct {
	Type      string // "folder" or "list"
	ID        uuid.UUID
	Name      string
	Icon      *string
	IconColor *string
}

// Price is a price range in one currency. Min and Max are decimal strings with
// exactly two decimals ("1299.00", D-015); nil means that bound is unset.
// Amount is request-only (F-007): a single price, stored as Min = Max. Rows read
// from the database never set it.
type Price struct {
	Min, Max *string
	Amount   *string
	Currency string
}

// normalized folds Amount into Min and Max.
func (p Price) normalized() Price {
	if p.Amount != nil {
		p.Min, p.Max, p.Amount = p.Amount, p.Amount, nil
	}
	return p
}

// CreateInput is a new item. Its type is the type of the list; nil Metadata is {}.
type CreateInput struct {
	Title    string
	Price    *Price
	Website  *string
	Metadata map[string]any
}

// Optional is a PATCH field: Set tells absent from present, and a nil Value
// means an explicit null.
type Optional[T any] struct {
	Set   bool
	Value *T
}

// Patch is a partial item update; absent fields stay as they are. A nil
// Metadata is absent, an empty map replaces the metadata with {}.
type Patch struct {
	Title    *string
	Price    Optional[Price]
	Website  Optional[string]
	Metadata map[string]any
}

// ListParams are the query options of listItems. Zero values mean the defaults.
type ListParams struct {
	Sort        string // manual, title, updated, price or a field key; "" = the list's sort_mode
	Q           string // case-insensitive title substring, plus director on movie lists
	FilterField string
	FilterValue string
	FilterMin   string
	FilterMax   string
	// Availability is "", AvailabilityAvailable or AvailabilityIncluded (movie and TV lists only).
	Availability string
	Limit        int
	Offset       int
}

// Normalized applies the defaults: an empty Sort is defaultSort (the list's
// sort_mode), Limit 0 is DefaultLimit and above MaxLimit is MaxLimit, a
// negative Offset is 0. It is idempotent.
func (p ListParams) Normalized(defaultSort string) ListParams {
	if p.Sort == "" {
		p.Sort = defaultSort
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
	return p
}

// ItemPage is one page of items and the total number of matches.
type ItemPage struct {
	Items         []Item
	Limit, Offset int
	Total         int
}

// EscapeLike makes q literal inside a LIKE pattern that uses `\` as escape.
func EscapeLike(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
}

// builtinSort reports whether a sort mode is not a field key of the type.
func builtinSort(sort string) bool {
	return sort == SortManual || sort == SortTitle || sort == SortUpdated || sort == "price"
}
