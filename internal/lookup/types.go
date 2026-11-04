// Package lookup orchestrates metadata lookup (TMDB for movies and TV, Open
// Library for books) without exposing provider details to the items or HTTP
// packages. A Provider per item type is registered in Providers; provenance of
// every import is kept in lookup_imports.
package lookup

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
)

// Mode controls how imported values are merged with an item.
type Mode string

const (
	ModeCreate Mode = "create"
	// ModeCreateUntracked creates a book item from Hardcover data: fields and cover
	// are applied but no provenance row and no provider id are written (Hardcover
	// has no lookup_imports provider; the data is the user's own from then on).
	ModeCreateUntracked Mode = "create_untracked"
	ModeFill            Mode = "fill"
	ModeRematch         Mode = "rematch"
	// ModeRefresh is the API's refresh of a movie or TV item: the weekly rule (D-123),
	// on demand. The store only ever sees ModeScheduled for it.
	ModeRefresh Mode = "refresh"
	// ModeScheduled is the weekly maintenance refresh of a movie or TV item (D-123).
	// It overwrites every fetched field and the title, user edits included, and
	// keeps a user-supplied image. It is not an API mode.
	ModeScheduled Mode = "scheduled"
)

// Record is the provenance of an imported item: what was fetched, and the poster
// that came with it.
type Record struct {
	ItemID      uuid.UUID
	ItemVersion int
	// Provider and SourceID identify the match (the TMDB id or an Open Library work id).
	Provider       string
	SourceID       string
	Type           string
	ImportedFields map[string]any
	ImportedTitle  *string
	// OriginalTitle is the user title from before TMDB normalized it and is the
	// import normalized it. It is kept with the import; the weekly refresh does not change it.
	OriginalTitle string
	AttachmentID  *uuid.UUID
	PosterPath    string
	// PosterFetchedAt is when the poster was fetched, kept apart from FetchedAt
	// because a poster can be replaced on its own.
	PosterFetchedAt *time.Time
	FetchedAt       time.Time
}

// ApplyParams is a fully fetched and image-processed lookup candidate. Network
// work finishes before Store.Apply starts its transaction.
type ApplyParams struct {
	Mode            Mode
	ListID          uuid.UUID // create only
	ItemID          uuid.UUID // existing-item modes only
	ExpectedVersion int       // existing-item modes only
	Type            string
	CreateInput     *items.CreateInput // create only; explicit user values win

	// Provider and SourceID identify the match. The provider decides the id's
	// metadata key and whether its rows carry legacy deadlines (TMDB only, D-089/D-095).
	Provider      string
	SourceID      string
	Title         string
	OriginalTitle string
	// Fields is the provider metadata object. Store persists its leaf values as
	// dotted-path snapshots so nested user edits can be detected independently.
	Fields map[string]any
	// FillOnly values are set only by create and fill, where the item has no value
	// yet, and are not tracked as imported (see Details.FillOnly).
	FillOnly   map[string]any
	PosterPath string
	Poster     *images.Processed
	// RefreshPoster distinguishes "preserve the current image" from a checked
	// TMDB response with no poster. In scheduled mode the latter removes a
	// TMDB-sourced poster; manual modes still preserve every existing image.
	RefreshPoster bool
}

// Store owns the atomic item, attachment and provenance transaction. Apply
// locks and rechecks the target so network latency cannot overwrite a
// concurrent user edit. A prepared poster is attached only when the locked
// item has no image; otherwise the existing image is preserved.
type Store interface {
	ListType(ctx context.Context, listID uuid.UUID) (string, error)
	Read(ctx context.Context, itemID uuid.UUID) (Record, bool, error)
	Apply(ctx context.Context, p ApplyParams) (items.Item, error)
	// ListItems returns the active items of a list, for the list refresh.
	ListItems(ctx context.Context, listID uuid.UUID) ([]listedItem, error)
}

// addMonthsClamped applies calendar-month deadlines without rolling a date at
// the end of a long month into the following month.
func addMonthsClamped(t time.Time, months int) time.Time {
	first := time.Date(t.Year(), t.Month(), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location()).AddDate(0, months, 0)
	lastDay := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(t.Day(), lastDay), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}
