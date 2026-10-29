package scope

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestWithAndWorkspaceID(t *testing.T) {
	ws := uuid.New()
	if got := WorkspaceID(With(context.Background(), ws)); got != ws {
		t.Fatalf("WorkspaceID = %s, want %s", got, ws)
	}
}

func TestWorkspaceIDUnscopedIsNil(t *testing.T) {
	if got := WorkspaceID(context.Background()); got != uuid.Nil {
		t.Fatalf("WorkspaceID on an unscoped context = %s, want nil", got)
	}
}

func TestMiddlewareScopesEveryRequest(t *testing.T) {
	ws := uuid.New()
	var seen []uuid.UUID
	h := Middleware(ws)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, WorkspaceID(r.Context()))
	}))
	for range 2 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	}
	if len(seen) != 2 || seen[0] != ws || seen[1] != ws {
		t.Fatalf("handler saw %v, want %s twice", seen, ws)
	}
}
