package folders

import (
	"time"

	"github.com/google/uuid"
)

// Folder is a folder as the API shows it. ParentID nil means the root.
type Folder struct {
	ID       uuid.UUID
	ParentID *uuid.UUID
	// OwnerID is set only on a top-level folder (F-027): nested folders inherit access from their top-level ancestor.
	OwnerID *uuid.UUID
	// MemberIDs are the users a top-level folder is shared with (never nil); empty for a nested one.
	MemberIDs []uuid.UUID
	Name      string
	SortOrder int
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}
