// Package tree owns folders and lists: create, read, update, move, reorder and
// the flat sidebar tree (API.md §5, DATA_MODEL §2 and §7). It knows nothing
// about HTTP: it returns typed errors from internal/apperr. Deleting folders
// and lists is the trash service's job (B-10).
//
// Every read and write works on active rows only (deleted_at IS NULL), and a
// trashed or missing resource is a NotFound. Structural writes (create, move,
// reorder) take one per-workspace advisory lock first, see lockTree.
package tree

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/clock"
	"github.com/0xfederama/collections-app-backend/internal/database"
	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
)

// Limits and enums from DATA_MODEL §2 (the same rules are CHECK constraints).
const (
	maxNameLen          = 200
	maxDescriptionBytes = 64 << 10 // Markdown, decoded UTF-8 bytes
)

var (
	displayModes = map[string]bool{"compact": true, "thumbnail": true, "grid": true}
	sortModes    = map[string]bool{"manual": true, "title": true, "updated": true, "kind_key": true, "price": true}
)

// Defaults for a list created without display_mode / sort_mode.
const (
	DefaultDisplayMode = "thumbnail"
	DefaultSortMode    = "manual"
)

// DB is satisfied by *pgxpool.Pool: plain queries plus transactions.
type DB interface {
	database.TxBeginner
	sqlc.DBTX
}

// Service is the folders-and-lists service of one workspace.
type Service struct {
	db          DB
	clock       clock.Clock
	workspaceID uuid.UUID
}

// New returns a Service bound to the single workspace (D-002).
func New(db DB, clk clock.Clock, workspaceID uuid.UUID) *Service {
	return &Service{db: db, clock: clk, workspaceID: workspaceID}
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

// List is a list as the API shows it. FolderID nil means root. Path is filled
// by GetList only: the ancestor folders, root first.
type List struct {
	ID       uuid.UUID
	FolderID *uuid.UUID
	Name     string
	// Kind is the one kind of every item in the list (F-007).
	Kind        string
	Description *string
	DisplayMode string
	SortMode    string
	// PinnedFields is nil for the kind default (F009-B2).
	PinnedFields *PinnedFields
	SortOrder    int
	ItemCount    int
	Version      int
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Path         []PathFolder
}

// PinnedFields are the field keys a list pins in its rows, in order: the first is
// the primary field, the second the secondary (F009-B2, replaces the under/right
// slots of F-002). Non-nil once stored, and may be empty (pin nothing).
type PinnedFields []string

func (p PinnedFields) equal(o PinnedFields) bool {
	return slices.Equal(p, o)
}

// pinnedFromJSON reads the stored column. NULL and anything unreadable are nil (the kind default).
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

func pinnedToJSON(p *PinnedFields) ([]byte, error) {
	if p == nil {
		return nil, nil
	}
	if *p == nil {
		return []byte("[]"), nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal pinned fields: %w", err)
	}
	return b, nil
}

// PathFolder is one ancestor folder of a list.
type PathFolder struct {
	ID   uuid.UUID
	Name string
}

// Tree is every active folder and list, flat, ordered by (sort_order, id).
type Tree struct {
	Folders []Folder
	Lists   []List
}

// Optional is a PATCH field: Set tells absent from present, and a nil Value
// means an explicit null.
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

// lockTree serializes structural writes of the workspace until the transaction
// ends. One lock for the whole tree (not one per parent) is deliberate: the
// folder cycle check reads other folders' parents, so two concurrent moves
// A→B and B→A would both pass it under per-parent locks. Trash delete and
// restore (B-10) change the tree too and must take it as well. It also covers
// the root scope, which has no parent row to lock.
func (s *Service) lockTree(ctx context.Context, q *sqlc.Queries) error {
	if err := q.LockTree(ctx, s.workspaceID); err != nil {
		return fmt.Errorf("lock tree: %w", err)
	}
	return nil
}

func (s *Service) now() time.Time { return s.clock.Now() }

// cleanName trims name and checks the 1–200 character rule.
func cleanName(name string) (string, *apperr.FieldError) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameLen {
		return name, &apperr.FieldError{Field: "name", Message: fmt.Sprintf("must be 1 to %d characters", maxNameLen)}
	}
	return name, nil
}

func validate(errs []apperr.FieldError) error {
	if len(errs) == 0 {
		return nil
	}
	return apperr.Validation(errs...)
}

// nullUUID converts the optional parent/folder id to its sqlc form.
func nullUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
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
		ID: l.ID, FolderID: ptrUUID(l.FolderID), Name: l.Name, Kind: l.Kind, Description: l.Description,
		DisplayMode: l.DisplayMode, SortMode: l.SortMode, PinnedFields: pinnedFromJSON(l.PinnedFields), SortOrder: int(l.SortOrder), ItemCount: int(l.ItemCount),
		Version: int(l.Version), CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt,
	}
}
