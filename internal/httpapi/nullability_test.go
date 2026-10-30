package httpapi_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/0xfederama/collections-app-backend/internal/content"
	"github.com/0xfederama/collections-app-backend/internal/definitions"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/lists"
	"github.com/0xfederama/collections-app-backend/internal/lookup"
	"github.com/0xfederama/collections-app-backend/internal/search"
	"github.com/0xfederama/collections-app-backend/internal/trash"
	"github.com/0xfederama/collections-app-backend/internal/tree"
)

// isNullable reports whether the schema accepts null.
func isNullable(r huma.Registry, s *huma.Schema) bool {
	if s.Ref != "" {
		s = r.SchemaFromRef(s.Ref)
	}
	if s.Nullable || s.Type == "null" {
		return true
	}
	for _, b := range append(append([]*huma.Schema{}, s.OneOf...), s.AnyOf...) {
		if isNullable(r, b) {
			return true
		}
	}
	return false
}

// TestWireNullabilityMatchesSpec fails when a response field that can be nil on
// the wire (pointer, slice, map, interface, no omitempty) is not declared
// nullable in the generated schema, unless the field says nullable:"false" to
// promise that the handler never sends null.
func TestWireNullabilityMatchesSpec(t *testing.T) {
	roots := []reflect.Type{
		reflect.TypeFor[tree.Tree](),
		reflect.TypeFor[items.ItemJSON](),
		reflect.TypeFor[items.ItemPageJSON](),
		reflect.TypeFor[lists.ListWithPathJSON](),
		reflect.TypeFor[search.SearchPageJSON](),
		reflect.TypeFor[search.RecentItemsJSON](),
		reflect.TypeFor[trash.TrashPageJSON](),
		reflect.TypeFor[definitions.TypeDefinitionJSON](),
		reflect.TypeFor[content.ContentVersionListJSON](),
		reflect.TypeFor[lookup.TmdbSuggestionsJSON](),
		reflect.TypeFor[lookup.BookSuggestionsJSON](),
	}
	r := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	seen := map[reflect.Type]bool{}
	var walk func(typ reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		s := r.Schema(typ, false, "")
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if f.Anonymous && name == "" {
				walk(f.Type)
				continue
			}
			if name == "" || name == "-" || strings.Contains(opts, "omitempty") {
				continue
			}
			walk(f.Type)
			switch f.Type.Kind() {
			case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface:
			default:
				continue
			}
			if f.Tag.Get("nullable") == "false" {
				continue
			}
			prop := s.Properties[name]
			if prop == nil {
				t.Errorf("%s.%s: no schema property %q", typ, f.Name, name)
			} else if !isNullable(r, prop) {
				t.Errorf("%s.%s can be nil on the wire but the spec says non-null", typ, f.Name)
			}
		}
	}
	for _, root := range roots {
		walk(root)
	}
}
