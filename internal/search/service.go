package search

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/items"
)

// maxPathDepth stops the parent walk if the data ever holds a cycle.
const maxPathDepth = 10000

// ErrInvalidQuery means q is empty after trimming, or longer than MaxQueryLen.
var ErrInvalidQuery = errors.New("search: q must be 1 to 200 characters")

// Store reads the rows the search needs. Declared here, where it is used:
// *Postgres implements it and tests may use a fake. Methods take the workspace
// from the context and see active rows only.
type Store interface {
	// SearchItems returns one page of the items whose title matches like (an escaped LIKE
	// fragment). Exact matches of q come first, then by title and id.
	SearchItems(ctx context.Context, like, q string, limit, offset int) ([]items.Item, error)
	// CountSearchItems counts every item whose title matches like, whatever the page.
	CountSearchItems(ctx context.Context, like string) (int, error)
	// ListSearchFolders returns every active folder.
	ListSearchFolders(ctx context.Context) ([]Folder, error)
	// ListSearchLists returns every active list.
	ListSearchLists(ctx context.Context) ([]List, error)
	// ListRecentItems returns the active items updated last, newest first (ties by id).
	ListRecentItems(ctx context.Context, limit int) ([]items.Item, error)
}

// Service holds the search rules: the query and paging limits, and the path of
// each result, built from the folders and lists loaded once per request.
type Service struct{ store Store }

// NewService returns the search rules over the store.
func NewService(store Store) *Service { return &Service{store: store} }

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

	like := items.EscapeLike(q)
	rows, err := s.store.SearchItems(ctx, like, q, p.Limit, p.Offset)
	if err != nil {
		return Page{}, err
	}
	total, err := s.store.CountSearchItems(ctx, like)
	if err != nil {
		return Page{}, err
	}
	page := Page{Results: make([]Result, 0, len(rows)), Limit: p.Limit, Offset: p.Offset, Total: total}
	if len(rows) == 0 {
		return page, nil
	}

	paths, err := s.loadPaths(ctx)
	if err != nil {
		return Page{}, err
	}
	for _, it := range rows {
		path, ok := paths.of(it.ListID)
		if !ok {
			// The list was trashed or created between the two reads: leave the
			// row out rather than invent a path. Rare, and the next call is right.
			continue
		}
		page.Results = append(page.Results, Result{Item: it, TypeName: paths.typeName(it.ListID), Path: path})
	}
	return page, nil
}

// Recent returns the active items updated last, newest first (ties by id), each
// with its path. Zero limit means DefaultRecentLimit. It shares the path building
// of Search, so a row whose list vanished between reads is skipped.
func (s *Service) Recent(ctx context.Context, limit int) ([]Result, error) {
	if limit <= 0 {
		limit = DefaultRecentLimit
	}
	if limit > MaxRecentLimit {
		limit = MaxRecentLimit
	}
	rows, err := s.store.ListRecentItems(ctx, limit)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(rows))
	if len(rows) == 0 {
		return results, nil
	}
	paths, err := s.loadPaths(ctx)
	if err != nil {
		return nil, err
	}
	for _, it := range rows {
		if path, ok := paths.of(it.ListID); ok {
			results = append(results, Result{Item: it, TypeName: paths.typeName(it.ListID), Path: path})
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
	parent *uuid.UUID
}

type listNode struct {
	name      string
	folder    *uuid.UUID
	icon      *string
	iconColor *string
	typeName  *string
}

func (s *Service) loadPaths(ctx context.Context) (tree, error) {
	folders, err := s.store.ListSearchFolders(ctx)
	if err != nil {
		return tree{}, err
	}
	lists, err := s.store.ListSearchLists(ctx)
	if err != nil {
		return tree{}, err
	}
	t := tree{folders: make(map[uuid.UUID]folderNode, len(folders)), lists: make(map[uuid.UUID]listNode, len(lists))}
	for _, f := range folders {
		t.folders[f.ID] = folderNode{name: f.Name, parent: f.ParentID}
	}
	for _, l := range lists {
		t.lists[l.ID] = listNode{name: l.Name, folder: l.FolderID, icon: l.Icon, iconColor: l.IconColor, typeName: l.TypeName}
	}
	return t, nil
}

func (t tree) typeName(listID uuid.UUID) *string { return t.lists[listID].typeName }

// of returns the path of a list: its folders root first, then the list. ok is
// false when the list or one of its folders is not active.
func (t tree) of(listID uuid.UUID) (path []items.PathSegment, ok bool) {
	l, found := t.lists[listID]
	if !found {
		return nil, false
	}
	// Collect leaf to root, then reverse.
	rev := []items.PathSegment{{Type: "list", ID: listID, Name: l.name, Icon: l.icon, IconColor: l.iconColor}}
	for next, depth := l.folder, 0; next != nil; depth++ {
		f, found := t.folders[*next]
		if !found || depth >= maxPathDepth {
			return nil, false
		}
		rev = append(rev, items.PathSegment{Type: "folder", ID: *next, Name: f.name})
		next = f.parent
	}
	path = make([]items.PathSegment, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		path = append(path, rev[i])
	}
	return path, true
}
