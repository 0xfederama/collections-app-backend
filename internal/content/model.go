// Package content owns the Markdown content of items: the current body, its
// saved versions, restore and retention (API.md §5 Content, DATA_MODEL §2
// item_content_versions, D-052). The current version of an item is the highest
// saved version (0 when it was never saved). Content versions are an ETag stream
// of their own ("c-N"): a save bumps items.updated_at but never items.version.
//
// The package is model.go (types), repository.go (the only file with sqlc),
// service.go (the rules, no HTTP, no SQL), handler.go (Huma operations) and
// wire.go (the JSON shapes).
package content

import (
	"time"

	"github.com/google/uuid"
)

// MaxBodyBytes is the largest Markdown body: 1 MiB of UTF-8 bytes (API.md §6).
const MaxBodyBytes = 1 << 20

// Content is one version of an item's Markdown. Version 0 is the empty,
// never-saved content: Body is "" and UpdatedAt is nil.
type Content struct {
	ItemID    uuid.UUID
	Version   int
	Body      string
	UpdatedAt *time.Time
	// AuthorUserID is who saved this version; nil for version 0 and for versions saved before authorship was recorded.
	AuthorUserID *uuid.UUID
}

// VersionInfo describes one saved version without its body.
type VersionInfo struct {
	Version   int
	SizeBytes int
	CreatedAt time.Time
	// AuthorUserID is nil when unknown.
	AuthorUserID *uuid.UUID
}

// Stored is a saved version with the SHA-256 of its body, which a save compares
// the new body against. Version 0 is the zero Stored with its ItemID set.
type Stored struct {
	Content
	SHA256 []byte
}
