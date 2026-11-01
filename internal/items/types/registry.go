package types

// Field is one field of a built-in type. Its position is its index in Def.Fields.
type Field struct {
	Key   string
	Label string
	Type  string // text, number, date, boolean, link, streaming or stores
}

// Def is one built-in type: the fixed facts every workspace shares. A nil
// DefaultIcon or DefaultSortField is "none".
type Def struct {
	Key              string
	Name             string
	DefaultIcon      *string
	AllowsPrice      bool
	DefaultSortField *string
	Fields           []Field
}

// builtins is the built-in set (exactly these five, F-018, D-105). Types are
// code, not data: no workspace can add, edit or remove one.
var builtins = []Def{
	{
		Key: "movie", Name: "Movies", DefaultSortField: ptr("release_year"),
		Fields: []Field{
			{"release_year", "Release year", "number"},
			{"runtime_minutes", "Runtime", "number"},
			{"director", "Director", "text"},
			{"imdb_url", "IMDb URL", "link"},
			{"streaming", "Streaming", "streaming"},
			{"tmdb_id", "TMDB ID", "number"},
			{"lookup_disabled", "Disable lookup", "boolean"},
		},
	},
	{
		Key: "tv_show", Name: "TV shows", DefaultSortField: ptr("first_air_year"),
		Fields: []Field{
			{"first_air_year", "First air year", "number"},
			{"end_year", "End year", "number"},
			{"seasons", "Seasons", "number"},
			{"episodes_per_season", "Episodes per season", "number"},
			{"episode_minutes", "Episode minutes", "number"},
			{"imdb_url", "IMDb URL", "link"},
			{"streaming", "Streaming", "streaming"},
			{"tmdb_id", "TMDB ID", "number"},
			{"lookup_disabled", "Disable lookup", "boolean"},
		},
	},
	{
		Key: "book", Name: "Books",
		Fields: []Field{
			{"author", "Writer", "text"},
			{"pages", "Pages", "number"},
			{"isbn", "ISBN", "text"},
			{"stores", "Where to get it", "stores"},
			{"openlibrary_id", "Open Library ID", "text"},
			{"lookup_disabled", "Disable lookup", "boolean"},
		},
	},
	{
		Key: "vehicle", Name: "Vehicles", DefaultIcon: ptr("i:car"),
		Fields: []Field{
			{"make", "Make", "text"},
			{"model", "Model", "text"},
			{"model_year", "Year", "number"},
			{"displacement_cc", "Engine size (cc)", "number"},
			{"power_hp", "Power (hp)", "number"},
		},
	},
	{Key: "wishlist", Name: "Wishlist", DefaultIcon: ptr("i:tag"), AllowsPrice: true},
}

// All returns the built-in types in display order. The caller may not modify them.
func All() []Def { return builtins }

// Lookup returns the built-in type with the key. ok is false for any other key.
func Lookup(key string) (Def, bool) {
	for _, d := range builtins {
		if d.Key == key {
			return d, true
		}
	}
	return Def{}, false
}

func ptr(s string) *string { return &s }
