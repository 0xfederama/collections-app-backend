package folders

import (
	"time"

	"github.com/google/uuid"
)

// FolderJSON is the wire shape of a folder (published as the schema "Folder").
// It lives outside handler.go so that the tree read can reuse it: the layer
// rules keep a feature's handler identifiers to itself.
type FolderJSON struct {
	ID             uuid.UUID  `json:"id" format:"uuid"`
	ParentFolderID *uuid.UUID `json:"parent_folder_id" format:"uuid" nullable:"true"`
	OwnerUserID    *uuid.UUID `json:"owner_user_id" format:"uuid" nullable:"true" doc:"The owner of a top-level folder; null for a nested one, which belongs to its top-level ancestor."`
	Name           string     `json:"name"`
	SortOrder      int        `json:"sort_order"`
	Version        int        `json:"version"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// ToJSON is the wire shape of f.
func ToJSON(f Folder) FolderJSON {
	return FolderJSON{
		ID: f.ID, ParentFolderID: f.ParentID, OwnerUserID: f.OwnerID, Name: f.Name, SortOrder: f.SortOrder,
		Version: f.Version, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
	}
}
