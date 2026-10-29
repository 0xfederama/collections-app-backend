package lists

import (
	"errors"
	"strings"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

func TestNormalizePatch(t *testing.T) {
	tests := []struct {
		name string
		in   Patch
		// wantFields are the fields of the validation errors in order; nil means no error.
		wantFields []string
		// wantTooLarge is set for the 413 description limit.
		wantTooLarge bool
		check        func(t *testing.T, p Patch)
	}{
		{
			name: "name is trimmed", in: Patch{Name: strp("  Renamed  ")},
			check: func(t *testing.T, p Patch) {
				if *p.Name != "Renamed" {
					t.Fatalf("name = %q", *p.Name)
				}
			},
		},
		{name: "blank name", in: Patch{Name: strp("   ")}, wantFields: []string{"name"}},
		{name: "name over 200 characters", in: Patch{Name: strp(strings.Repeat("x", 201))}, wantFields: []string{"name"}},
		{
			name: "200 multi-byte characters are a valid name", in: Patch{Name: strp(strings.Repeat("é", 200))},
			check: func(t *testing.T, p Patch) {
				if *p.Name != strings.Repeat("é", 200) {
					t.Fatal("name changed")
				}
			},
		},
		{
			name: "empty description becomes nil", in: Patch{Description: Optional[string]{Set: true, Value: strp("")}},
			check: func(t *testing.T, p Patch) {
				if !p.Description.Set || p.Description.Value != nil {
					t.Fatalf("description = %+v, want set to nil", p.Description)
				}
			},
		},
		{
			name: "description with NUL is 422", in: Patch{Description: Optional[string]{Set: true, Value: strp("a\x00b")}},
			wantFields: []string{"description"},
		},
		{
			name: "description over 64 KiB is 413", in: Patch{Description: Optional[string]{Set: true, Value: strp(strings.Repeat("x", 64<<10+1))}},
			wantTooLarge: true,
		},
		{
			// A description error is returned at once, before the name and enum errors are collected.
			name: "description error wins over name and enum errors", in: Patch{
				Name: strp(""), DisplayMode: strp("huge"), Description: Optional[string]{Set: true, Value: strp("a\x00b")},
			},
			wantFields: []string{"description"},
		},
		{
			name: "name and display mode errors are collected in order", in: Patch{Name: strp(""), DisplayMode: strp("huge")},
			wantFields: []string{"name", "display_mode"},
		},
		{
			name: "sort mode has no enum check here", in: Patch{SortMode: strp("whatever")},
		},
		{
			name: "blank type key is 422", in: Patch{Type: Optional[string]{Set: true, Value: strp("  ")}},
			wantFields: []string{"type"},
		},
		{
			name: "no type (null) is fine", in: Patch{Type: Optional[string]{Set: true}},
		},
		{
			name: "absent fields are left alone", in: Patch{Name: nil},
			check: func(t *testing.T, p Patch) {
				if p.Name != nil || p.Description.Set || p.Type.Set {
					t.Fatalf("patch changed: %+v", p)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizePatch(tt.in)
			if tt.wantTooLarge {
				var tl *apperr.PayloadTooLargeError
				if !errors.As(err, &tl) {
					t.Fatalf("err = %v, want payload too large", err)
				}
				return
			}
			if tt.wantFields == nil {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if tt.check != nil {
					tt.check(t, got)
				}
				return
			}
			var ve *apperr.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want validation error on %v", err, tt.wantFields)
			}
			var fields []string
			for _, fe := range ve.Errors {
				fields = append(fields, fe.Field)
			}
			if strings.Join(fields, ",") != strings.Join(tt.wantFields, ",") {
				t.Fatalf("fields = %v, want %v", fields, tt.wantFields)
			}
		})
	}
}

func TestValidateListFieldsAndUnknownType(t *testing.T) {
	name, errs := validateListFields(strp("  Films  "), strp("grid"), strp("title"))
	if name != "Films" || len(errs) != 0 {
		t.Fatalf("validateListFields = %q, %v; want Films and no errors", name, errs)
	}
	if _, errs := validateListFields(strp(" "), strp("huge"), nil); len(errs) != 2 || errs[0].Field != "name" || errs[1].Field != "display_mode" {
		t.Fatalf("validateListFields errors = %+v, want name and display_mode", errs)
	}

	var ve *apperr.ValidationError
	if !errors.As(unknownListType(), &ve) || len(ve.Errors) != 1 || ve.Errors[0].Field != "type" {
		t.Fatalf("unknownListType = %v, want a validation error on type", unknownListType())
	}
}
