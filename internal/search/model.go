// Package search finds active items by title and lists the recently updated ones
// (API.md §5 Search, DATA_MODEL §6). The files are layered: model.go, the read
// model and the limits; service.go, the rules over a Store (trimming, paging, the
// path of each result); repository.go, the only file with SQL; handler.go and
// wire.go, the Huma operations.
//
// An item inside a trashed list or folder is itself trashed (batch cascade,
// D-004), so the queries only have to filter on the item. Paths are built in Go
// from the active folders and lists, loaded once per request.
package search

import (
	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/items"
)

// Limits of the q parameter (after trimming) and page sizes (API.md §5).
const (
	MaxQueryLen  = 200
	DefaultLimit = items.DefaultLimit
	MaxLimit     = items.MaxLimit

	// DefaultRecentLimit and MaxRecentLimit bound Recent (API.md §5, D-070).
	DefaultRecentLimit = 12
	MaxRecentLimit     = 50
)

// Params are the options of Search. Zero Limit means the default.
type Params struct {
	Q      string
	Limit  int
	Offset int
}

// Result is one match: the item and where it lives, root folder first, list last.
type Result struct {
	Item     items.Item
	TypeName *string // nil when the list has no type
	Path     []items.PathSegment
}

// Page is one page of results and the total number of matches.
type Page struct {
	Results       []Result
	Limit, Offset int
	Total         int
}

// Folder is an active folder, enough to walk a path. ParentID nil is the workspace root.
type Folder struct {
	ID       uuid.UUID
	Name     string
	ParentID *uuid.UUID
}

// List is an active list, enough to name the last path segment (with its icon),
// find its folder and show its type. TypeName is nil when the list has no type.
type List struct {
	ID        uuid.UUID
	Name      string
	FolderID  *uuid.UUID
	Icon      *string
	IconColor *string
	TypeName  *string
}
