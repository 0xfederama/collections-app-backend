package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

type opIn struct {
	Kind string `query:"kind"`
}

type opOut struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

// newOpAPI mounts one GET operation whose handler fails as ?kind asks, behind
// the full middleware stack.
func newOpAPI(log *slog.Logger) http.Handler {
	r := chi.NewRouter()
	api := NewAPI(r)
	huma.Register(api, huma.Operation{OperationID: "op", Method: http.MethodGet, Path: "/op"},
		Op(log, func(_ context.Context, in *opIn) (*opOut, error) {
			switch in.Kind {
			case "notfound":
				return nil, apperr.NotFound("item")
			case "internal":
				return nil, errors.New(`pq: relation "items" does not exist SELECT secret`)
			case "canceled":
				return nil, fmt.Errorf("query: %w", context.Canceled)
			case "problem":
				return nil, problem.New(http.StatusConflict, "custom", "Custom", "Already answered.")
			}
			out := &opOut{}
			out.Body.OK = true
			return out, nil
		}))
	return Wrap(r, log)
}

func TestOp_ErrorsBecomeProblems(t *testing.T) {
	tests := []struct {
		name          string
		kind          string
		cancelRequest bool // the client has gone before the handler returns
		wantStatus    int
		wantCode      string
		wantDetail    string
		wantRequestID bool // only the generic 500 carries the id
		wantLevel     string
		wantLog       []string // substrings the log must hold
		wantNotInBody []string // substrings the response must not hold
	}{
		{"success is not a problem", "", false, http.StatusOK, "", "", false, "", nil, nil},
		{"domain error is mapped", "notfound", false, http.StatusNotFound, "not_found", "Item not found.", false, "", nil, nil},
		{"problem from the handler passes through", "problem", false, http.StatusConflict, "custom", "Already answered.", false, "", nil, nil},
		{"unexpected error is logged and sanitised", "internal", false, http.StatusInternalServerError, "internal",
			"Something went wrong.", true, "ERROR", []string{"unhandled error", "SELECT secret"},
			[]string{"pq:", "SELECT", "relation"}},
		{"canceled client answers 499 at DEBUG", "canceled", true, 499, "client_closed_request",
			"The client closed the request before the answer was ready.", false, "DEBUG",
			[]string{"request canceled by the client"}, []string{"SELECT", "context canceled"}},
		{"canceled error on a live request is still a 500", "canceled", false, http.StatusInternalServerError, "internal",
			"Something went wrong.", true, "ERROR", []string{"unhandled error"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			h := newOpAPI(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/op?kind="+tt.kind, nil)
			req.Header.Set("X-Request-Id", "req-op")
			if tt.cancelRequest {
				ctx, cancel := context.WithCancel(req.Context())
				cancel()
				req = req.WithContext(ctx)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body)
			}
			out := logs.String()
			if tt.wantStatus == http.StatusOK {
				if strings.Contains(out, `"level":"ERROR"`) || rec.Header().Get("Content-Type") == "application/problem+json" {
					t.Errorf("success logged an error or wrote a problem: %s", out)
				}
				return
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q", ct)
			}
			var p problem.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatalf("not a problem: %s (%v)", rec.Body, err)
			}
			if p.Code != tt.wantCode || p.Detail != tt.wantDetail {
				t.Errorf("problem = %+v, want code %s detail %q", p, tt.wantCode, tt.wantDetail)
			}
			if tt.wantRequestID != (p.RequestID == "req-op") {
				t.Errorf("request_id = %q, want set=%v", p.RequestID, tt.wantRequestID)
			}
			for _, bad := range tt.wantNotInBody {
				if strings.Contains(rec.Body.String(), bad) {
					t.Errorf("body leaks %q: %s", bad, rec.Body)
				}
			}
			for _, want := range tt.wantLog {
				if !strings.Contains(out, want) {
					t.Errorf("log lacks %q: %s", want, out)
				}
			}
			// The handler's own line is at wantLevel; no line may be at ERROR unless expected.
			if got := strings.Contains(out, `"level":"ERROR"`); got != (tt.wantLevel == "ERROR") {
				t.Errorf("ERROR line logged = %v, want %v: %s", got, tt.wantLevel == "ERROR", out)
			}
			if tt.wantLevel != "" && !strings.Contains(out, `"level":"`+tt.wantLevel+`"`) {
				t.Errorf("no %s line in the log: %s", tt.wantLevel, out)
			}
		})
	}
}
