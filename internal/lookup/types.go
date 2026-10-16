// Package lookup orchestrates TMDB metadata lookup without exposing provider
// details to the items or HTTP packages.
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
	ModeCreate  Mode = "create"
	ModeFill    Mode = "fill"
	ModeRematch Mode = "rematch"
	ModeRefresh Mode = "refresh"
	// ModeOwned is the automatic retention refresh. It may replace only values
	// that still equal their last imported snapshots.
	ModeOwned Mode = "owned"
)

// Record is the provenance needed to refresh or expire imported TMDB data
// without removing values the user has edited since import.
type Record struct {
	ItemID         uuid.UUID
	ItemVersion    int
	TMDBID         int
	Kind           string
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
	Kind            string
	CreateInput     *items.CreateInput // create only; explicit user values win

	TMDBID        int
	Title         string
	OriginalTitle string
	// Fields is the provider metadata object. Store persists its leaf values as
	// dotted-path snapshots so nested user edits can be detected independently.
	Fields     map[string]any
	PosterPath string
	Poster     *images.Processed
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
	ListKind(ctx context.Context, listID uuid.UUID) (string, error)
	Read(ctx context.Context, itemID uuid.UUID) (Record, bool, error)
	Apply(ctx context.Context, p ApplyParams) (items.Item, error)
}
