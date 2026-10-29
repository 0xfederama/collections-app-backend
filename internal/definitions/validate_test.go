package definitions

import (
	"slices"
	"strings"
	"testing"
)

func TestIsFieldKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"rating", true},
		{"a", true},
		{"field_2", true},
		{"", false},
		{"2nd", false},
		{"_x", false},
		{"Rating", false},
		{"with-dash", false},
		{strings.Repeat("a", 32), true},
		{strings.Repeat("a", 33), false},
	}
	for _, tt := range tests {
		if got := isFieldKey(tt.key); got != tt.want {
			t.Errorf("isFieldKey(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}

func TestNormalizeTypeName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "trims", in: "  Board  ", want: "Board"},
		{name: "blank", in: "   ", wantErr: true},
		{name: "one rune", in: "é", want: "é"},
		{name: "100 runes", in: strings.Repeat("é", 100), want: strings.Repeat("é", 100)},
		{name: "101 runes", in: strings.Repeat("é", 101), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeTypeName(tt.in)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("normalizeTypeName(%q) = %q, %v", tt.in, got, err)
			}
		})
	}
}

func TestNormalizeFieldLabel(t *testing.T) {
	if got, err := normalizeFieldLabel("  Rating "); err != nil || got != "Rating" {
		t.Errorf("normalizeFieldLabel = %q, %v", got, err)
	}
	if _, err := normalizeFieldLabel(" "); err == nil {
		t.Error("blank label accepted")
	}
	if _, err := normalizeFieldLabel(strings.Repeat("x", 101)); err == nil {
		t.Error("101-character label accepted")
	}
}

func TestIsCustomFieldType(t *testing.T) {
	for _, typ := range []string{"text", "number", "date", "boolean", "link"} {
		if !isCustomFieldType(typ) {
			t.Errorf("isCustomFieldType(%q) = false", typ)
		}
	}
	for _, typ := range []string{"stores", "streaming", "Text", ""} {
		if isCustomFieldType(typ) {
			t.Errorf("isCustomFieldType(%q) = true", typ)
		}
	}
}

func TestFieldKeyFromLabel(t *testing.T) {
	tests := []struct {
		label   string
		want    string
		wantErr bool
	}{
		{label: "Player age!", want: "player_age"},
		{label: "2nd rating", want: "field_2nd_rating"},
		{label: "!!!", wantErr: true},
		{label: strings.Repeat("a", 40), want: strings.Repeat("a", 32)},
		// The 32nd character is a separator: it is trimmed, not kept.
		{label: strings.Repeat("a", 31) + " b", want: strings.Repeat("a", 31)},
	}
	for _, tt := range tests {
		got, err := fieldKeyFromLabel(tt.label)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("fieldKeyFromLabel(%q) = %q, %v; want %q", tt.label, got, err, tt.want)
		}
	}
}

func TestKeyFromLabel(t *testing.T) {
	tests := map[string]string{
		"Board game":   "board_game",
		"  --x--  ":    "x",
		"Año":          "a_o",
		"a   b":        "a_b",
		"Rating (1-5)": "rating_1_5",
	}
	for in, want := range tests {
		if got := keyFromLabel(in); got != want {
			t.Errorf("keyFromLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckNewTypeDefaults(t *testing.T) {
	tests := []struct {
		name      string
		in        CreateType
		wantField string // "" = accepted
	}{
		{name: "nothing", in: CreateType{Name: "x"}},
		{name: "icon emoji", in: CreateType{DefaultIcon: ptr("🎲")}},
		{name: "icon not valid", in: CreateType{DefaultIcon: ptr("dice")}, wantField: "default_icon"},
		{name: "sort price needs allows_price", in: CreateType{DefaultSortField: ptr("price")}, wantField: "default_sort_field"},
		{name: "sort price allowed", in: CreateType{AllowsPrice: true, DefaultSortField: ptr("price")}},
		{name: "sort not a key", in: CreateType{DefaultSortField: ptr("Rating")}, wantField: "default_sort_field"},
		{name: "three pins", in: CreateType{DefaultPinnedFields: &[]string{"a", "b", "c"}}},
		{name: "four pins", in: CreateType{DefaultPinnedFields: &[]string{"a", "b", "c", "d"}}, wantField: "default_pinned_fields"},
		{name: "pin duplicated", in: CreateType{DefaultPinnedFields: &[]string{"a", "a"}}, wantField: "default_pinned_fields[1]"},
		{name: "pin price needs allows_price", in: CreateType{DefaultPinnedFields: &[]string{"price"}}, wantField: "default_pinned_fields[0]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkNewTypeDefaults(tt.in)
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("checkNewTypeDefaults() = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("checkNewTypeDefaults() accepted an invalid default")
			}
		})
	}
}

func TestCheckDefaults(t *testing.T) {
	fields := []Field{{Key: "rating", Type: "number"}, {Key: "tags", Type: "stores"}}
	tests := []struct {
		name       string
		allowPrice bool
		sort       *string
		pins       *[]string
		wantFields []string
	}{
		{name: "nothing set"},
		{name: "sort on a field", sort: ptr("rating")},
		{name: "sort on a stores field", sort: ptr("tags"), wantFields: []string{"default_sort_field"}},
		{name: "pin on a stores field is allowed", pins: &[]string{"tags"}},
		{name: "pin on price without allows_price", pins: &[]string{"price"}, wantFields: []string{"default_pinned_fields[0]"}},
		{name: "pin on price with allows_price", allowPrice: true, pins: &[]string{"price"}},
		{name: "pin not a field", pins: &[]string{"nope"}, wantFields: []string{"default_pinned_fields[0]"}},
		{name: "pin not a key", pins: &[]string{"Rating"}, wantFields: []string{"default_pinned_fields[0]"}},
		{name: "pin duplicated", pins: &[]string{"rating", "rating"}, wantFields: []string{"default_pinned_fields[1]"}},
		{name: "pins over three", pins: &[]string{"rating", "tags", "rating", "x"}, wantFields: []string{"default_pinned_fields"}},
		{name: "sort and pins both wrong", sort: ptr("nope"), pins: &[]string{"nope"}, wantFields: []string{"default_sort_field", "default_pinned_fields[0]"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkDefaults(tt.allowPrice, fields, tt.sort, tt.pins)
			if tt.wantFields == nil {
				if err != nil {
					t.Fatalf("checkDefaults() = %v", err)
				}
				return
			}
			if got := validationFields(t, err); !slices.Equal(got, tt.wantFields) {
				t.Errorf("fields = %v, want %v", got, tt.wantFields)
			}
		})
	}
}

func TestSameKeySet(t *testing.T) {
	tests := []struct {
		a, b []string
		want bool
	}{
		{[]string{"a", "b"}, []string{"b", "a"}, true},
		{[]string{"a", "b"}, []string{"a", "a"}, false},
		{[]string{"a", "b"}, []string{"a"}, false},
		{nil, nil, true},
	}
	for _, tt := range tests {
		if got := sameKeySet(tt.a, tt.b); got != tt.want {
			t.Errorf("sameKeySet(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestSameOptional(t *testing.T) {
	if !sameOptionalString(nil, nil) || sameOptionalString(nil, ptr("")) || !sameOptionalString(ptr("x"), ptr("x")) {
		t.Error("sameOptionalString")
	}
	if !sameStringList(nil, nil) || sameStringList(nil, &[]string{}) || !sameStringList(&[]string{"a"}, &[]string{"a"}) {
		t.Error("sameStringList")
	}
}
