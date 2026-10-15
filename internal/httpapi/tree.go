package httpapi

import (
	"context"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/etag"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/tree"
)

// GetTree returns every active folder and list, flat (API.md §5 Tree).
func (s *Server) GetTree(ctx context.Context, _ oapi.GetTreeRequestObject) (oapi.GetTreeResponseObject, error) {
	t, err := s.deps.Tree.Tree(ctx)
	if err != nil {
		return nil, err
	}
	out := oapi.Tree{Folders: make([]oapi.Folder, 0, len(t.Folders)), Lists: make([]oapi.List, 0, len(t.Lists))}
	for _, f := range t.Folders {
		out.Folders = append(out.Folders, folderBody(f))
	}
	for _, l := range t.Lists {
		out.Lists = append(out.Lists, listBody(l))
	}
	return oapi.GetTree200JSONResponse(out), nil
}

func folderBody(f tree.Folder) oapi.Folder {
	return oapi.Folder{
		Id: f.ID, ParentFolderId: nullableUUID(f.ParentFolderID), Name: f.Name,
		SortOrder: f.SortOrder, Version: f.Version, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
	}
}

func listBody(l tree.List) oapi.List {
	out := oapi.List{
		Id: l.ID, FolderId: nullableUUID(l.FolderID), Name: l.Name, Kind: oapi.ItemKind(l.Kind), Description: nullableString(l.Description),
		DisplayMode: oapi.DisplayMode(l.DisplayMode), SortMode: oapi.SortMode(l.SortMode),
		SortOrder: l.SortOrder, ItemCount: l.ItemCount, Version: l.Version, CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt,
	}
	if l.Path != nil {
		path := make([]oapi.PathSegment, 0, len(l.Path))
		for _, p := range l.Path {
			path = append(path, oapi.PathSegment{Type: oapi.PathSegmentTypeFolder, Id: p.ID, Name: p.Name})
		}
		out.Path = &path
	}
	return out
}

// nullableUUID always sets the field: an unset Nullable would marshal as the zero UUID, not null.
func nullableUUID(id *uuid.UUID) nullable.Nullable[uuid.UUID] {
	if id == nil {
		return nullable.NewNullNullable[uuid.UUID]()
	}
	return nullable.NewNullableWithValue(*id)
}

func nullableString(s *string) nullable.Nullable[string] {
	if s == nil {
		return nullable.NewNullNullable[string]()
	}
	return nullable.NewNullableWithValue(*s)
}

// optionalUUID reads an optional nullable UUID field: absent and null both mean "none" (the root).
func optionalUUID(n nullable.Nullable[uuid.UUID]) *uuid.UUID {
	if v, err := n.Get(); err == nil {
		return &v
	}
	return nil
}

// requiredUUID reads a field the spec marks required but the generated types
// leave optional: absent is a 422, null means the root.
func requiredUUID(field string, n nullable.Nullable[uuid.UUID]) (*uuid.UUID, error) {
	if !n.IsSpecified() {
		return nil, apperr.Validation(apperr.FieldError{Field: field, Message: "is required (null means the root)"})
	}
	return optionalUUID(n), nil
}

// optionalString turns a nullable PATCH field into a tree.Optional.
func optionalString(n nullable.Nullable[string]) tree.Optional[string] {
	if !n.IsSpecified() {
		return tree.Optional[string]{}
	}
	if v, err := n.Get(); err == nil {
		return tree.Optional[string]{Set: true, Value: &v}
	}
	return tree.Optional[string]{Set: true}
}

func enumPtr[T ~string](v *T) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}

func folderETag(f tree.Folder) string { return etag.Format(etag.Folder, f.Version) }
func listETag(l tree.List) string     { return etag.Format(etag.List, l.Version) }
