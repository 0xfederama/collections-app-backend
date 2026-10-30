package items

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
)

// The wire types below are declared outside handler.go so that the layer rules
// keep a feature's handler identifiers to itself. Their JSON keys are in
// alphabetical order, as the generated contract wrote them.

// ItemJSON is the wire shape of an item (published as the schema "Item"). Path is
// present on getItem only.
type ItemJSON struct {
	ContentVersion int                    `json:"content_version"`
	CreatedAt      time.Time              `json:"created_at"`
	ID             uuid.UUID              `json:"id" format:"uuid"`
	Image          any                    `json:"image" nullable:"true" doc:"An item image, either an external URL or an uploaded attachment. null when the item has no image."`
	ListID         uuid.UUID              `json:"list_id" format:"uuid"`
	Metadata       map[string]any         `json:"metadata" doc:"Validated against the list's type; its shape depends on the type."`
	Path           *[]ItemPathSegmentJSON `json:"path,omitempty" doc:"Ancestor folders and the list. Present on getItem only."`
	Price          *PriceJSON             `json:"price"`
	SortOrder      int                    `json:"sort_order"`
	Title          string                 `json:"title"`
	Type           *string                `json:"type" nullable:"true" doc:"null when the list has no type."`
	UpdatedAt      time.Time              `json:"updated_at"`
	Version        int                    `json:"version"`
	Website        *string                `json:"website" nullable:"true"`
}

// ItemSummaryJSON is an item row of listItems (published as "ItemSummary"): the
// same metadata as the full item, and the image reduced.
type ItemSummaryJSON struct {
	ContentVersion int               `json:"content_version"`
	CreatedAt      time.Time         `json:"created_at"`
	ID             uuid.UUID         `json:"id" format:"uuid"`
	Image          *ImageSummaryJSON `json:"image"`
	ListID         uuid.UUID         `json:"list_id" format:"uuid"`
	Metadata       map[string]any    `json:"metadata"`
	Price          *PriceJSON        `json:"price"`
	SortOrder      int               `json:"sort_order"`
	Title          string            `json:"title"`
	Type           *string           `json:"type" nullable:"true"`
	UpdatedAt      time.Time         `json:"updated_at"`
	Version        int               `json:"version"`
	Website        *string           `json:"website" nullable:"true"`
}

// ItemPageJSON is the listItems answer (published as "ItemPage").
type ItemPageJSON struct {
	Data []ItemSummaryJSON `json:"data"`
	Page PageJSON          `json:"page"`
}

// PageJSON is the paging of a list answer (published as "Page").
type PageJSON struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	Total  int `json:"total" doc:"Total rows matching, ignoring limit and offset."`
}

// ItemPathSegmentJSON is one ancestor of an item (published as "ItemPathSegment": the
// lists package publishes "PathSegment" with the same shape, and the registry needs unique names).
type ItemPathSegmentJSON struct {
	ID   uuid.UUID `json:"id" format:"uuid"`
	Name string    `json:"name"`
	Type string    `json:"type" enum:"folder,list"`
}

// PriceJSON is a price in responses: always min, max and currency (published as "Price").
type PriceJSON struct {
	Currency string  `json:"currency"`
	Max      *string `json:"max" nullable:"true"`
	Min      *string `json:"min" nullable:"true"`
}

// PriceInput is a price in requests: a single amount or a range (published as "PriceInput").
// Money is a decimal string (D-062). The rules are the service's, so no field is required here.
type PriceInput struct {
	Amount   *string `json:"amount,omitempty"`
	Currency string  `json:"currency,omitempty"`
	Max      *string `json:"max,omitempty"`
	Min      *string `json:"min,omitempty"`
}

// ImageSummaryJSON is the image of a list row (published as "ImageSummary").
type ImageSummaryJSON struct {
	ThumbnailURL *string `json:"thumbnail_url,omitempty" doc:"Present for uploads only."`
	URL          string  `json:"url"`
}

// UploadImageJSON is an uploaded image (published as "UploadImage").
type UploadImageJSON struct {
	AttachmentID     uuid.UUID `json:"attachment_id" format:"uuid"`
	Height           int       `json:"height"`
	OriginalFilename *string   `json:"original_filename" nullable:"true"`
	Source           string    `json:"source" enum:"upload"`
	ThumbnailURL     string    `json:"thumbnail_url"`
	URL              string    `json:"url"`
	Width            int       `json:"width"`
}

// URLImageJSON is an image given by its URL (published as "UrlImage").
type URLImageJSON struct {
	Source string `json:"source" enum:"url"`
	URL    string `json:"url"`
}

