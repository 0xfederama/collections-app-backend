package stores

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
)

// Register adds GET /api/v1/store-providers to the API. The list is fixed, so the
// handler has no input and no service: it shapes Providers() as the answer.
func Register(api huma.API, log *slog.Logger) {
	huma.Register(api, huma.Operation{
		OperationID: "listStoreProviders", Method: http.MethodGet, Path: "/api/v1/store-providers", Tags: []string{"settings"},
		Summary: "List the stores a `stores` field can name",
		Description: "The fixed list of stores (F-016, D-096), in display order (Amazon first; Open Library is not a store since 0.13.0). " +
			"It does not depend on the workspace.",
	}, httpserver.Op(log, listProviders))
}

// StoreProvidersOutput is the listStoreProviders answer.
type StoreProvidersOutput struct {
	Body StoreProvidersJSON
}

func listProviders(context.Context, *struct{}) (*StoreProvidersOutput, error) {
	list := Providers()
	out := StoreProvidersJSON{Providers: make([]StoreProviderJSON, 0, len(list))}
	for _, p := range list {
		out.Providers = append(out.Providers, StoreProviderJSON(p))
	}
	return &StoreProvidersOutput{Body: out}, nil
}
