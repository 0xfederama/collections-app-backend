package items

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

// These tests are the unit form of the item rules of internal/httpapi/items_test.go
// (AT05 field rules, F007/F002 price rules, TestAT05_UpdateItem) and of
// internal/items/price_test.go. They need no database.

// unknownPath returns the path of an apperr.UnknownField error, "" for others.
func unknownPath(err error) string {
	var u *apperr.UnknownFieldError
	if errors.As(err, &u) {
		return u.Path
	}
	return ""
}

// validationFields returns the field names of an apperr.Validation error, nil for others.
func validationFields(err error) []string {
	var v *apperr.ValidationError
	if !errors.As(err, &v) {
		return nil
	}
	var out []string
	for _, fe := range v.Errors {
		out = append(out, fe.Field)
	}
	return out
}

func strp(s string) *string { return &s }

// movieType is the built-in movie type as the rules see it.
var movieType = types.Movie

// matrixMeta is the stored metadata of TestAT05_CreateItem's movie.
const matrixMeta = `{"release_year":1999,"runtime_minutes":136,"imdb_url":"https://www.imdb.com/title/tt0133093/","streaming":{"country":"IT","platforms":["Netflix","Prime Video"]}}`

func TestValidateMetadataRules(t *testing.T) {
	tests := []struct {
		name       string
		typ        *string
		fields     []types.Field
		in         map[string]any
		wantPath   string   // unknown_field path, when set
		wantFields []string // validation_failed fields, when set
		wantOK     bool
	}{
		{name: "valid movie metadata", typ: &movieType, fields: movieFields, in: map[string]any{
			"release_year": 1999.0, "runtime_minutes": 136.0, "imdb_url": "https://www.imdb.com/title/tt0133093/",
			"streaming": map[string]any{"country": "IT", "platforms": []any{"Netflix", "Prime Video"}},
		}, wantOK: true},
		{name: "empty object is valid", typ: &movieType, fields: movieFields, in: map[string]any{}, wantOK: true},
		{name: "nil metadata is an empty object", typ: &movieType, fields: movieFields, in: nil, wantOK: true},
		{name: "bad imdb url", typ: &movieType, fields: movieFields, in: map[string]any{"imdb_url": "http://imdb.com/title/tt1234567/"}, wantFields: []string{"metadata.imdb_url"}},
		{name: "unknown metadata key", typ: &movieType, fields: movieFields, in: map[string]any{"unknown_movie_field": "x"}, wantPath: "metadata.unknown_movie_field"},
		{name: "unknown nested streaming key", typ: &movieType, fields: movieFields, in: map[string]any{"streaming": map[string]any{"country": "IT", "x": 1.0}}, wantPath: "metadata.streaming.x"},
		{name: "a key of another type", typ: &movieType, fields: movieFields, in: map[string]any{"make": "VW"}, wantPath: "metadata.make"},
		{name: "no type has no fields", typ: nil, fields: movieFields, in: map[string]any{"release_year": 1999.0}, wantPath: "metadata.release_year"},
		{name: "no type takes an empty object", typ: nil, fields: movieFields, in: map[string]any{}, wantOK: true},
		{name: "custom number field", typ: strp("gadget"), fields: []types.Field{{Key: "rating", Type: "number"}}, in: map[string]any{"rating": 4.5}, wantOK: true},
		{name: "custom field unknown to the type", typ: strp("gadget"), fields: []types.Field{{Key: "rating", Type: "number"}}, in: map[string]any{"release_year": 1.0}, wantPath: "metadata.release_year"},
		{name: "null value is omitted", typ: &movieType, fields: movieFields, in: map[string]any{"director": nil}, wantOK: true},
		{name: "number field with a string", typ: &movieType, fields: movieFields, in: map[string]any{"release_year": "1999"}, wantFields: []string{"metadata.release_year"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := ValidateMetadata(tt.typ, tt.fields, tt.in, nil)
			switch {
			case tt.wantPath != "":
				if got := unknownPath(err); got != tt.wantPath {
					t.Fatalf("unknown path = %q (err %v), want %q", got, err, tt.wantPath)
				}
			case tt.wantFields != nil:
				if got := validationFields(err); !slices.Equal(got, tt.wantFields) {
					t.Fatalf("validation fields = %v (err %v), want %v", got, err, tt.wantFields)
				}
			case tt.wantOK:
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if out == nil {
					t.Fatal("a valid object is never nil")
				}
			}
		})
	}
}

