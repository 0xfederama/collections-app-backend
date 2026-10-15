package httpapi

import (
	"context"

	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/tree"
)

func (s *Server) CreateList(ctx context.Context, r oapi.CreateListRequestObject) (oapi.CreateListResponseObject, error) {
	b := r.Body
	in := tree.CreateListInput{
		Name:        b.Name,
		Kind:        string(b.Kind),
		FolderID:    optionalUUID(b.FolderId),
		Description: optionalString(b.Description).Value,
		DisplayMode: derefOr(enumPtr(b.DisplayMode)),
		SortMode:    derefOr(enumPtr(b.SortMode)),
	}
	l, err := s.deps.Tree.CreateList(ctx, in)
	if err != nil {
		return nil, err
	}
	return oapi.CreateList201JSONResponse{
		Body:    listBody(l),
		Headers: oapi.CreateList201ResponseHeaders{ETag: listETag(l), Location: "/api/v1/lists/" + l.ID.String()},
	}, nil
}

func (s *Server) GetList(ctx context.Context, r oapi.GetListRequestObject) (oapi.GetListResponseObject, error) {
	l, err := s.deps.Tree.GetList(ctx, r.Id)
	if err != nil {
		return nil, err
	}
	return oapi.GetList200JSONResponse{Body: listBody(l), Headers: oapi.GetList200ResponseHeaders{ETag: listETag(l)}}, nil
}

func (s *Server) UpdateList(ctx context.Context, r oapi.UpdateListRequestObject) (oapi.UpdateListResponseObject, error) {
	b := r.Body
	l, err := s.deps.Tree.UpdateList(ctx, r.Id, IfMatchVersion(ctx), tree.ListPatch{
		Name:        b.Name,
		Kind:        enumPtr(b.Kind),
		Description: optionalString(b.Description),
		DisplayMode: enumPtr(b.DisplayMode),
		SortMode:    enumPtr(b.SortMode),
	})
	if err != nil {
		return nil, err
	}
	return oapi.UpdateList200JSONResponse{Body: listBody(l), Headers: oapi.UpdateList200ResponseHeaders{ETag: listETag(l)}}, nil
}

func (s *Server) MoveList(ctx context.Context, r oapi.MoveListRequestObject) (oapi.MoveListResponseObject, error) {
	folder, err := requiredUUID("folder_id", r.Body.FolderId)
	if err != nil {
		return nil, err
	}
	l, err := s.deps.Tree.MoveList(ctx, r.Id, IfMatchVersion(ctx), folder)
	if err != nil {
		return nil, err
	}
	return oapi.MoveList200JSONResponse{Body: listBody(l), Headers: oapi.MoveList200ResponseHeaders{ETag: listETag(l)}}, nil
}

func (s *Server) ReorderLists(ctx context.Context, r oapi.ReorderListsRequestObject) (oapi.ReorderListsResponseObject, error) {
	folder, err := requiredUUID("folder_id", r.Body.FolderId)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Tree.ReorderLists(ctx, folder, r.Body.Ids); err != nil {
		return nil, err
	}
	return oapi.ReorderLists204Response{}, nil
}

func derefOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
