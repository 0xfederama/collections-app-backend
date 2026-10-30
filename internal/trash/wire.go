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
	Counts            TrashCountsJSON          `json:"counts"`
	DeletedAt         time.Time                `json:"deleted_at"`
	FormerPath        []httpserver.PathSegment `json:"former_path" nullable:"false"`
	ID                uuid.UUID                `json:"id" format:"uuid" doc:"The trash batch ID."`
	RestorableInPlace bool                     `json:"restorable_in_place" doc:"False when the original parent is trashed or gone."`
	RootID            uuid.UUID                `json:"root_id" format:"uuid"`
	RootName          string                   `json:"root_name"`
	RootType          string                   `json:"root_type" enum:"folder,list,item"`
}

// TrashCountsJSON is what a trash batch holds (published as "TrashCounts").
type TrashCountsJSON struct {
	Folders int `json:"folders"`
	Items   int `json:"items"`
	Lists   int `json:"lists"`
}

// TrashPageJSON is the listTrash answer (published as "TrashPage").
type TrashPageJSON struct {
	Data []TrashEntryJSON `json:"data" nullable:"false"`
	Page httpserver.Page  `json:"page"`
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
