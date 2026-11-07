package tmdb

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestR28DirectorsCappedToTextLimit(t *testing.T) {
	var crew []string
	for _, n := range []string{"Samuel Armstrong", "James Algar", "Bill Roberts", "Paul Satterfield", "Hamilton Luske", "Jim Handley", "Ford Beebe", "T. Hee", "Norman Ferguson", "Wilfred Jackson", "Pixie Duster", "Extra Person"} {
		crew = append(crew, `{"job":"Director","name":"`+n+`"}`)
	}
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":756,"title":"Fantasia","release_date":"1940-11-13","credits":{"crew":[` + strings.Join(crew, ",") + `]}}`))
	})
	d, err := c.Details(context.Background(), "movie", 756, "en-US", "US")
	if err != nil {
		t.Fatal(err)
	}
	dir := d.Metadata["director"].(string)
	if n := len([]rune(dir)); n == 0 || n > 100 || !strings.HasPrefix(dir, "Samuel Armstrong, James Algar, ") || strings.HasSuffix(dir, ",") || strings.HasSuffix(dir, " ") {
		t.Fatalf("director = %q (%d runes)", dir, len([]rune(dir)))
	}
}

func TestR28JoinNamesTruncatesLongFirstName(t *testing.T) {
	if got := joinNames([]string{strings.Repeat("é", 150), "B"}, 100); len([]rune(got)) != 100 {
		t.Fatalf("long first name = %d runes", len([]rune(got)))
	}
}
