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

func (*Server) DeleteItemImage(context.Context, oapi.DeleteItemImageRequestObject) (oapi.DeleteItemImageResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) SetItemImage(context.Context, oapi.SetItemImageRequestObject) (oapi.SetItemImageResponseObject, error) {
	return nil, apperr.NotImplemented()
}

func (*Server) DeleteList(context.Context, oapi.DeleteListRequestObject) (oapi.DeleteListResponseObject, error) {
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
