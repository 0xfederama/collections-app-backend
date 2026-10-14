package httpapi

import (
	"context"

	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
)

func (s *Server) CreateFolder(ctx context.Context, r oapi.CreateFolderRequestObject) (oapi.CreateFolderResponseObject, error) {
	f, err := s.deps.Tree.CreateFolder(ctx, r.Body.Name, optionalUUID(r.Body.ParentFolderId))
	if err != nil {
		return nil, err
	}
	return oapi.CreateFolder201JSONResponse{
		Body:    folderBody(f),
		Headers: oapi.CreateFolder201ResponseHeaders{ETag: folderETag(f), Location: "/api/v1/folders/" + f.ID.String()},
	}, nil
}

func (s *Server) GetFolder(ctx context.Context, r oapi.GetFolderRequestObject) (oapi.GetFolderResponseObject, error) {
	f, err := s.deps.Tree.GetFolder(ctx, r.Id)
	if err != nil {
		return nil, err
	}
	return oapi.GetFolder200JSONResponse{Body: folderBody(f), Headers: oapi.GetFolder200ResponseHeaders{ETag: folderETag(f)}}, nil
}

func (s *Server) UpdateFolder(ctx context.Context, r oapi.UpdateFolderRequestObject) (oapi.UpdateFolderResponseObject, error) {
	f, err := s.deps.Tree.UpdateFolder(ctx, r.Id, IfMatchVersion(ctx), r.Body.Name)
	if err != nil {
		return nil, err
	}
	return oapi.UpdateFolder200JSONResponse{Body: folderBody(f), Headers: oapi.UpdateFolder200ResponseHeaders{ETag: folderETag(f)}}, nil
}

func (s *Server) MoveFolder(ctx context.Context, r oapi.MoveFolderRequestObject) (oapi.MoveFolderResponseObject, error) {
	parent, err := requiredUUID("parent_folder_id", r.Body.ParentFolderId)
	if err != nil {
		return nil, err
	}
	f, err := s.deps.Tree.MoveFolder(ctx, r.Id, IfMatchVersion(ctx), parent)
	if err != nil {
		return nil, err
	}
	return oapi.MoveFolder200JSONResponse{Body: folderBody(f), Headers: oapi.MoveFolder200ResponseHeaders{ETag: folderETag(f)}}, nil
}

func (s *Server) ReorderFolders(ctx context.Context, r oapi.ReorderFoldersRequestObject) (oapi.ReorderFoldersResponseObject, error) {
	parent, err := requiredUUID("parent_folder_id", r.Body.ParentFolderId)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Tree.ReorderFolders(ctx, parent, r.Body.Ids); err != nil {
		return nil, err
	}
	return oapi.ReorderFolders204Response{}, nil
}
