package httpapi

import (
	"context"

	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/trash"
)

func (s *Server) DeleteFolder(ctx context.Context, r oapi.DeleteFolderRequestObject) (oapi.DeleteFolderResponseObject, error) {
	if err := s.deps.Trash.DeleteFolder(ctx, r.Id, IfMatchVersion(ctx)); err != nil {
		return nil, err
	}
	return oapi.DeleteFolder204Response{}, nil
}

func (s *Server) DeleteList(ctx context.Context, r oapi.DeleteListRequestObject) (oapi.DeleteListResponseObject, error) {
	if err := s.deps.Trash.DeleteList(ctx, r.Id, IfMatchVersion(ctx)); err != nil {
		return nil, err
	}
	return oapi.DeleteList204Response{}, nil
}

func (s *Server) DeleteItem(ctx context.Context, r oapi.DeleteItemRequestObject) (oapi.DeleteItemResponseObject, error) {
	if err := s.deps.Trash.DeleteItem(ctx, r.Id, IfMatchVersion(ctx)); err != nil {
		return nil, err
	}
	return oapi.DeleteItem204Response{}, nil
}

func (s *Server) ListTrash(ctx context.Context, r oapi.ListTrashRequestObject) (oapi.ListTrashResponseObject, error) {
	var limit, offset int
	if r.Params.Limit != nil {
		limit = *r.Params.Limit
	}
	if r.Params.Offset != nil {
		offset = *r.Params.Offset
	}
	page, err := s.deps.Trash.List(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	out := oapi.ListTrash200JSONResponse{
		Data: make([]oapi.TrashEntry, 0, len(page.Entries)),
		Page: oapi.Page{Limit: page.Limit, Offset: page.Offset, Total: page.Total},
	}
	for _, e := range page.Entries {
		path := make([]oapi.PathSegment, 0, len(e.FormerPath))
		for _, seg := range e.FormerPath {
			path = append(path, oapi.PathSegment{Type: oapi.PathSegmentType(seg.Type), Id: seg.ID, Name: seg.Name})
		}
		entry := oapi.TrashEntry{
			Id: e.ID, RootType: oapi.TrashEntryRootType(e.RootType), RootId: e.RootID, RootName: e.RootName,
			FormerPath: path, DeletedAt: e.DeletedAt, RestorableInPlace: e.RestorableInPlace,
		}
		entry.Counts.Folders, entry.Counts.Lists, entry.Counts.Items = e.Folders, e.Lists, e.Items
		out.Data = append(out.Data, entry)
	}
	return out, nil
}

func (s *Server) RestoreTrashBatch(ctx context.Context, r oapi.RestoreTrashBatchRequestObject) (oapi.RestoreTrashBatchResponseObject, error) {
	var dest *trash.Destination
	if r.Body != nil && r.Body.Destination != nil {
		d := r.Body.Destination
		dest = &trash.Destination{
			FolderSet: d.FolderId.IsSpecified(),
			FolderID:  optionalUUID(d.FolderId),
			ListID:    d.ListId,
		}
	}
	res, err := s.deps.Trash.Restore(ctx, r.BatchId, dest)
	if err != nil {
		return nil, err
	}
	return oapi.RestoreTrashBatch200JSONResponse{RootType: oapi.RestoreResultRootType(res.RootType), RootId: res.RootID}, nil
}

func (s *Server) PurgeTrashBatch(ctx context.Context, r oapi.PurgeTrashBatchRequestObject) (oapi.PurgeTrashBatchResponseObject, error) {
	if err := s.deps.Trash.Purge(ctx, r.BatchId); err != nil {
		return nil, err
	}
	return oapi.PurgeTrashBatch204Response{}, nil
}

func (s *Server) EmptyTrash(ctx context.Context, _ oapi.EmptyTrashRequestObject) (oapi.EmptyTrashResponseObject, error) {
	if _, err := s.deps.Trash.PurgeAll(ctx); err != nil {
		return nil, err
	}
	return oapi.EmptyTrash204Response{}, nil
}
