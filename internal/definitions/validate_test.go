package definitions

import (
	"strings"
	"testing"
)

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
