package problem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

func TestFrom(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		check      func(t *testing.T, p *Problem)
	}{
		{"not found", apperr.NotFound("item"), 404, "not_found", func(t *testing.T, p *Problem) {
			if p.Detail != "Item not found." {
				t.Errorf("detail = %q", p.Detail)
			}
		}},
		{"not found without a resource name", apperr.NotFound(""), 404, "not_found", func(t *testing.T, p *Problem) {
			if p.Detail != "Resource not found." {
				t.Errorf("detail = %q", p.Detail)
			}
		}},
		{"version conflict", apperr.VersionConflict(8), 412, "version_conflict", func(t *testing.T, p *Problem) {
			if p.CurrentVersion == nil || *p.CurrentVersion != 8 {
				t.Errorf("current_version = %v, want 8", p.CurrentVersion)
			}
		}},
		{"version conflict at 0 still reports the version", apperr.VersionConflict(0), 412, "version_conflict", func(t *testing.T, p *Problem) {
			if p.CurrentVersion == nil || *p.CurrentVersion != 0 {
				t.Errorf("current_version = %v, want 0", p.CurrentVersion)
			}
		}},
		{"validation", apperr.Validation(apperr.FieldError{Field: "price.max", Message: "must be >= min"}, apperr.FieldError{Field: "title", Message: "required"}), 422, "validation_failed", func(t *testing.T, p *Problem) {
			if len(p.Errors) != 2 || p.Errors[0].Field != "price.max" || p.Errors[1].Message != "required" {
				t.Errorf("errors = %+v", p.Errors)
			}
		}},
		{"unknown field", apperr.UnknownField("metadata.runtime"), 400, "unknown_field", func(t *testing.T, p *Problem) {
			if p.Detail != `Unknown field "metadata.runtime".` {
				t.Errorf("detail = %q", p.Detail)
			}
		}},
		{"cycle", apperr.Cycle(), 409, "cycle_detected", func(t *testing.T, p *Problem) {
			if p.Title != "Cycle detected" {
				t.Errorf("title = %q", p.Title)
			}
		}},
		{"parent in trash", apperr.ParentInTrash(apperr.BlockingParent{Type: "folder", ID: "f1", Name: "Movies"}), 409, "parent_in_trash", func(t *testing.T, p *Problem) {
			if p.BlockingParent == nil || *p.BlockingParent != (BlockingParent{Type: "folder", ID: "f1", Name: "Movies"}) {
				t.Errorf("blocking_parent = %+v", p.BlockingParent)
			}
		}},
		{"destination unavailable", apperr.DestinationUnavailable(), 409, "destination_unavailable", func(t *testing.T, p *Problem) {
			if p.Title != "Destination unavailable" {
				t.Errorf("title = %q", p.Title)
			}
		}},
		{"type mismatch", apperr.TypeMismatch(), 409, "type_mismatch", func(t *testing.T, p *Problem) {
			if p.Title != "Type mismatch" {
				t.Errorf("title = %q", p.Title)
			}
		}},
		{"order stale", apperr.OrderStale(), 409, "order_stale", func(t *testing.T, p *Problem) {
			if p.Title != "Order is stale" {
				t.Errorf("title = %q", p.Title)
			}
		}},
		{"unnamed conflict code uses the generic conflict", &apperr.ConflictError{Code: "other"}, 409, "other", func(t *testing.T, p *Problem) {
			if p.Title != "Conflict" || p.Detail != "The request conflicts with the current state." {
				t.Errorf("problem = %+v", p)
			}
		}},
		{"payload too large", apperr.PayloadTooLarge(1), 413, "payload_too_large", nil},
		{"MaxBytesError from the reader", &http.MaxBytesError{Limit: 1}, 413, "payload_too_large", nil},
		{"unsupported media type", apperr.UnsupportedMediaType("image/svg+xml"), 415, "unsupported_media_type", func(t *testing.T, p *Problem) {
			if p.Detail != "Only JPEG, PNG and WebP images are accepted." {
				t.Errorf("detail = %q", p.Detail)
			}
		}},
		{"lookup disabled", apperr.LookupDisabled(), 503, "lookup_disabled", nil},
		{"upstream unavailable", apperr.UpstreamUnavailable(), 503, "upstream_unavailable", nil},
		{"wrapped domain error", fmt.Errorf("move: %w", apperr.Cycle()), 409, "cycle_detected", nil},
		{"joined domain error", errors.Join(errors.New("ctx"), apperr.NotFound("list")), 404, "not_found", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, unexpected := From(tt.err, "rid")
			if unexpected {
				t.Fatal("reported as unexpected")
			}
			if p.Status != tt.wantStatus || p.Code != tt.wantCode {
				t.Fatalf("got %d %s, want %d %s", p.Status, p.Code, tt.wantStatus, tt.wantCode)
			}
			if p.RequestID != "" {
				t.Errorf("request_id %q set on a non-500 problem", p.RequestID)
			}
			if want := typeBase + strings.ReplaceAll(tt.wantCode, "_", "-"); p.Type != want {
				t.Errorf("type = %q, want %q", p.Type, want)
			}
			if tt.check != nil {
				tt.check(t, p)
			}
			assertWireShape(t, p)
		})
	}
}

