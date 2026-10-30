package trash

import (
	"time"

	"github.com/google/uuid"
)

// Root types of a trash batch (the `root_type` column).
const (
	TypeFolder = "folder"
	TypeList   = "list"
	TypeItem   = "item"
)

// PathSegment is one ancestor of a trashed root as it was when deleted.
type PathSegment struct {
	Type string    `json:"type"` // "folder" or "list"
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// Entry is one trash batch as the API shows it.
type Entry struct {
	ID                uuid.UUID
	RootType          string
	RootID            uuid.UUID
	RootName          string
	FormerPath        []PathSegment
	DeletedAt         time.Time
	Folders           int
	Lists             int
	Items             int
	RestorableInPlace bool
}

// Page is one page of trash entries, newest first.
type Page struct {
	Entries       []Entry
	Limit, Offset int
	Total         int
}

// Destination is the optional body of a restore. A folder or list root takes
// FolderID (nil = the workspace root; FolderSet tells absent from null), an
// item root takes ListID.
type Destination struct {
	FolderSet bool
	FolderID  *uuid.UUID
	ListID    *uuid.UUID
}

// Restored names the root that came back.
type Restored struct {
	RootType string
	RootID   uuid.UUID
}

// Batch is a trash batch row as the restore and purge need it. The former path is
// only read for the list (Entry).
type Batch struct {
	ID        uuid.UUID
	RootType  string
	RootID    uuid.UUID
	RootName  string
	DeletedAt time.Time
}

// FolderNode is one folder of the workspace in any state, the input of the walks.
type FolderNode struct {
	ID       uuid.UUID
	ParentID *uuid.UUID
	Name     string
	Trashed  bool
}

// FolderRow is the folder columns a delete or restore reads.
type FolderRow struct {
	ID       uuid.UUID
	Name     string
	ParentID *uuid.UUID
	Version  int
}

// ListRow is the list columns a delete or restore reads. Version is zero when the
// read did not lock the row.
type ListRow struct {
	ID       uuid.UUID
	Name     string
	FolderID *uuid.UUID
	Version  int
}

// ItemRow is the item columns a delete or restore reads.
type ItemRow struct {
	ID      uuid.UUID
	Title   string
	ListID  uuid.UUID
	Version int
}

// ParentState is the name and trashed flag of a folder or list, for the blocking
// parent of a restore.
type ParentState struct {
	Name    string
	Trashed bool
}

// AttachmentFile is an image attachment of a purged item with its two file keys.
type AttachmentFile struct {
	ID                  uuid.UUID
	StorageKey          string
	ThumbnailStorageKey string
}