func TestValidateMetadataStreamingLink(t *testing.T) {
	const link = "https://www.justwatch.com/it/movie/matrix"
	stored := map[string]any{"streaming": map[string]any{"country": "IT", "platforms": []any{"Netflix"}, "link": link}}

	t.Run("a client-sent link is dropped", func(t *testing.T) {
		in := map[string]any{"streaming": map[string]any{"country": "DE", "link": "https://www.justwatch.com/de/movie/evil"}}
		out, err := ValidateMetadata(&movieType, movieFields, in, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, has := out["streaming"].(map[string]any)["link"]; has {
			t.Fatalf("client link stored: %v", out)
		}
	})

	t.Run("the stored link is kept while the country is unchanged", func(t *testing.T) {
		in := map[string]any{"streaming": map[string]any{"country": "IT", "platforms": []any{"Netflix", "Prime Video"}}}
		out, err := ValidateMetadata(&movieType, movieFields, in, stored)
		if err != nil {
			t.Fatal(err)
		}
		if got := out["streaming"].(map[string]any)["link"]; got != link {
			t.Fatalf("link = %v, want the stored one", got)
		}
	})

	t.Run("the stored link is dropped when the country changes", func(t *testing.T) {
		in := map[string]any{"streaming": map[string]any{"country": "DE"}}
		out, err := ValidateMetadata(&movieType, movieFields, in, stored)
		if err != nil {
			t.Fatal(err)
		}
		if _, has := out["streaming"].(map[string]any)["link"]; has {
			t.Fatalf("link kept after a country change: %v", out)
		}
	})

	t.Run("the lookup import keeps the link it writes", func(t *testing.T) {
		in := map[string]any{"streaming": map[string]any{"country": "IT", "link": link}}
		out, err := ValidateImport(&movieType, movieFields, in)
		if err != nil {
			t.Fatal(err)
		}
		if got := out["streaming"].(map[string]any)["link"]; got != link {
			t.Fatalf("import dropped the link: %v", out)
		}
	})

	t.Run("a link that is not JustWatch is rejected", func(t *testing.T) {
		in := map[string]any{"streaming": map[string]any{"country": "IT", "link": "https://example.com/x"}}
		if _, err := ValidateImport(&movieType, movieFields, in); err == nil {
			t.Fatal("a non-JustWatch link was accepted")
		}
	})
}

func TestPriceAllowed(t *testing.T) {
	if err := PriceAllowed(true); err != nil {
		t.Fatalf("allowed type: %v", err)
	}
	// A type without a price, and a list with no type (allows_price false), are a 422 on price.
	if got := validationFields(PriceAllowed(false)); !slices.Equal(got, []string{"price"}) {
		t.Fatalf("PriceAllowed(false) fields = %v, want [price]", got)
	}
}

func TestCleanCreateFieldRules(t *testing.T) {
	long := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = 'a'
		}
		return string(b)
	}
	price := func(min, max *string, currency string) *Price {
		return &Price{Min: min, Max: max, Currency: currency}
	}
	tests := []struct {
		name   string
		in     CreateInput
		fields []string
	}{
		{"title empty", CreateInput{Title: ""}, []string{"title"}},
		{"title blank", CreateInput{Title: "   "}, []string{"title"}},
		{"title 301", CreateInput{Title: long(301)}, []string{"title"}},
		{"title 300 is kept", CreateInput{Title: long(300)}, nil},
		{"url javascript", CreateInput{Title: "t", Website: strp("javascript:alert(1)")}, []string{"website"}},
		{"url data", CreateInput{Title: "t", Website: strp("data:text/html,x")}, []string{"website"}},
		{"url ftp", CreateInput{Title: "t", Website: strp("ftp://example.com/a")}, []string{"website"}},
		{"url no scheme", CreateInput{Title: "t", Website: strp("example.com")}, []string{"website"}},
		{"url no host", CreateInput{Title: "t", Website: strp("https://")}, []string{"website"}},
		{"url empty", CreateInput{Title: "t", Website: strp("")}, []string{"website"}},
		{"url too long", CreateInput{Title: "t", Website: strp("https://example.com/" + long(2040))}, []string{"website"}},
		{"url upper scheme is accepted", CreateInput{Title: "t", Website: strp("HTTP://Example.com/a?b=c#d")}, nil},
		{"price min format", CreateInput{Title: "t", Price: price(strp("12.5"), nil, "EUR")}, []string{"price.min"}},
		{"price comma", CreateInput{Title: "t", Price: price(strp("12,50"), nil, "EUR")}, []string{"price.min"}},
		{"price negative", CreateInput{Title: "t", Price: price(strp("-1.00"), nil, "EUR")}, []string{"price.min"}},
		{"price 11 digits", CreateInput{Title: "t", Price: price(strp("12345678901.00"), nil, "EUR")}, []string{"price.min"}},
		{"price max format", CreateInput{Title: "t", Price: price(nil, strp("abc"), "EUR")}, []string{"price.max"}},
		{"price min above max", CreateInput{Title: "t", Price: price(strp("20.00"), strp("10.00"), "EUR")}, []string{"price.max"}},
		{"price both null", CreateInput{Title: "t", Price: price(nil, nil, "EUR")}, []string{"price"}},
		{"currency missing", CreateInput{Title: "t", Price: price(strp("1.00"), nil, "")}, []string{"price.currency"}},
		{"currency lowercase", CreateInput{Title: "t", Price: price(strp("1.00"), nil, "eur")}, []string{"price.currency"}},
		{"currency 2 letters", CreateInput{Title: "t", Price: price(strp("1.00"), nil, "EU")}, []string{"price.currency"}},
		{"amount format", CreateInput{Title: "t", Price: &Price{Amount: strp("12.5"), Currency: "EUR"}}, []string{"price.amount"}},
		{"amount without currency", CreateInput{Title: "t", Price: &Price{Amount: strp("12.50")}}, []string{"price.currency"}},
		{"amount with min", CreateInput{Title: "t", Price: &Price{Amount: strp("12.50"), Min: strp("10.00"), Currency: "EUR"}}, []string{"price"}},
		{"amount with max", CreateInput{Title: "t", Price: &Price{Amount: strp("12.50"), Max: strp("15.00"), Currency: "EUR"}}, []string{"price"}},
		{"price 0 to max JPY", CreateInput{Title: "t", Price: price(strp("0.00"), strp("9999999999.99"), "JPY")}, nil},
		{"equal min and max", CreateInput{Title: "t", Price: price(strp("5.00"), strp("5.00"), "EUR")}, nil},
		{"max only", CreateInput{Title: "t", Price: price(nil, strp("5.00"), "EUR")}, nil},
		{"amount alone", CreateInput{Title: "t", Price: &Price{Amount: strp("5.00"), Currency: "EUR"}}, nil},
		{"several errors at once", CreateInput{Title: "", Website: strp("ftp://x"), Price: price(strp("1"), strp("x"), "")},
			[]string{"title", "website", "price.min", "price.max", "price.currency"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := CleanCreate(tt.in)
			if got := validationFields(err); !slices.Equal(got, tt.fields) {
				t.Fatalf("fields = %v (err %v), want %v", got, err, tt.fields)
			}
			if err == nil && out.Title == "" {
				t.Fatal("a valid create has a title")
			}
		})
	}

	t.Run("title is trimmed and website normalised", func(t *testing.T) {
		out, err := CleanCreate(CreateInput{Title: "  Plain  ", Website: strp(" HTTPS://A.B ")})
		if err != nil {
			t.Fatal(err)
		}
		if out.Title != "Plain" || *out.Website != "HTTPS://A.B" {
			t.Fatalf("out = %q %q", out.Title, *out.Website)
		}
	})
}

