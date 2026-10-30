package trash

// The wire types below are declared outside handler.go, as in the other features,
// so that the layer rules keep a feature's handler identifiers to itself. Their
// JSON keys are in alphabetical order, as the generated contract wrote them.

import (
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
)

// TrashEntryJSON is one trash batch (published as "TrashEntry").
type TrashEntryJSON struct {
	Counts struct {
		Folders int `json:"folders"`
		Items   int `json:"items"`
		Lists   int `json:"lists"`
	} `json:"counts"`
	DeletedAt         time.Time              `json:"deleted_at"`
	FormerPath        []TrashPathSegmentJSON `json:"former_path"`
	ID                uuid.UUID              `json:"id" format:"uuid" doc:"The trash batch ID."`
	RestorableInPlace bool                   `json:"restorable_in_place" doc:"False when the original parent is trashed or gone."`
	RootID            uuid.UUID              `json:"root_id" format:"uuid"`
	RootName          string                 `json:"root_name"`
	RootType          string                 `json:"root_type" enum:"folder,list,item"`
}

// TrashPathSegmentJSON is one ancestor of a trashed root as it was when deleted
// (published as "TrashPathSegment": the other features publish their own path
// segments, and the registry needs unique names).
type TrashPathSegmentJSON struct {
	ID   uuid.UUID `json:"id" format:"uuid"`
	Name string    `json:"name"`
	Type string    `json:"type" enum:"folder,list"`
}

// PagingJSON is the paging of a trash page (published as "Paging"; the item list
// publishes "Page" with the same shape).
type PagingJSON struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	Total  int `json:"total" doc:"Total rows matching, ignoring limit and offset."`
}

// TrashPageJSON is the listTrash answer (published as "TrashPage").
type TrashPageJSON struct {
	Data []TrashEntryJSON `json:"data"`
	Page PagingJSON       `json:"page"`
}

// RestoreTrashRequest is the optional body of restoreTrashBatch (published as "RestoreTrashRequest").
type RestoreTrashRequest struct {
	Destination *RestoreDestinationJSON `json:"destination,omitempty" doc:"folder_id for a folder or list root (null = workspace root), list_id for an item root."`
}

// RestoreDestinationJSON is the destination of a restore (published as "RestoreDestination").
// FolderID is absent (not set), null (the workspace root) or a folder id.
type RestoreDestinationJSON struct {
	FolderID httpserver.Optional[uuid.UUID] `json:"folder_id,omitempty"`
	ListID   *uuid.UUID                     `json:"list_id,omitempty" format:"uuid"`
}

// RestoreResultJSON names the root that came back (published as "RestoreResult").
type RestoreResultJSON struct {
	RootID   uuid.UUID `json:"root_id" format:"uuid"`
	RootType string    `json:"root_type" enum:"folder,list,item"`
}
