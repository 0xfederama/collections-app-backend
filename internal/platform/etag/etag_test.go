package etag_test

import (
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/platform/etag"
)

func TestFormat(t *testing.T) {
	if got := etag.Format(etag.Item, 7); got != `"i-7"` {
		t.Errorf("Format = %s", got)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		header, prefix string
		want           int
		ok             bool
	}{
		{`"f-0"`, "f", 0, true},
		{`"i-12"`, "i", 12, true},
		{`"c-2147483647"`, "c", 2147483647, true},
		{``, "f", 0, false},
		{`*`, "f", 0, false},
		{`f-1`, "f", 0, false},
		{`"f-1`, "f", 0, false},
		{`W/"f-1"`, "f", 0, false},
		{`"i-1"`, "f", 0, false},
		{`"f-"`, "f", 0, false},
		{`"f-+1"`, "f", 0, false},
		{`"f--1"`, "f", 0, false},
		{`"f-01"`, "f", 0, false},
		{`"f-1.5"`, "f", 0, false},
		{`"f-1", "f-2"`, "f", 0, false},
		{`"f-99999999999999999999"`, "f", 0, false},
	}
	for _, tt := range tests {
		got, ok := etag.Parse(tt.header, tt.prefix)
		if ok != tt.ok || got != tt.want {
			t.Errorf("Parse(%q, %q) = %d, %v; want %d, %v", tt.header, tt.prefix, got, ok, tt.want, tt.ok)
		}
	}
}
