package images

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/platform/etag"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

const (
	multipartMediaType = "multipart/form-data"
	jsonMediaType      = "application/json"

	// multipartBodyBytes is the limit of an upload request: the file plus its multipart envelope.
	multipartBodyBytes = MaxUploadBytes + httpserver.DefaultMaxBodyBytes
	// uploadTimeout replaces the server-wide read and write deadlines of an upload.
	uploadTimeout = 2 * time.Minute
	// immutableCacheControl: a replacement always creates a new attachment id, so a
	// file never changes under its URL.
	immutableCacheControl = "private, max-age=31536000, immutable"
)

// Register adds the image operations to the API. The handlers do HTTP only: parse
// the input, call the service, shape the output. Errors go back as they are:
// httpserver.Op maps them to problems.
func Register(api huma.API, log *slog.Logger, svc *Service) {
	h := &handler{svc: svc}
	tags := []string{"image"}

	huma.Register(api, huma.Operation{
		OperationID: "setItemImage", Method: http.MethodPut, Path: "/api/v1/items/{id}/image", Tags: tags,
		Summary: "Set the item image (upload or URL)",
		Description: "Requires `If-Match` (`\"i-N\"`). Replaces any existing image of either source. Send either `multipart/form-data` with a `file` " +
			"part (up to 10 MiB, JPEG, PNG or WebP detected by content sniffing, SVG always rejected) or `application/json` `{url}`. " +
			"Returns the updated item.",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusPreconditionFailed,
			http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType,
			http.StatusUnprocessableEntity, http.StatusPreconditionRequired},
		RequestBody: setImageRequestBody(),
		// Huma must not parse the body as JSON: the handler picks the decoder by Content-Type.
		SkipValidateBody: true,
		MaxBodyBytes:     multipartBodyBytes,
		Middlewares:      huma.Middlewares{ifMatchFirst(api), uploadGuard(api)},
	}, httpserver.Op(log, h.setItemImage))
	huma.Register(api, huma.Operation{
		OperationID: "deleteItemImage", Method: http.MethodDelete, Path: "/api/v1/items/{id}/image", Tags: tags,
		Summary:     "Remove the item image",
		Description: "Requires `If-Match` (`\"i-N\"`). Returns the updated item.",
		Errors:      []int{http.StatusBadRequest, http.StatusNotFound, http.StatusPreconditionFailed, http.StatusPreconditionRequired},
		Middlewares: huma.Middlewares{ifMatchFirst(api)},
	}, httpserver.Op(log, h.deleteItemImage))
	huma.Register(api, huma.Operation{
		OperationID: "getAttachmentFile", Method: http.MethodGet, Path: "/api/v1/attachments/{id}/file", Tags: tags,
		Summary:     "Download the original image",
		Description: "A replacement always creates a new attachment ID, so the response is cacheable forever.",
		Errors:      []int{http.StatusBadRequest, http.StatusNotFound},
	}, httpserver.Op(log, h.attachmentFile))
	huma.Register(api, huma.Operation{
		OperationID: "getAttachmentThumbnail", Method: http.MethodGet, Path: "/api/v1/attachments/{id}/thumbnail", Tags: tags,
		Summary:     "Download the JPEG thumbnail",
		Description: "Longest side 640 px. Same caching as the original.",
		Errors:      []int{http.StatusBadRequest, http.StatusNotFound},
	}, httpserver.Op(log, h.attachmentThumbnail))
}

type handler struct{ svc *Service }

// setImageRequestBody declares the two media types of setItemImage for the spec.
func setImageRequestBody() *huma.RequestBody {
	return &huma.RequestBody{Content: map[string]*huma.MediaType{
		multipartMediaType: {Schema: &huma.Schema{
			Type: "object", Required: []string{"file"},
			Properties: map[string]*huma.Schema{
				"file": {Type: "string", Format: "binary", Description: "The image. Its part may carry a filename, which is kept for display."},
			},
		}},
		jsonMediaType: {Schema: &huma.Schema{
			Type: "object", Required: []string{"url"},
			Properties: map[string]*huma.Schema{
				"url": {Type: "string", Description: "An absolute http or https URL of at most 2048 characters."},
			},
		}},
	}}
}

