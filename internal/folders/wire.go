package folders

import (
	"time"

	"github.com/google/uuid"
)

// FolderJSON is the wire shape of a folder (published as the schema "Folder").
// It lives outside handler.go so that the tree read can reuse it: the layer
// rules keep a feature's handler identifiers to itself.
type FolderJSON struct {
	ID              uuid.UUID   `json:"id" format:"uuid"`
	ParentFolderID  *uuid.UUID  `json:"parent_folder_id" format:"uuid" nullable:"true"`
	OwnerUserID     *uuid.UUID  `json:"owner_user_id" format:"uuid" nullable:"true" doc:"The owner of a top-level folder; null for a nested one, which belongs to its top-level ancestor."`
	Shared          bool        `json:"shared" doc:"True for a top-level folder shared with at least one other user."`
	MemberUserIDs   []uuid.UUID `json:"member_user_ids" format:"uuid" nullable:"false" doc:"The users a top-level folder is shared with, besides its owner; empty for a nested or an unshared folder."`
	Name            string      `json:"name"`
	SortOrder       int         `json:"sort_order"`
	Version         int         `json:"version"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	CreatedByUserID *uuid.UUID  `json:"created_by_user_id" format:"uuid" nullable:"true" doc:"The user who created it; null when unknown (created before authorship was recorded)."`
	UpdatedByUserID *uuid.UUID  `json:"updated_by_user_id" format:"uuid" nullable:"true" doc:"The user who last changed it; null when unknown. A system refresh keeps the previous value."`
}

// ToJSON is the wire shape of f.
func ToJSON(f Folder) FolderJSON {
	return FolderJSON{
		ID: f.ID, ParentFolderID: f.ParentID, OwnerUserID: f.OwnerID,
		Shared: len(f.MemberIDs) > 0, MemberUserIDs: members(f.MemberIDs), Name: f.Name, SortOrder: f.SortOrder,
		Version: f.Version, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
		CreatedByUserID: f.CreatedByID, UpdatedByUserID: f.UpdatedByID,
	}
}

func members(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}
