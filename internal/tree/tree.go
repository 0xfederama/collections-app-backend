// Package tree serves the flat sidebar tree (GET /tree) and holds the folder and
// list shapes it returns. Writes to folders and lists live in internal/folders and
// internal/lists; deleting them is the trash service's job (B-10).
package tree

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
)

// DB is satisfied by *pgxpool.Pool: plain queries plus transactions.
type DB interface {
	database.TxBeginner
	sqlc.DBTX
}

// Service reads the folders-and-lists tree of one workspace. The clock is not
// used: the tree is read only.
type Service struct {
	db          DB
	workspaceID uuid.UUID
}

// New returns a Service bound to the single workspace (D-002).
func New(db DB, _ clock.Clock, workspaceID uuid.UUID) *Service {
	return &Service{db: db, workspaceID: workspaceID}
}

// Folder is a folder as the API shows it. ParentFolderID nil means root.
type Folder struct {
	ID             uuid.UUID
	ParentFolderID *uuid.UUID
	Name           string
	SortOrder      int
	Version        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// List is a list as the API shows it. FolderID nil means root.
type List struct {
	ID       uuid.UUID
	FolderID *uuid.UUID
	Name     string
	// Type is the one type of every item in the list (F-007); nil is a list with no type.
	Type        *string
	Description *string
	DisplayMode string
	SortMode    string
	// PinnedFields is nil for the type default (F009-B2).
	PinnedFields *PinnedFields
	// Icon and IconColor are nil for the default icon (F012-B1).
	Icon      *string
	IconColor *string
	SortOrder int
	ItemCount int
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PinnedFields are the field keys a list pins in its rows, in order: the first is
// the primary field, the second the secondary (F009-B2, replaces the under/right
// slots of F-002). Non-nil once stored, and may be empty (pin nothing).
type PinnedFields []string

// pinnedFromJSON reads the stored column. NULL and anything unreadable are nil (the type default).
func pinnedFromJSON(b []byte) *PinnedFields {
	if b == nil {
		return nil
	}
	var p PinnedFields
	if json.Unmarshal(b, &p) != nil {
		return nil
	}
	if p == nil {
		p = PinnedFields{}
	}
	return &p
}

// Tree is every active folder and list, flat, ordered by (sort_order, id).
type Tree struct {
	Folders []Folder
	Lists   []List
}

// Optional is a PATCH field: Set tells absent from present, and a nil Value
// means an explicit null. Items use it for their PATCH.
type Optional[T any] struct {
	Set   bool
	Value *T
}

// Tree returns all active folders and lists (item_count = active items).
func (s *Service) Tree(ctx context.Context) (Tree, error) {
	q := sqlc.New(s.db)
	folders, err := q.ListActiveFolders(ctx, s.workspaceID)
	if err != nil {
		return Tree{}, fmt.Errorf("list folders: %w", err)
	}
	lists, err := q.ListActiveLists(ctx, s.workspaceID)
	if err != nil {
		return Tree{}, fmt.Errorf("list lists: %w", err)
	}
	t := Tree{Folders: make([]Folder, 0, len(folders)), Lists: make([]List, 0, len(lists))}
	for _, f := range folders {
		t.Folders = append(t.Folders, folderFromRow(f))
	}
	for _, l := range lists {
		t.Lists = append(t.Lists, listFromRow(sqlc.GetListRow(l)))
	}
	return t, nil
}

func ptrUUID(id uuid.NullUUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	v := id.UUID
	return &v
}

func folderFromRow(f sqlc.Folder) Folder {
	return Folder{
		ID: f.ID, ParentFolderID: ptrUUID(f.ParentFolderID), Name: f.Name,
		SortOrder: int(f.SortOrder), Version: int(f.Version), CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
	}
}

func listFromRow(l sqlc.GetListRow) List {
	return List{
		ID: l.ID, FolderID: ptrUUID(l.FolderID), Name: l.Name, Type: l.Type, Description: l.Description,
		DisplayMode: l.DisplayMode, SortMode: l.SortMode, PinnedFields: pinnedFromJSON(l.PinnedFields), Icon: l.Icon, IconColor: l.IconColor,
		SortOrder: int(l.SortOrder), ItemCount: int(l.ItemCount),
		Version: int(l.Version), CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt,
	}
}
