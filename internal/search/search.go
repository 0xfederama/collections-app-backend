// Package search finds active items by title (API.md §5 Search, DATA_MODEL §6).
// It knows nothing about HTTP: it returns typed errors. An item inside a
// trashed list or folder is itself trashed (batch cascade, D-004), so the query
// only has to filter on the item. Paths are built in Go from the active
// folders and lists, loaded once per request.
package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/items"
)

// Limits of the q parameter (after trimming) and page sizes (API.md §5).
const (
	MaxQueryLen  = 200
	DefaultLimit = items.DefaultLimit
	MaxLimit     = items.MaxLimit

	// DefaultRecentLimit and MaxRecentLimit bound Recent (API.md §5, D-070).
	DefaultRecentLimit = 12
	MaxRecentLimit     = 50

	// maxPathDepth stops the parent walk if the data ever holds a cycle.
	maxPathDepth = 10000
)

// ErrInvalidQuery means q is empty after trimming, or longer than MaxQueryLen.
var ErrInvalidQuery = errors.New("search: q must be 1 to 200 characters")

// Service is the search service of one workspace.
type Service struct {
	db          sqlc.DBTX
	workspaceID uuid.UUID
}

// New returns a Service bound to the single workspace.
func New(db sqlc.DBTX, workspaceID uuid.UUID) *Service {
	return &Service{db: db, workspaceID: workspaceID}
}

// Params are the options of Search. Zero Limit means the default.
type Params struct {
	Q      string
	Limit  int
	Offset int
}

// Result is one match: the item and where it lives, root folder first, list last.
type Result struct {
	Item items.Item
	Path []items.PathSegment
}

// Page is one page of results and the total number of matches.
type Page struct {
	Results       []Result
	Limit, Offset int
	Total         int
}

// Search returns the active items whose title contains p.Q (case-insensitive,
// literal). An exact title match ranks first, then titles alphabetically, then
// id. Total counts every match, whatever the page.
func (s *Service) Search(ctx context.Context, p Params) (Page, error) {
	q := strings.TrimSpace(p.Q)
	if n := utf8.RuneCountInString(q); n < 1 || n > MaxQueryLen {
		return Page{}, ErrInvalidQuery
	}
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	if p.Offset < 0 {
		p.Offset = 0
	}

	db := sqlc.New(s.db)
	like := items.EscapeLike(q)
	rows, err := db.SearchItems(ctx, sqlc.SearchItemsParams{
		WorkspaceID: s.workspaceID, LikeQ: like, Q: q,
		PageLimit: int32(p.Limit), PageOffset: int32(p.Offset), //nolint:gosec // bounded above
	})
	if err != nil {
		return Page{}, fmt.Errorf("search items: %w", err)
	}
	total, err := db.CountSearchItems(ctx, sqlc.CountSearchItemsParams{WorkspaceID: s.workspaceID, LikeQ: like})
	if err != nil {
		return Page{}, fmt.Errorf("count search items: %w", err)
	}
	page := Page{Results: make([]Result, 0, len(rows)), Limit: p.Limit, Offset: p.Offset, Total: int(total)}
	if len(rows) == 0 {
		return page, nil
	}

	paths, err := s.loadPaths(ctx, db)
	if err != nil {
		return Page{}, err
	}
	for _, r := range rows {
		it := items.FromSearchRow(r)
		path, ok := paths.of(it.ListID)
		if !ok {
			// The list was trashed or created between the two reads: leave the
			// row out rather than invent a path. Rare, and the next call is right.
			continue
		}
		page.Results = append(page.Results, Result{Item: it, Path: path})
	}
	return page, nil
}

// Recent returns the active items updated last, newest first (ties by id),
// each with its path. Zero limit means DefaultRecentLimit. It shares the path
// building of Search, so a row whose list vanished between reads is skipped.
func (s *Service) Recent(ctx context.Context, limit int) ([]Result, error) {
	if limit <= 0 {
		limit = DefaultRecentLimit
	}
	if limit > MaxRecentLimit {
		limit = MaxRecentLimit
	}
	db := sqlc.New(s.db)
	rows, err := db.ListRecentItems(ctx, sqlc.ListRecentItemsParams{WorkspaceID: s.workspaceID, PageLimit: int32(limit)}) //nolint:gosec // bounded above
	if err != nil {
		return nil, fmt.Errorf("list recent items: %w", err)
	}
	results := make([]Result, 0, len(rows))
	if len(rows) == 0 {
		return results, nil
	}
	paths, err := s.loadPaths(ctx, db)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		it := items.FromRecentRow(r)
		if path, ok := paths.of(it.ListID); ok {
			results = append(results, Result{Item: it, Path: path})
		}
	}
	return results, nil
}

// tree holds the active folders and lists, enough to build any item's path.
type tree struct {
	folders map[uuid.UUID]folderNode
	lists   map[uuid.UUID]listNode
}

type folderNode struct {
	name   string
	parent uuid.NullUUID
}

type listNode struct {
	name      string
	folder    uuid.NullUUID
	icon      *string
	iconColor *string
}

func (s *Service) loadPaths(ctx context.Context, db *sqlc.Queries) (tree, error) {
	folders, err := db.ListSearchFolders(ctx, s.workspaceID)
	if err != nil {
		return tree{}, fmt.Errorf("search folders: %w", err)
	}
	lists, err := db.ListSearchLists(ctx, s.workspaceID)
	if err != nil {
		return tree{}, fmt.Errorf("search lists: %w", err)
	}
	t := tree{folders: make(map[uuid.UUID]folderNode, len(folders)), lists: make(map[uuid.UUID]listNode, len(lists))}
	for _, f := range folders {
		t.folders[f.ID] = folderNode{name: f.Name, parent: f.ParentFolderID}
	}
	for _, l := range lists {
		t.lists[l.ID] = listNode{name: l.Name, folder: l.FolderID, icon: l.Icon, iconColor: l.IconColor}
	}
	return t, nil
}

// of returns the path of a list: its folders root first, then the list. ok is
// false when the list or one of its folders is not active.
func (t tree) of(listID uuid.UUID) (path []items.PathSegment, ok bool) {
	l, found := t.lists[listID]
	if !found {
		return nil, false
	}
	// Collect leaf to root, then reverse.
	rev := []items.PathSegment{{Type: "list", ID: listID, Name: l.name, Icon: l.icon, IconColor: l.iconColor}}
	for next, depth := l.folder, 0; next.Valid; depth++ {
		f, found := t.folders[next.UUID]
		if !found || depth >= maxPathDepth {
			return nil, false
		}
		rev = append(rev, items.PathSegment{Type: "folder", ID: next.UUID, Name: f.name})
		next = f.parent
	}
	path = make([]items.PathSegment, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		path = append(path, rev[i])
	}
	return path, true
}
