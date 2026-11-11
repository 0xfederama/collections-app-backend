// Package lists owns lists: create, read, update, move and reorder (API.md §5,
// DATA_MODEL §2 and §7). It knows nothing about HTTP. Deleting lists is the
// trash service's job (B-10).
package lists

import (
	"time"

	"github.com/google/uuid"
)

// CreateInput is a new list. A nil Type is a list with no type. Empty DisplayMode / SortMode take the defaults.
type CreateInput struct {
	Name        string
	Type        *string
	FolderID    *uuid.UUID
	Description *string
	DisplayMode string
	SortMode    string
	// Icon and IconColor nil = the default icon (F012-B1).
	Icon      *string
	IconColor *string
}

// Patch is a partial list update; absent fields stay as they are.
type Patch struct {
	Name        *string
	Type        Optional[string] // a nil Value is no type
	Description Optional[string]
	DisplayMode *string
	SortMode    *string
	// Icon and IconColor (F012-B1): a nil Value clears them. A changed icon that is not built-in clears the colour.
	Icon      Optional[string]
	IconColor Optional[string]
}

// Optional is a PATCH field: Set tells absent from present, and a nil Value
// means an explicit null. (The HTTP layer has its own, with the schema.)
type Optional[T any] struct {
	Set   bool
	Value *T
}

// List is a list as the API shows it. FolderID nil means root. Path is filled
// by the service for GET only: the ancestor folders, root first.
type List struct {
	ID       uuid.UUID
	FolderID *uuid.UUID
	// OwnerID is set only on a top-level list (F-027): a list in a folder belongs to its top-level ancestor.
	OwnerID *uuid.UUID
	Name    string
	// Type is the one type of every item in the list (F-007); nil is a list with no type.
	Type        *string
	Description *string
	DisplayMode string
	SortMode    string
	// Icon and IconColor are nil for the default icon (F012-B1).
	Icon      *string
	IconColor *string
	SortOrder int
	ItemCount int
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
	// CreatedByID and UpdatedByID are nil when unknown (F-027 decision 12).
	CreatedByID *uuid.UUID
	UpdatedByID *uuid.UUID
	Path        []PathFolder
}

// PathFolder is one ancestor folder of a list.
type PathFolder struct {
	ID   uuid.UUID
	Name string
}

// TypeInfo is what the list rules read from a type definition.
type TypeInfo struct {
	Key              string
	AllowsPrice      bool
	DefaultSortField *string
}

// FieldInfo is one field of a type: the key a sort or a pin names, and its field type.
type FieldInfo struct {
	Key       string
	FieldType string
}