func TestCleanPatchFieldRules(t *testing.T) {
	tests := []struct {
		name   string
		p      Patch
		fields []string
	}{
		{"empty patch", Patch{}, nil},
		{"title empty", Patch{Title: strp("")}, []string{"title"}},
		{"website javascript", Patch{Website: Optional[string]{Set: true, Value: strp("javascript:alert(1)")}}, []string{"website"}},
		{"website null clears", Patch{Website: Optional[string]{Set: true}}, nil},
		{"website empty string", Patch{Website: Optional[string]{Set: true, Value: strp("")}}, []string{"website"}},
		{"price min above max", Patch{Price: Optional[Price]{Set: true, Value: &Price{Min: strp("2.00"), Max: strp("1.00"), Currency: "EUR"}}}, []string{"price.max"}},
		{"price min only with no max is valid", Patch{Price: Optional[Price]{Set: true, Value: &Price{Min: strp("1.00"), Currency: "EUR"}}}, nil},
		{"price null clears", Patch{Price: Optional[Price]{Set: true}}, nil},
		{"price amount with max", Patch{Price: Optional[Price]{Set: true, Value: &Price{Amount: strp("1.00"), Max: strp("2.00"), Currency: "USD"}}}, []string{"price"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CleanPatch(tt.p)
			if got := validationFields(err); !slices.Equal(got, tt.fields) {
				t.Fatalf("fields = %v (err %v), want %v", got, err, tt.fields)
			}
		})
	}

	t.Run("title is trimmed", func(t *testing.T) {
		out, err := CleanPatch(Patch{Title: strp("  Solo 2  ")})
		if err != nil {
			t.Fatal(err)
		}
		if *out.Title != "Solo 2" {
			t.Fatalf("title = %q", *out.Title)
		}
	})
}

