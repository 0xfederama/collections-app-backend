package favicons

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
)

// cacheControl lets the browser keep an icon for a week; the server cache holds it longer.
const cacheControl = "public, max-age=604800"

// Register adds GET /api/v1/favicon to the API.
func Register(api huma.API, log *slog.Logger, svc *Service) {
	h := &handler{svc: svc}
	huma.Register(api, huma.Operation{
		OperationID: "getFavicon", Method: http.MethodGet, Path: "/api/v1/favicon", Tags: []string{"favicon"},
		Summary: "Get the favicon of a website",
		Description: "The server fetches the icon of the origin of `url` (http or https, at most 2048 characters): the " +
			"`<link rel=icon>` of its home page, else `/favicon.ico`. It never connects to loopback, private, link-local " +
			"or other non-public addresses (422 on `url`). Icons are cached on disk for 30 days, \"no icon\" answers for " +
			"1 day. PNG, ICO, GIF, JPEG and WebP up to 256 KiB; SVG is refused. 404 `favicon_not_found` means the web " +
			"shows its fallback.",
		Errors: []int{http.StatusNotFound, http.StatusUnprocessableEntity},
		Responses: map[string]*huma.Response{"200": {
			Description: "The icon bytes.",
			Content: map[string]*huma.MediaType{
				"image/png":    {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/x-icon": {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/gif":    {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/jpeg":   {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/webp":   {Schema: &huma.Schema{Type: "string", Format: "binary"}},
			},
		}},
	}, httpserver.Op(log, h.get))
}

type handler struct{ svc *Service }

// GetFaviconInput is the query of getFavicon.
type GetFaviconInput struct {
	URL string `query:"url" required:"true" doc:"An absolute http or https URL; only its origin is used."`
}

func (h *handler) get(ctx context.Context, in *GetFaviconInput) (*huma.StreamResponse, error) {
	icon, err := h.svc.Get(ctx, in.URL)
	if err != nil {
		return nil, err
	}
	return &huma.StreamResponse{Body: func(ctx huma.Context) {
		ctx.SetHeader("Content-Type", icon.ContentType)
		ctx.SetHeader("Content-Length", strconv.Itoa(len(icon.Data)))
		ctx.SetHeader("Cache-Control", cacheControl)
		ctx.SetHeader("X-Content-Type-Options", "nosniff")
		ctx.SetStatus(http.StatusOK)
		_, _ = ctx.BodyWriter().Write(icon.Data)
	}}, nil
}
