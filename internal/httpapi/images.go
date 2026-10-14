package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
)

// SetItemImage takes either a multipart upload (field `file`) or JSON `{url}`.
// The request guard has already checked If-Match, the media type and the size.
func (s *Server) SetItemImage(ctx context.Context, r oapi.SetItemImageRequestObject) (oapi.SetItemImageResponseObject, error) {
	version := IfMatchVersion(ctx)
	var item items.Item
	switch {
	case r.JSONBody != nil:
		var err error
		if item, err = s.deps.Images.SetURL(ctx, r.Id, version, r.JSONBody.Url); err != nil {
			return nil, err
		}
	case r.MultipartBody != nil:
		file, name, err := readFilePart(r.MultipartBody)
		if err != nil {
			return nil, err
		}
		defer func() { _ = file.Close() }()
		p, err := images.Process(file)
		if err != nil {
			return nil, err
		}
		if item, err = s.deps.Images.SetUpload(ctx, r.Id, version, p, name); err != nil {
			return nil, err
		}
	default:
		return nil, apperr.UnsupportedMediaType("send multipart/form-data or application/json")
	}
	body, err := itemBody(item)
	if err != nil {
		return nil, err
	}
	return oapi.SetItemImage200JSONResponse{Body: body, Headers: oapi.SetItemImage200ResponseHeaders{ETag: itemETag(item)}}, nil
}

func (s *Server) DeleteItemImage(ctx context.Context, r oapi.DeleteItemImageRequestObject) (oapi.DeleteItemImageResponseObject, error) {
	item, err := s.deps.Images.Delete(ctx, r.Id, IfMatchVersion(ctx))
	if err != nil {
		return nil, err
	}
	body, err := itemBody(item)
	if err != nil {
		return nil, err
	}
	return oapi.DeleteItemImage200JSONResponse{Body: body, Headers: oapi.DeleteItemImage200ResponseHeaders{ETag: itemETag(item)}}, nil
}

func (s *Server) GetAttachmentFile(ctx context.Context, r oapi.GetAttachmentFileRequestObject) (oapi.GetAttachmentFileResponseObject, error) {
	f, err := s.deps.Images.Open(ctx, r.Id, false)
	if err != nil {
		return nil, err
	}
	h := oapi.GetAttachmentFile200ResponseHeaders{CacheControl: immutableCacheControl}
	switch f.MediaType {
	case "image/png":
		return inlineFile{oapi.GetAttachmentFile200ImagepngResponse{Body: f.Body, Headers: h, ContentLength: f.Size}}, nil
	case "image/webp":
		return inlineFile{oapi.GetAttachmentFile200ImagewebpResponse{Body: f.Body, Headers: h, ContentLength: f.Size}}, nil
	default:
		return inlineFile{oapi.GetAttachmentFile200ImagejpegResponse{Body: f.Body, Headers: h, ContentLength: f.Size}}, nil
	}
}

func (s *Server) GetAttachmentThumbnail(ctx context.Context, r oapi.GetAttachmentThumbnailRequestObject) (oapi.GetAttachmentThumbnailResponseObject, error) {
	f, err := s.deps.Images.Open(ctx, r.Id, true)
	if err != nil {
		return nil, err
	}
	return inlineThumbnail{oapi.GetAttachmentThumbnail200ImagejpegResponse{
		Body: f.Body, Headers: oapi.GetAttachmentThumbnail200ResponseHeaders{CacheControl: immutableCacheControl}, ContentLength: f.Size,
	}}, nil
}

// immutableCacheControl: a replacement always creates a new attachment id, so a
// file never changes under its URL.
const immutableCacheControl = "private, max-age=31536000, immutable"

// inlineFile and inlineThumbnail add Content-Disposition: inline to the
// generated responses (the spec declares only Cache-Control). nosniff and the
// restrictive Content-Security-Policy come from the securityHeaders middleware.
type inlineFile struct {
	oapi.GetAttachmentFileResponseObject
}

func (r inlineFile) VisitGetAttachmentFileResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Disposition", "inline")
	return r.GetAttachmentFileResponseObject.VisitGetAttachmentFileResponse(w)
}

type inlineThumbnail struct {
	oapi.GetAttachmentThumbnailResponseObject
}

func (r inlineThumbnail) VisitGetAttachmentThumbnailResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Disposition", "inline")
	return r.GetAttachmentThumbnailResponseObject.VisitGetAttachmentThumbnailResponse(w)
}

// readFilePart finds the `file` part of a multipart body and returns it with
// its display name. Other parts are ignored.
func readFilePart(mr *multipart.Reader) (io.ReadCloser, *string, error) {
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, nil, apperr.Validation(apperr.FieldError{Field: "file", Message: "is required"})
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read multipart: %w", err)
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		var name *string
		if fn := part.FileName(); fn != "" {
			name = &fn
		}
		return part, name, nil
	}
}
