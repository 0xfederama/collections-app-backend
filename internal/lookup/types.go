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
	"github.com/0xfederama/collections-app-backend/internal/items/core"
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
	ModeRefresh         Mode = "refresh"
	// ModeOwned is the automatic retention refresh. It may replace only values
	// that still equal their last imported snapshots.
	ModeOwned Mode = "owned"
)

// Record is the provenance needed to refresh or expire imported data without
// removing values the user has edited since import.
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
	// fallback if an unchanged imported title expires.
	OriginalTitle string
	AttachmentID  *uuid.UUID
	PosterPath    string
	// PosterFetchedAt is separate from FetchedAt: refreshing metadata without
	// replacing the poster must not extend the poster's retention window.
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
	CreateInput     *core.CreateInput // create only; explicit user values win

	// Provider and SourceID identify the match. The provider decides the id's
	// metadata key and its retention (only TMDB has one, D-089/D-095).
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
	// RefreshFields is relevant to owned maintenance. False means a poster-only
	// refresh and must leave field snapshots, title provenance and FetchedAt
	// unchanged.
	RefreshFields bool
	// RefreshPoster distinguishes "preserve the current image" from a checked
	// TMDB response with no poster. In owned mode the latter removes an expired
	// lookup-owned poster; manual modes still preserve every existing image.
	RefreshPoster bool
}

// Store owns the atomic item, attachment and provenance transaction. Apply
// locks and rechecks the target so network latency cannot overwrite a
// concurrent user edit. A prepared poster is attached only when the locked
// item has no image; otherwise the existing image is preserved.
type Store interface {
	ListType(ctx context.Context, listID uuid.UUID) (string, error)
	Read(ctx context.Context, itemID uuid.UUID) (Record, bool, error)
	Apply(ctx context.Context, p ApplyParams) (core.Item, error)
}

// addMonthsClamped applies calendar-month retention without rolling a date at
// the end of a long month into the following month.
func addMonthsClamped(t time.Time, months int) time.Time {
	first := time.Date(t.Year(), t.Month(), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location()).AddDate(0, months, 0)
	lastDay := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(t.Day(), lastDay), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}