func TestValidatePrice(t *testing.T) {
	s := func(v string) *string { return &v }
	tests := []struct {
		name  string
		price Price
		bad   []string
	}{
		{"ok min only", Price{Min: s("1.00"), Currency: "EUR"}, nil},
		{"ok equal", Price{Min: s("2.00"), Max: s("2.00"), Currency: "EUR"}, nil},
		{"min above max", Price{Min: s("2.01"), Max: s("2.00"), Currency: "EUR"}, []string{"price.max"}},
		{"numeric not lexical order", Price{Min: s("10.00"), Max: s("9.00"), Currency: "EUR"}, []string{"price.max"}},
		{"neither", Price{Currency: "EUR"}, []string{"price"}},
		{"bad currency", Price{Min: s("1.00"), Currency: "eur"}, []string{"price.currency"}},
		{"all bad", Price{Min: s("1"), Max: s("x"), Currency: ""}, []string{"price.min", "price.max", "price.currency"}},
		{"TestF007 ok amount", Price{Amount: s("1299.00"), Currency: "EUR"}, nil},
		{"TestF007 amount bad format", Price{Amount: s("12.5"), Currency: "EUR"}, []string{"price.amount"}},
		{"TestF007 amount bad currency", Price{Amount: s("12.50"), Currency: "eur"}, []string{"price.currency"}},
		{"TestF007 amount with min", Price{Amount: s("12.50"), Min: s("10.00"), Currency: "EUR"}, []string{"price"}},
		{"TestF007 amount with max", Price{Amount: s("12.50"), Max: s("15.00"), Currency: "EUR"}, []string{"price"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validatePrice(&tt.price)
			if len(errs) != len(tt.bad) {
				t.Fatalf("errs = %v, want fields %v", errs, tt.bad)
			}
			for i, fe := range errs {
				if fe.Field != tt.bad[i] {
					t.Errorf("errs[%d].Field = %s, want %s", i, fe.Field, tt.bad[i])
				}
			}
		})
	}
}

