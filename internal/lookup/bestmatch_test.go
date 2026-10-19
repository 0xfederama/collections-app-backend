package lookup

import "testing"

func TestPickBest(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		matches []Suggestion
		want    int
	}{
		{"empty list", "x", nil, 0},
		{"no exact title match keeps upstream first", "il vecchio e il mare", []Suggestion{
			{Title: "Il vecchio e il mare e altri racconti"}, {Title: "Il vecchio", Authors: "A", Pages: 9},
		}, 0},
		{"exact match with author beats first no-author result", "Il vecchio e il mare", []Suggestion{
			{Title: "Il vecchio e il mare"}, {Title: "Il vecchio e il mare", Authors: "Ernest Hemingway"},
		}, 1},
		{"author and pages beats author only", "dune", []Suggestion{
			{Title: "Dune", Authors: "Frank Herbert"}, {Title: "Dune", Authors: "Frank Herbert", Pages: 608},
		}, 1},
		{"accent, case and punctuation insensitive", "Perche' il Cielo e blu!", []Suggestion{
			{Title: "Altro", Authors: "A", Pages: 1}, {Title: "Perché il cielo è blu", Authors: "X", Pages: 100},
		}, 1},
		{"ties keep upstream order", "dune", []Suggestion{
			{Title: "Dune", Authors: "A"}, {Title: "Dune", Authors: "B"}, {Title: "Dune", Pages: 3},
		}, 0},
		{"non candidate never beats a candidate", "dune", []Suggestion{
			{Title: "Dune Messiah", Authors: "A", Pages: 5}, {Title: "Dune"},
		}, 1},
		{"isbn query has no title equal", "9780261103283", []Suggestion{
			{Title: "The Hobbit", Authors: "T", Pages: 1}, {Title: "Other"},
		}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickBest(tt.query, tt.matches); got != tt.want {
				t.Fatalf("pickBest = %d, want %d", got, tt.want)
			}
		})
	}
}