// ifMatchFirst answers 428 for a missing or malformed If-Match before the body is
// read, so a broken body does not hide the missing precondition.
func ifMatchFirst(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if _, ok := etag.Parse(ctx.Header("If-Match"), etag.Item); !ok {
			_ = huma.WriteErr(api, ctx, http.StatusPreconditionRequired, "", problem.PreconditionRequired())
			return
		}
		next(ctx)
	}
}

// uploadGuard runs before the body is read. A media type other than multipart or
// JSON answers 400, a JSON body over 64 KiB answers 413 (by its Content-Length),
// and an upload gets its own read and write deadlines. Huma then reads the body,
// capped at the operation's MaxBodyBytes.
func uploadGuard(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		mt, _, err := mime.ParseMediaType(ctx.Header("Content-Type"))
		if err != nil || (mt != multipartMediaType && mt != jsonMediaType) {
			_ = huma.WriteErr(api, ctx, http.StatusBadRequest, "", problem.UnsupportedContentType())
			return
		}
		r, w, ok := unwrap(ctx)
		if ok && mt == jsonMediaType && r.ContentLength > httpserver.DefaultMaxBodyBytes {
			_ = huma.WriteErr(api, ctx, http.StatusRequestEntityTooLarge, "", problem.PayloadTooLarge())
			return
		}
		if ok && mt == multipartMediaType {
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Now().Add(uploadTimeout)) // ErrNotSupported on test recorders: ignore
			_ = rc.SetWriteDeadline(time.Now().Add(uploadTimeout))
		}
		next(ctx)
	}
}

// unwrap returns the request and the response writer behind a Huma context.
func unwrap(ctx huma.Context) (*http.Request, http.ResponseWriter, bool) {
	u, ok := ctx.(interface {
		Unwrap() (*http.Request, http.ResponseWriter)
	})
	if !ok {
		return nil, nil, false
	}
	r, w := u.Unwrap()
	return r, w, true
}

// IDParam is the {id} path parameter of the image operations.
type IDParam struct {
	ID uuid.UUID `path:"id" format:"uuid"`
}

// ItemOutput is an item with its ETag.
type ItemOutput struct {
	ETag string `header:"ETag"`
	Body items.ItemJSON
}

// SetItemImageInput is the setItemImage path, If-Match and raw body. The body is
// read as bytes: the handler decodes it by Content-Type.
type SetItemImageInput struct {
	IDParam
	IfMatch     string `header:"If-Match" doc:"Item ETag, \"i-N\"."`
	ContentType string `header:"Content-Type"`
	RawBody     []byte `contentType:"multipart/form-data"`
}

// DeleteItemImageInput is the deleteItemImage path and If-Match.
type DeleteItemImageInput struct {
	IDParam
	IfMatch string `header:"If-Match" doc:"Item ETag, \"i-N\"."`
}

func (h *handler) setItemImage(ctx context.Context, in *SetItemImageInput) (*ItemOutput, error) {
	version, ok := etag.Parse(in.IfMatch, etag.Item)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	mt, params, err := mime.ParseMediaType(in.ContentType)
	if err != nil {
		return nil, problem.UnsupportedContentType()
	}
	var it items.Item
	switch mt {
	case multipartMediaType:
		it, err = h.setUpload(ctx, in.ID, version, in.RawBody, params["boundary"])
	case jsonMediaType:
		it, err = h.setURL(ctx, in.ID, version, in.RawBody)
	default:
		err = problem.UnsupportedContentType()
	}
	if err != nil {
		return nil, err
	}
	return itemOutput(it)
}

