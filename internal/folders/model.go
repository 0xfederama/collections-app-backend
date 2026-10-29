package folders

import (
	"time"

	"github.com/google/uuid"
)

// Folder is a folder as the API shows it. ParentID nil means the root.
type Folder struct {
	ID        uuid.UUID
	ParentID  *uuid.UUID
	Name      string
	SortOrder int
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}