// CreateItemRequest is the body of createItem. The lookup fields are read by the
// lookup path only; the type of the item is its list's.
type CreateItemRequest struct {
	Title    string         `json:"title,omitempty"`
	Lookup   *bool          `json:"lookup,omitempty" doc:"For movie, TV and book lists, run lookup during creation. Ignored on other types."`
	TmdbID   *int           `json:"tmdb_id,omitempty" doc:"Optional selected TMDB suggestion. Used only when lookup runs on a movie or TV item."`
	SourceID *string        `json:"source_id,omitempty" doc:"Optional selected Open Library work id. Used only when lookup runs on a book item."`
	Price    *PriceInput    `json:"price,omitempty"`
	Website  *string        `json:"website,omitempty" nullable:"true"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// UpdateItemRequest is the body of updateItem. Absent fields stay as they are; the
// nullable ones tell null (clear) from absent with httpserver.Optional.
type UpdateItemRequest struct {
	Title    *string                         `json:"title,omitempty"`
	Price    httpserver.Optional[PriceInput] `json:"price,omitempty"`
	Website  httpserver.Optional[string]     `json:"website,omitempty"`
	Metadata map[string]any                  `json:"metadata,omitempty"`
}

// MoveItemRequest is the body of moveItem. list_id is checked by the handler, so
// an absent one gets the same 422 as before.
type MoveItemRequest struct {
	ListID uuid.UUID `json:"list_id,omitempty" format:"uuid"`
}

// ReorderItemsRequest is the body of reorderListItems.
type ReorderItemsRequest struct {
	IDs []uuid.UUID `json:"ids,omitempty" format:"uuid"`
}

// pageOf is the paging of a page of rows.
func pageOf(p ItemPage) PageJSON {
	return PageJSON{Limit: p.Limit, Offset: p.Offset, Total: p.Total}
}

// itemJSON is the wire shape of it. Path is set only when the item has one (getItem).
func itemJSON(it Item) (ItemJSON, error) {
	metadata, err := metadataOf(it.Metadata)
	if err != nil {
		return ItemJSON{}, err
	}
	out := ItemJSON{
		ContentVersion: it.ContentVersion, CreatedAt: it.CreatedAt, ID: it.ID, Image: imageJSON(it.Image),
		ListID: it.ListID, Metadata: metadata, Price: priceJSON(it.Price), SortOrder: it.SortOrder,
		Title: it.Title, Type: it.Type, UpdatedAt: it.UpdatedAt, Version: it.Version, Website: it.Website,
	}
	if it.Path != nil {
		path := make([]ItemPathSegmentJSON, 0, len(it.Path))
		for _, p := range it.Path {
			path = append(path, ItemPathSegmentJSON{ID: p.ID, Name: p.Name, Type: p.Type})
		}
		out.Path = &path
	}
	return out, nil
}

// itemSummaryJSON is the wire shape of a list row. The stored metadata is validated
// JSON written by this service; anything unreadable degrades to {}.
func itemSummaryJSON(it Item) ItemSummaryJSON {
	metadata, err := metadataOf(it.Metadata)
	if err != nil {
		metadata = map[string]any{}
	}
	return ItemSummaryJSON{
		ContentVersion: it.ContentVersion, CreatedAt: it.CreatedAt, ID: it.ID, Image: imageSummaryJSON(it.Image),
		ListID: it.ListID, Metadata: metadata, Price: priceJSON(it.Price), SortOrder: it.SortOrder,
		Title: it.Title, Type: it.Type, UpdatedAt: it.UpdatedAt, Version: it.Version, Website: it.Website,
	}
}

// metadataOf decodes the stored metadata object; nothing stored is {}.
func metadataOf(raw json.RawMessage) (map[string]any, error) {
	out := map[string]any{}
	if len(raw) == 0 {
		return out, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode item metadata: %w", err)
	}
	if m != nil {
		out = m
	}
	return out, nil
}

// imageJSON is the full image: an upload or a URL, nil when there is none.
func imageJSON(img *Image) any {
	if img == nil {
		return nil
	}
	if img.Source == ImageSourceUpload {
		return UploadImageJSON{
			AttachmentID: img.AttachmentID, Height: img.Height, OriginalFilename: img.OriginalFilename,
			Source: ImageSourceUpload, ThumbnailURL: img.ThumbnailURL, URL: img.URL, Width: img.Width,
		}
	}
	return URLImageJSON{Source: ImageSourceURL, URL: img.URL}
}

// imageSummaryJSON is the reduced image of a list row, nil when there is none.
func imageSummaryJSON(img *Image) *ImageSummaryJSON {
	if img == nil {
		return nil
	}
	out := &ImageSummaryJSON{URL: img.URL}
	if img.Source == ImageSourceUpload {
		thumb := img.ThumbnailURL
		out.ThumbnailURL = &thumb
	}
	return out
}

// priceJSON is the wire shape of a price, nil when there is none.
func priceJSON(p *Price) *PriceJSON {
	if p == nil {
		return nil
	}
	return &PriceJSON{Currency: p.Currency, Max: p.Max, Min: p.Min}
}
