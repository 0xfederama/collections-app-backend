package httpapi

import (
	"context"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
)

// Every operation below answers 501 until its endpoint task implements it.
// To implement one: delete its method here, add it to a file of its own
// (settings.go, folders.go, …) and return domain errors from internal/apperr.

func (*Server) GetAttachmentFile(context.Context, oapi.GetAttachmentFileRequestObject) (oapi.GetAttachmentFileResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) GetAttachmentThumbnail(context.Context, oapi.GetAttachmentThumbnailRequestObject) (oapi.GetAttachmentThumbnailResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) DeleteFolder(context.Context, oapi.DeleteFolderRequestObject) (oapi.DeleteFolderResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) DeleteItem(context.Context, oapi.DeleteItemRequestObject) (oapi.DeleteItemResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) GetItem(context.Context, oapi.GetItemRequestObject) (oapi.GetItemResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) UpdateItem(context.Context, oapi.UpdateItemRequestObject) (oapi.UpdateItemResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) GetItemContent(context.Context, oapi.GetItemContentRequestObject) (oapi.GetItemContentResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) UpdateItemContent(context.Context, oapi.UpdateItemContentRequestObject) (oapi.UpdateItemContentResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) RestoreContentVersion(context.Context, oapi.RestoreContentVersionRequestObject) (oapi.RestoreContentVersionResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) ListContentVersions(context.Context, oapi.ListContentVersionsRequestObject) (oapi.ListContentVersionsResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) GetContentVersion(context.Context, oapi.GetContentVersionRequestObject) (oapi.GetContentVersionResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) DeleteItemImage(context.Context, oapi.DeleteItemImageRequestObject) (oapi.DeleteItemImageResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) SetItemImage(context.Context, oapi.SetItemImageRequestObject) (oapi.SetItemImageResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) MoveItem(context.Context, oapi.MoveItemRequestObject) (oapi.MoveItemResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) DeleteList(context.Context, oapi.DeleteListRequestObject) (oapi.DeleteListResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) ListItems(context.Context, oapi.ListItemsRequestObject) (oapi.ListItemsResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) CreateItem(context.Context, oapi.CreateItemRequestObject) (oapi.CreateItemResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) ReorderListItems(context.Context, oapi.ReorderListItemsRequestObject) (oapi.ReorderListItemsResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) SearchItems(context.Context, oapi.SearchItemsRequestObject) (oapi.SearchItemsResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) ListTrash(context.Context, oapi.ListTrashRequestObject) (oapi.ListTrashResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) PurgeTrashBatch(context.Context, oapi.PurgeTrashBatchRequestObject) (oapi.PurgeTrashBatchResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) RestoreTrashBatch(context.Context, oapi.RestoreTrashBatchRequestObject) (oapi.RestoreTrashBatchResponseObject, error) {
	return nil, apperr.NotImplemented()
}
