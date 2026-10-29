// Package tree serves the sidebar tree (GET /tree): every active folder and list,
// flat. It is a read-only feature: it owns no data and no writes.
package tree

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

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

// Register adds getTree to the API. Folders and lists are read flat, ordered by
// (sort_order, id), so the client builds the tree.
func Register(api huma.API, log *slog.Logger, folders folderLister, lists listLister) {
	h := &handler{folders: folders, lists: lists}
	huma.Register(api, huma.Operation{
		OperationID: "getTree", Method: http.MethodGet, Path: "/api/v1/tree", Tags: []string{"tree"},
		Summary:     "Get the sidebar tree",
		Description: "All **active** folders and lists, flat and unpaginated. The client builds the tree.",
	}, httpserver.Op(log, h.get))
}

// Tree is the getTree answer (published as the schema "Tree"). Both arrays are
// always present, empty rather than null.
type Tree struct {
	Folders []folders.FolderJSON `json:"folders"`
	Lists   []lists.ListJSON     `json:"lists"`
}

// TreeOutput is the getTree answer.
type TreeOutput struct {
	Body Tree
}

type handler struct {
	folders folderLister
	lists   listLister
}

func (h *handler) get(ctx context.Context, _ *struct{}) (*TreeOutput, error) {
	fs, err := h.folders.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	ls, err := h.lists.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	out := Tree{Folders: make([]folders.FolderJSON, 0, len(fs)), Lists: make([]lists.ListJSON, 0, len(ls))}
	for _, f := range fs {
		out.Folders = append(out.Folders, folders.ToJSON(f))
	}
	for _, l := range ls {
		out.Lists = append(out.Lists, lists.ToJSON(l))
	}
	return &TreeOutput{Body: out}, nil
}
