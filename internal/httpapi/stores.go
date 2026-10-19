package httpapi

import (
	"context"

	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/stores"
)

// ListStoreProviders returns the fixed store list, so clients need no copy of it (F-016).
func (s *Server) ListStoreProviders(_ context.Context, _ oapi.ListStoreProvidersRequestObject) (oapi.ListStoreProvidersResponseObject, error) {
	list := stores.Providers()
	providers := make([]oapi.StoreProvider, 0, len(list))
	for _, p := range list {
		providers = append(providers, oapi.StoreProvider{Key: oapi.StoreKey(p.Key), Name: p.Name})
	}
	return oapi.ListStoreProviders200JSONResponse{Providers: providers}, nil
}
