// Package tree serves the sidebar tree (GET /tree): every active folder and list,
// flat. It is a read-only feature: it owns no data and no writes.
package tree

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/folders"
	"github.com/0xfederama/collections-app-backend/internal/lists"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
)

// folderLister is satisfied by *folders.Postgres.
type folderLister interface {
	ListActive(ctx context.Context) ([]folders.Folder, error)
}

// listLister is satisfied by *lists.Postgres.
type listLister interface {
	ListActive(ctx context.Context) ([]lists.List, error)
}

// Access answers what the caller may see and how they ordered their top level (internal/access,
// wired in httpapi).
type Access interface {
	VisibleFolderIDs(ctx context.Context) (map[uuid.UUID]bool, error)
	VisibleListIDs(ctx context.Context) (map[uuid.UUID]bool, error)
	// TopOrder returns the caller's own position of their top-level folders and lists; an entry
	// they never ordered is absent.
	TopOrder(ctx context.Context) (folders, lists map[uuid.UUID]int, err error)
}

// Register adds getTree to the API. Folders and lists are read flat, ordered by
// (sort_order, id), so the client builds the tree. Only the boxes the caller may see are
// returned; without access the tree is empty.
func Register(api huma.API, log *slog.Logger, folders folderLister, lists listLister, access Access) {
	h := &handler{folders: folders, lists: lists, access: access}
	huma.Register(api, huma.Operation{
		OperationID: "getTree", Method: http.MethodGet, Path: "/api/v1/tree", Tags: []string{"tree"},
		Summary: "Get the sidebar tree",
		Description: "The **active** folders and lists the caller can see, flat and unpaginated. The client builds the tree.\n" +
			"The `sort_order` of a top-level folder or list is the caller's own position (each user orders their top level); nested entries keep the shared order.",
		Errors: []int{http.StatusInternalServerError},
	}, httpserver.Op(log, h.get))
}

// Tree is the getTree answer (published as the schema "Tree"). Both arrays are
// always present, empty rather than null.
type Tree struct {
	Folders []folders.FolderJSON `json:"folders" nullable:"false"`
	Lists   []lists.ListJSON     `json:"lists" nullable:"false"`
}

// TreeOutput is the getTree answer.
type TreeOutput struct {
	Body Tree
}

type handler struct {
	folders folderLister
	lists   listLister
	access  Access
}

func (h *handler) get(ctx context.Context, _ *struct{}) (*TreeOutput, error) {
	if h.access == nil {
		return &TreeOutput{Body: Tree{Folders: []folders.FolderJSON{}, Lists: []lists.ListJSON{}}}, nil
	}
	visibleF, err := h.access.VisibleFolderIDs(ctx)
	if err != nil {
		return nil, err
	}
	visibleL, err := h.access.VisibleListIDs(ctx)
	if err != nil {
		return nil, err
	}
	orderF, orderL, err := h.access.TopOrder(ctx)
	if err != nil {
		return nil, err
	}
	fs, err := h.folders.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	ls, err := h.lists.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	fs = slices.DeleteFunc(fs, func(f folders.Folder) bool { return !visibleF[f.ID] })
	ls = slices.DeleteFunc(ls, func(l lists.List) bool { return !visibleL[l.ID] })
	orderTop(fs, orderF, func(f folders.Folder) (uuid.UUID, bool) { return f.ID, f.ParentID == nil },
		func(f *folders.Folder, pos int) { f.SortOrder = pos })
	orderTop(ls, orderL, func(l lists.List) (uuid.UUID, bool) { return l.ID, l.FolderID == nil },
		func(l *lists.List, pos int) { l.SortOrder = pos })
	out := Tree{Folders: make([]folders.FolderJSON, 0, len(fs)), Lists: make([]lists.ListJSON, 0, len(ls))}
	for _, f := range fs {
		out.Folders = append(out.Folders, folders.ToJSON(f))
	}
	for _, l := range ls {
		out.Lists = append(out.Lists, lists.ToJSON(l))
	}
	return &TreeOutput{Body: out}, nil
}

// orderTop puts the top-level entries of rows in the caller's order and numbers them 0, 1, 2...
// in that order, so a client that sorts by (sort_order, id) shows the caller's order. Entries the
// caller ordered come first by their position; the others follow by the shared sort_order, then
// id (the rows arrive in that order). Nested entries keep their rows, positions and sort_order.
func orderTop[T any](rows []T, position map[uuid.UUID]int, top func(T) (uuid.UUID, bool), setOrder func(*T, int)) {
	var slots []int
	var entries []T
	for i, r := range rows {
		if _, isTop := top(r); isTop {
			slots = append(slots, i)
			entries = append(entries, r)
		}
	}
	rank := func(r T) (int, bool) {
		id, _ := top(r)
		p, ok := position[id]
		return p, ok
	}
	slices.SortStableFunc(entries, func(a, b T) int {
		pa, oka := rank(a)
		pb, okb := rank(b)
		switch {
		case oka && okb:
			return cmp.Compare(pa, pb)
		case oka:
			return -1
		case okb:
			return 1
		}
		return 0 // both unordered: keep the shared order
	})
	for n, slot := range slots {
		setOrder(&entries[n], n)
		rows[slot] = entries[n]
	}
}