func TestFrom_UnknownErrorIsOpaque(t *testing.T) {
	secret := errors.New(`pq: relation "items" does not exist at /srv/app/internal/items/repo.go:42 SELECT * FROM items`)
	p, unexpected := From(secret, "req-1")
	if !unexpected || p.Status != 500 || p.Code != "internal" || p.RequestID != "req-1" {
		t.Fatalf("got %+v unexpected=%v", p, unexpected)
	}
	if p.Detail != "Something went wrong." {
		t.Errorf("detail = %q", p.Detail)
	}
	assertNoInternals(t, mustJSON(t, p))
}

func TestFromHuma(t *testing.T) {
	detail := func(loc, msg string) error {
		return &huma.ErrorDetail{Location: loc, Message: msg, Value: "secret-body"}
	}
	tests := []struct {
		name       string
		status     int
		errs       []error
		wantStatus int
		wantCode   string
		wantDetail string
	}{
		{"unexpected property at the body root", 422, []error{detail("body.bogus", msgUnexpectedProperty)}, 400, "unknown_field", `Unknown field "bogus".`},
		{"unexpected property in a nested object", 422, []error{detail("body.metadata.runtime", msgUnexpectedProperty)}, 400, "unknown_field", `Unknown field "metadata.runtime".`},
		{"bad path value", 400, []error{detail("path.id", "expected integer")}, 400, "invalid_parameter", `"id" is invalid.`},
		{"bad query value", 422, []error{detail("query.n", "expected integer")}, 400, "invalid_parameter", `"n" is invalid.`},
		{"bad header value", 422, []error{detail("header.X-Limit", "expected integer")}, 400, "invalid_parameter", `"X-Limit" is invalid.`},
		{"body that is not JSON", 400, []error{detail("body", "invalid character 's' looking for beginning of value")}, 400, "malformed_request", detailNotJSON},
		{"wrong content type", 415, []error{detail("body", "unknown content type: text/plain")}, 400, "malformed_request", detailUnsupportedContentType},
		{"body over the limit", 413, []error{errors.New("request body is too large limit=10 bytes")}, 413, "payload_too_large", "The request body exceeds the limit for this route."},
		{"field rules", 422, []error{detail("body.title", "expected string"), detail("body.count", "expected integer")}, 422, "validation_failed", "One or more fields are invalid."},
		{"handler error mapped as a service error", 500, []error{apperr.NotFound("item")}, 404, "not_found", "Item not found."},
		{"unknown handler error is opaque", 500, []error{errors.New("pq: SELECT secret")}, 500, "internal", "Something went wrong."},
		{"status without details keeps its standard text", 406, nil, 406, "not_acceptable", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fromHuma(context.Background(), tt.status, tt.errs)
			if p.Status != tt.wantStatus || p.Code != tt.wantCode {
				t.Fatalf("got %d %s, want %d %s", p.Status, p.Code, tt.wantStatus, tt.wantCode)
			}
			if p.Detail != tt.wantDetail {
				t.Errorf("detail = %q, want %q", p.Detail, tt.wantDetail)
			}
			assertNoInternals(t, mustJSON(t, p))
		})
	}
}

func TestFromHuma_FieldErrorsKeepTheirPath(t *testing.T) {
	p := fromHuma(context.Background(), 422, []error{
		&huma.ErrorDetail{Location: "body.title", Message: "expected string"},
		&huma.ErrorDetail{Location: "body.count", Message: "expected integer"},
	})
	if len(p.Errors) != 2 || p.Errors[0] != (FieldError{Field: "title", Message: "expected string"}) {
		t.Fatalf("errors = %+v", p.Errors)
	}
}

func TestFromHuma_ProblemIsWrittenAsIs(t *testing.T) {
	want := Malformed("kept")
	if got := fromHuma(context.Background(), 400, []error{want}); got != want {
		t.Fatalf("got %+v, want the same problem", got)
	}
}

func TestFromHuma_InternalCarriesTheRequestID(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-9")
	p := fromHuma(ctx, 500, []error{errors.New("boom")})
	if p.Code != "internal" || p.RequestID != "req-9" {
		t.Fatalf("got %+v", p)
	}
	if got := RequestIDFrom(context.Background()); got != "" {
		t.Fatalf("RequestIDFrom on a plain context = %q", got)
	}
}

func TestInstall_HumaBuildsProblems(t *testing.T) {
	Install()
	Install() // idempotent
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusInternalServerError} {
		if _, ok := huma.NewError(status, "x").(*Problem); !ok {
			t.Fatalf("huma.NewError(%d) is not a *Problem", status)
		}
	}
}

// assertWireShape checks the members every problem carries and that it encodes
// as a JSON object.
func assertWireShape(t *testing.T, p *Problem) {
	t.Helper()
	if p.ContentType("application/json") != "application/problem+json" {
		t.Errorf("content type = %q", p.ContentType("application/json"))
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(mustJSON(t, p)), &back); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"type", "title", "status", "code"} {
		if _, ok := back[k]; !ok {
			t.Errorf("body lacks %q: %v", k, back)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertNoInternals(t *testing.T, body string) {
	t.Helper()
	for _, bad := range []string{"pq:", "relation", "SELECT", "/srv/", ".go", "goroutine", "panic", "secret-body"} {
		if strings.Contains(body, bad) {
			t.Errorf("body leaks %q: %s", bad, body)
		}
	}
}
