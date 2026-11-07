package problem

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

// TestCodesCoverEveryEmittedCode fails when From or one of the helpers can emit
// a code that Codes does not list (the list is the contract's code table).
func TestCodesCoverEveryEmittedCode(t *testing.T) {
	listed := map[string]bool{}
	for _, c := range Codes {
		listed[c.Code] = true
	}

	var emitted []*Problem
	for _, err := range []error{
		apperr.NotFound("item"),
		apperr.VersionConflict(1),
		apperr.Validation(apperr.FieldError{Field: "title", Message: "required"}),
		apperr.UnknownField("metadata.x"),
		apperr.Cycle(),
		apperr.ParentInTrash(apperr.BlockingParent{Type: "folder", ID: "f", Name: "n"}),
		apperr.DestinationUnavailable(),
		apperr.TypeMismatch(),
		apperr.DuplicateTitle(),
		apperr.OrderStale(),
		apperr.PayloadTooLarge(10),
		apperr.UnsupportedMediaType("svg"),
		apperr.LookupDisabled(),
		apperr.UpstreamUnavailable(),
		apperr.FaviconNotFound(),
		errors.New("unexpected"),
	} {
		p, _ := From(err, "req-1")
		emitted = append(emitted, p)
	}
	emitted = append(emitted,
		Malformed("d"), InvalidParameter("id"), UnknownField("body.x"), PreconditionRequired(),
		PayloadTooLarge(), UnsupportedContentType(), internal("req-1"),
	)

	// Huma's own answers: a detail-carrying error and the status-only fallback
	// for each status the contract documents.
	detail := func(loc, msg string) error { return &huma.ErrorDetail{Location: loc, Message: msg} }
	for _, c := range []struct {
		status int
		errs   []error
	}{
		{400, []error{detail("path.id", "expected integer")}},
		{400, []error{detail("body", "expected object")}},
		{422, []error{detail("body.x", msgUnexpectedProperty)}},
		{422, []error{detail("body.title", "expected length >= 1")}},
		{422, []error{detail("body", "expected required property title to be present")}},
		{415, []error{detail("body", "unknown content type")}},
		{413, nil},
		{400, nil}, {404, nil}, {409, nil}, {412, nil}, {422, nil}, {428, nil}, {500, nil}, {503, nil},
	} {
		emitted = append(emitted, fromHuma(context.Background(), c.status, c.errs))
	}

	for _, p := range emitted {
		if !listed[p.Code] {
			t.Errorf("code %q (status %d) is emitted but not listed in Codes", p.Code, p.Status)
		}
	}
}

func TestCodesAreUniqueAndDescribed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Codes {
		if c.Code == "" || c.Meaning == "" {
			t.Errorf("code entry %+v is missing its code or meaning", c)
		}
		if seen[c.Code] {
			t.Errorf("code %q is listed twice", c.Code)
		}
		seen[c.Code] = true
	}
}

// The Problem schema description names every code, so the contract cannot drift
// from the table.
func TestProblemSchemaListsEveryCode(t *testing.T) {
	reg := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	reg.Schema(reflect.TypeFor[Problem](), true, "")
	s := reg.SchemaFromRef("#/components/schemas/Problem")
	if s == nil {
		t.Fatal("Problem is not registered as a schema")
	}
	for _, c := range Codes {
		if !strings.Contains(s.Description, "`"+c.Code+"`") {
			t.Errorf("Problem description does not list %q", c.Code)
		}
	}
}