func TestCleanWebsite(t *testing.T) {
	for _, ok := range []string{"http://a.b", "https://a.b/c?d=e#f", " HTTPS://A.B "} {
		if _, fe := cleanWebsite(ok); fe != nil {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"javascript:alert(1)", "JavaScript:alert(1)", "data:text/html,x", "file:///etc/passwd", "ftp://a.b", "//a.b", "a.b", "https://", "https:///x", "", "http://a b.c"} {
		if _, fe := cleanWebsite(bad); fe == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSamePriceAmountIsMinEqualsMax(t *testing.T) {
	amount := "1200.00"
	p := Price{Amount: &amount, Currency: "EUR"}
	// What is read back never carries an amount: min and max are equal, and the same price compares equal.
	back := &Price{Min: &amount, Max: &amount, Currency: "EUR"}
	if !samePrice(&p, back) {
		t.Error("an amount price must equal the same min == max price")
	}
	other := "1200.01"
	if samePrice(&p, &Price{Amount: &other, Currency: "EUR"}) || samePrice(&p, &Price{Amount: &amount, Currency: "USD"}) {
		t.Error("different amounts or currencies must differ")
	}
	if samePrice(&p, nil) || !samePrice(nil, nil) {
		t.Error("a nil price equals only nil")
	}
}

func TestMergePatch(t *testing.T) {
	const website = "https://www.warnerbros.com/movies/matrix"
	cur := Editable{
		Title:    "The Matrix",
		Website:  strp(website),
		Metadata: json.RawMessage(matrixMeta),
	}
	tests := []struct {
		name    string
		p       Patch
		changed bool
	}{
		{"an empty patch changes nothing", Patch{}, false},
		{"the same title changes nothing", Patch{Title: strp("The Matrix")}, false},
		{"the same website changes nothing", Patch{Website: Optional[string]{Set: true, Value: strp(website)}}, false},
		{"the same metadata in another key order changes nothing", Patch{Metadata: map[string]any{
			"streaming": map[string]any{"platforms": []any{"Netflix", "Prime Video"}, "country": "IT"}, "imdb_url": "https://www.imdb.com/title/tt0133093/",
			"runtime_minutes": 136.0, "release_year": 1999.0,
		}}, false},
		{"a new title changes", Patch{Title: strp("The Matrix Reloaded")}, true},
		{"a null website clears it", Patch{Website: Optional[string]{Set: true}}, true},
		{"new metadata replaces the whole object", Patch{Metadata: map[string]any{"runtime_minutes": 90.0}}, true},
		{"an empty metadata object replaces it with {}", Patch{Metadata: map[string]any{}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed, err := MergePatch(cur, &movieType, movieFields, tt.p)
			if err != nil {
				t.Fatal(err)
			}
			if changed != tt.changed {
				t.Fatalf("changed = %v, want %v", changed, tt.changed)
			}
			if !changed && !reflect.DeepEqual(got, cur) {
				t.Fatalf("an unchanged patch returned %+v", got)
			}
		})
	}

	t.Run("a patch keeps the fields it does not name", func(t *testing.T) {
		got, _, err := MergePatch(cur, &movieType, movieFields, Patch{Title: strp("Reloaded")})
		if err != nil {
			t.Fatal(err)
		}
		if got.Title != "Reloaded" || *got.Website != website || string(got.Metadata) != matrixMeta {
			t.Fatalf("untouched fields changed: %+v", got)
		}
	})

	t.Run("a null website clears the stored value", func(t *testing.T) {
		got, _, err := MergePatch(cur, &movieType, movieFields, Patch{Website: Optional[string]{Set: true}})
		if err != nil {
			t.Fatal(err)
		}
		if got.Website != nil {
			t.Fatalf("website = %v, want nil", *got.Website)
		}
	})

	t.Run("metadata is checked against the type", func(t *testing.T) {
		_, _, err := MergePatch(cur, &movieType, movieFields, Patch{Metadata: map[string]any{"make": "VW"}})
		if got := unknownPath(err); got != "metadata.make" {
			t.Fatalf("unknown path = %q (err %v)", got, err)
		}
	})

	t.Run("a round trip of the streaming object keeps the stored link and changes nothing", func(t *testing.T) {
		const link = "https://www.justwatch.com/it/movie/matrix"
		withLink := Editable{
			Title:    "The Matrix",
			Metadata: json.RawMessage(`{"streaming":{"country":"IT","platforms":["Netflix"],"link":"` + link + `"}}`),
		}
		patch := Patch{Metadata: map[string]any{"streaming": map[string]any{"country": "IT", "platforms": []any{"Netflix"}}}}
		got, changed, err := MergePatch(withLink, &movieType, movieFields, patch)
		if err != nil {
			t.Fatal(err)
		}
		if changed {
			t.Fatalf("a round trip changed the item: %s", got.Metadata)
		}
		var m map[string]any
		if err := json.Unmarshal(got.Metadata, &m); err != nil || m["streaming"].(map[string]any)["link"] != link {
			t.Fatalf("link lost: %s", got.Metadata)
		}
	})

	t.Run("a price is stored normalised, an amount as min and max", func(t *testing.T) {
		wish := Editable{Title: "gift", Metadata: json.RawMessage(`{}`)}
		got, changed, err := MergePatch(wish, nil, nil, Patch{Price: Optional[Price]{Set: true, Value: &Price{Amount: strp("999.90"), Currency: "USD"}}})
		if err != nil {
			t.Fatal(err)
		}
		if !changed || got.Price == nil || *got.Price.Min != "999.90" || *got.Price.Max != "999.90" || got.Price.Amount != nil || got.Price.Currency != "USD" {
			t.Fatalf("price = %+v", got.Price)
		}
		_, changed, _ = MergePatch(got, nil, nil, Patch{Price: Optional[Price]{Set: true, Value: &Price{Min: strp("999.90"), Max: strp("999.90"), Currency: "USD"}}})
		if changed {
			t.Fatal("min == max is the amount price: an identical price changed nothing")
		}
	})

	t.Run("a null price clears a stored price, a changed one replaces it", func(t *testing.T) {
		priced := Editable{Title: "gift", Price: &Price{Min: strp("1299.00"), Currency: "EUR"}, Metadata: json.RawMessage(`{}`)}
		got, changed, _ := MergePatch(priced, nil, nil, Patch{Price: Optional[Price]{Set: true}})
		if !changed || got.Price != nil {
			t.Fatalf("null price: changed=%v price=%+v", changed, got.Price)
		}
		got, changed, _ = MergePatch(priced, nil, nil, Patch{Price: Optional[Price]{Set: true, Value: &Price{Min: strp("1299.00"), Currency: "EUR"}}})
		if changed || got.Price == nil {
			t.Fatalf("the same price changed: %v %+v", changed, got.Price)
		}
	})
}
