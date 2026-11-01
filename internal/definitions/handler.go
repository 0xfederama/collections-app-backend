package definitions

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
)

// Register adds the listTypes operation. The built-in types are code (the
// registry in items/types), so there is no service, repository or write.
func Register(api huma.API, log *slog.Logger) {
	h := &handler{}
	huma.Register(api, huma.Operation{
		OperationID: "listTypes", Method: http.MethodGet, Path: "/api/v1/types", Tags: []string{"types"},
		Summary: "List the built-in types and their fields",
		Errors:  []int{http.StatusInternalServerError},
	}, httpserver.Op(log, h.list))
}

type handler struct{}

// ListTypesOutput is the listTypes answer.
type ListTypesOutput struct {
	Body []TypeDefinitionJSON
}

func (h *handler) list(_ context.Context, _ *struct{}) (*ListTypesOutput, error) {
	all := types.All()
	out := make([]TypeDefinitionJSON, 0, len(all))
	for _, d := range all {
		out = append(out, typeJSON(d))
	}
	return &ListTypesOutput{Body: out}, nil
}