// setUpload takes the `file` part of a multipart body, validates it and attaches it.
func (h *handler) setUpload(ctx context.Context, id uuid.UUID, version int, body []byte, boundary string) (items.Item, error) {
	data, name, err := filePart(body, boundary)
	if err != nil {
		return items.Item{}, err
	}
	p, err := Process(bytes.NewReader(data))
	if err != nil {
		return items.Item{}, err
	}
	return h.svc.SetUpload(ctx, id, version, p, name)
}

// setURL reads the JSON body and points the item at the URL.
func (h *handler) setURL(ctx context.Context, id uuid.UUID, version int, body []byte) (items.Item, error) {
	if len(body) > httpserver.DefaultMaxBodyBytes {
		return items.Item{}, apperr.PayloadTooLarge(httpserver.DefaultMaxBodyBytes)
	}
	rawURL, err := urlFromJSON(body)
	if err != nil {
		return items.Item{}, err
	}
	return h.svc.SetURL(ctx, id, version, rawURL)
}

func (h *handler) deleteItemImage(ctx context.Context, in *DeleteItemImageInput) (*ItemOutput, error) {
	version, ok := etag.Parse(in.IfMatch, etag.Item)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	it, err := h.svc.Delete(ctx, in.ID, version)
	if err != nil {
		return nil, err
	}
	return itemOutput(it)
}

// filePart returns the bytes and the display name of the `file` part of a multipart
// body; other parts are skipped. A body that cannot be read as multipart has no
// readable file part, so it gets the same 422 as a body without one.
func filePart(body []byte, boundary string) ([]byte, *string, error) {
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := mr.NextPart()
		if err != nil {
			return nil, nil, missingFile()
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return nil, nil, missingFile()
		}
		var name *string
		if fn := part.FileName(); fn != "" {
			name = &fn
		}
		return data, name, nil
	}
}

func missingFile() error {
	return apperr.Validation(apperr.FieldError{Field: "file", Message: "is required"})
}

// urlFromJSON reads the `url` of a JSON body. Undeclared keys are a 400 unknown_field
// (the first in sorted order, so the answer is deterministic); a wrong JSON type or a
// body that is not an object is a 400 malformed_request. The URL itself is checked
// by the service.
func urlFromJSON(body []byte) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return "", problem.Malformed("The request body is not valid JSON.")
	}
	var unknown []string
	for k := range fields {
		if k != "url" {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return "", apperr.UnknownField(unknown[0])
	}
	var req SetItemImageURLRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return "", problem.Malformed("The request body is not valid JSON.")
	}
	return req.URL, nil
}

// itemOutput is the item an image write answers with, with its ETag.
func itemOutput(it items.Item) (*ItemOutput, error) {
	body, err := items.ToItemJSON(it)
	if err != nil {
		return nil, err
	}
	return &ItemOutput{ETag: etag.Format(etag.Item, it.Version), Body: body}, nil
}

func (h *handler) attachmentFile(ctx context.Context, in *IDParam) (*huma.StreamResponse, error) {
	f, err := h.svc.Open(ctx, in.ID, false)
	if err != nil {
		return nil, err
	}
	return serve(f), nil
}

func (h *handler) attachmentThumbnail(ctx context.Context, in *IDParam) (*huma.StreamResponse, error) {
	f, err := h.svc.Open(ctx, in.ID, true)
	if err != nil {
		return nil, err
	}
	return serve(f), nil
}

// serve streams a stored file inline. nosniff and the restrictive Content-Security-Policy
// come from the security headers middleware. The file is opened before this point, so
// a missing file is answered as a problem, never as a half-written response.
func serve(f File) *huma.StreamResponse {
	return &huma.StreamResponse{Body: func(ctx huma.Context) {
		defer func() { _ = f.Body.Close() }()
		ctx.SetHeader("Content-Type", f.MediaType)
		ctx.SetHeader("Content-Length", strconv.FormatInt(f.Size, 10))
		ctx.SetHeader("Cache-Control", immutableCacheControl)
		ctx.SetHeader("Content-Disposition", "inline")
		ctx.SetStatus(http.StatusOK)
		_, _ = io.Copy(ctx.BodyWriter(), f.Body)
	}}
}
