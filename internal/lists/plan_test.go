package lists

import (
	"errors"
	"reflect"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

// Fixtures: the definitions of the built-in types the update rules meet (fields abridged).
var (
	movieDef    = TypeInfo{Key: "movie", DefaultSortField: strp("release_year")}
	movieFields = []FieldInfo{{Key: "release_year", FieldType: "number"}, {Key: "streaming", FieldType: "text"}}
	vehicleDef  = TypeInfo{Key: "vehicle"}
	// stores is a field whose values are not sortable (validateDefinitionSort skips it).
	vehicleFields = []FieldInfo{
		{Key: "make", FieldType: "text"}, {Key: "displacement_cc", FieldType: "number"},
		{Key: "stores", FieldType: "stores"},
	}
	wishlistDef = TypeInfo{Key: "wishlist", AllowsPrice: true}
)

func strp(s string) *string { return &s }

// movieList is a list of type movie sorted by its default.
func movieList() List {
	return List{
		Name: "Films", Type: strp("movie"), DisplayMode: "thumbnail", SortMode: "release_year",
	}
}

// vehicleList is a list of type vehicle sorted manually.
func vehicleList() List {
	return List{Name: "Bikes", Type: strp("vehicle"), DisplayMode: "thumbnail", SortMode: "manual"}
}

type planCase struct {
	name string
	cur  List
	def  TypeInfo
	// fields are the fields of def (nil for no type).
	fields []FieldInfo
	patch  Patch
	want   updatePlan
	// wantErr is the field of the expected validation error; "" means the plan is returned.
	wantErr string
}

func TestPlanUpdate(t *testing.T) {
	tests := []planCase{
		// No change: nothing is written, the version does not move.
		{name: "empty patch is a no-op", cur: movieList(), def: movieDef, fields: movieFields, patch: Patch{}, want: updatePlan{}},
		{
			name: "stored values sent again are a no-op", cur: movieList(), def: movieDef, fields: movieFields,
			patch: Patch{
				Name: strp("Films"), Type: Optional[string]{Set: true, Value: strp("movie")}, Description: Optional[string]{Set: true},
				DisplayMode: strp("thumbnail"), SortMode: strp("release_year"),
				Icon: Optional[string]{Set: true}, IconColor: Optional[string]{Set: true},
			},
			want: updatePlan{},
		},
		{
			name: "same icon and colour are a no-op", cur: func() List {
				l := movieList()
				l.Icon, l.IconColor = strp("i:film"), strp("teal")
				return l
			}(),
			def: movieDef, fields: movieFields,
			patch: Patch{Icon: Optional[string]{Set: true, Value: strp("i:film")}, IconColor: Optional[string]{Set: true, Value: strp("teal")}},
			want:  updatePlan{},
		},

		// Fields of the list.
		{
			name: "rename writes the row", cur: movieList(), def: movieDef, fields: movieFields,
			patch: Patch{Name: strp("Renamed")},
			want:  updatePlan{Write: true, Name: "Renamed", DisplayMode: "thumbnail", SortMode: "release_year"},
		},
		{
			name: "description cleared by null", cur: func() List {
				l := movieList()
				l.Description = strp("notes")
				return l
			}(),
			def: movieDef, fields: movieFields,
			patch: Patch{Description: Optional[string]{Set: true}},
			want:  updatePlan{Write: true, Name: "Films", DisplayMode: "thumbnail", SortMode: "release_year"},
		},
		{
			name: "display mode change", cur: movieList(), def: movieDef, fields: movieFields,
			patch: Patch{DisplayMode: strp("grid")},
			want:  updatePlan{Write: true, Name: "Films", DisplayMode: "grid", SortMode: "release_year"},
		},
		{
			name: "explicit built-in sort", cur: movieList(), def: movieDef, fields: movieFields,
			patch: Patch{SortMode: strp("title")},
			want:  updatePlan{Write: true, Name: "Films", DisplayMode: "thumbnail", SortMode: "title"},
		},
		{
			name: "explicit sort on a field of the type", cur: movieList(), def: movieDef, fields: movieFields,
			patch: Patch{SortMode: strp("streaming")},
			want:  updatePlan{Write: true, Name: "Films", DisplayMode: "thumbnail", SortMode: "streaming"},
		},
		{name: "sort on a field of another type is 422", cur: movieList(), def: movieDef, fields: movieFields, patch: Patch{SortMode: strp("displacement_cc")}, wantErr: "sort_mode"},
		{name: "sort on a stores field is 422", cur: vehicleList(), def: vehicleDef, fields: vehicleFields, patch: Patch{SortMode: strp("stores")}, wantErr: "sort_mode"},
		{
			// The stored sort is checked against the type on every PATCH, even when the patch does not send it.
			name: "a stored sort that the type no longer has blocks an unrelated edit", cur: movieList(), def: movieDef, fields: nil,
			patch: Patch{Name: strp("Renamed")}, wantErr: "sort_mode",
		},

		// Type changes.
		{
			name: "type sent as the stored type is not a change", cur: movieList(), def: movieDef, fields: movieFields,
			patch: Patch{Type: Optional[string]{Set: true, Value: strp("movie")}, DisplayMode: strp("grid")},
			want:  updatePlan{Write: true, Name: "Films", DisplayMode: "grid", SortMode: "release_year"},
		},
		{
			name: "type change from no type to a type with a default sort", cur: List{Name: "Bikes", DisplayMode: "thumbnail", SortMode: "manual"}, def: movieDef, fields: movieFields,
			patch: Patch{Type: Optional[string]{Set: true, Value: strp("movie")}},
			want: updatePlan{
				Write: true, TypeChanged: true, NewType: strp("movie"), ClearMetadata: true, ClearPrice: true,
				Name: "Bikes", DisplayMode: "thumbnail", SortMode: "release_year",
			},
		},
		{
			// The stored sort is valid for the new type, so it stays: the type write is the
			// only write.
			name: "type change keeps a valid sort without a row write", cur: func() List {
				l := movieList()
				l.SortMode = "title"
				return l
			}(),
			def: vehicleDef, fields: vehicleFields,
			patch: Patch{Type: Optional[string]{Set: true, Value: strp("vehicle")}},
			want: updatePlan{
				TypeChanged: true, NewType: strp("vehicle"), ClearMetadata: true, ClearPrice: true,
				Name: "Films", DisplayMode: "thumbnail", SortMode: "title",
			},
		},
		{
			// A sort of the old type that means nothing for the new one falls back to manual, and that is a row write.
			name: "type change to a type with no default sort falls back to manual", cur: movieList(), def: wishlistDef,
			patch: Patch{Type: Optional[string]{Set: true, Value: strp("wishlist")}},
			want: updatePlan{
				Write: true, TypeChanged: true, NewType: strp("wishlist"), ClearMetadata: true,
				Name: "Films", DisplayMode: "thumbnail", SortMode: "manual",
			},
		},
		{
			name: "type change to no type falls back to manual", cur: func() List {
				l := vehicleList()
				l.SortMode = "displacement_cc"
				return l
			}(),
			def:   TypeInfo{},
			patch: Patch{Type: Optional[string]{Set: true}},
			want: updatePlan{
				Write: true, TypeChanged: true, ClearMetadata: true, ClearPrice: true,
				Name: "Bikes", DisplayMode: "thumbnail", SortMode: "manual",
			},
		},
		{
			name: "type change to no type with an explicit sort keeps the explicit sort", cur: func() List {
				l := vehicleList()
				l.SortMode = "displacement_cc"
				return l
			}(),
			def:   TypeInfo{},
			patch: Patch{Type: Optional[string]{Set: true}, SortMode: strp("manual")},
			want: updatePlan{
				Write: true, TypeChanged: true, ClearMetadata: true, ClearPrice: true,
				Name: "Bikes", DisplayMode: "thumbnail", SortMode: "manual",
			},
		},
		{
			name: "type change with an explicit sort the new type rejects is 422", cur: movieList(), def: vehicleDef, fields: vehicleFields,
			patch: Patch{Type: Optional[string]{Set: true, Value: strp("vehicle")}, SortMode: strp("release_year")}, wantErr: "sort_mode",
		},
		{
			name: "type change to a type that allows a price does not clear prices", cur: movieList(), def: wishlistDef,
			patch: Patch{Type: Optional[string]{Set: true, Value: strp("wishlist")}},
			want: updatePlan{
				Write: true, TypeChanged: true, NewType: strp("wishlist"), ClearMetadata: true,
				Name: "Films", DisplayMode: "thumbnail", SortMode: "manual",
			},
		},
		// Icon and colour (F012-B1).
		{
			name: "built-in icon with a colour", cur: movieList(), def: movieDef, fields: movieFields,
			patch: Patch{Icon: Optional[string]{Set: true, Value: strp("i:film")}, IconColor: Optional[string]{Set: true, Value: strp("indigo")}},
			want: updatePlan{
				Write: true, Name: "Films", DisplayMode: "thumbnail", SortMode: "release_year",
				Icon: strp("i:film"), IconColor: strp("indigo"),
			},
		},
		{
			name: "recolour alone", cur: func() List {
				l := movieList()
				l.Icon, l.IconColor = strp("i:film"), strp("teal")
				return l
			}(),
			def: movieDef, fields: movieFields,
			patch: Patch{IconColor: Optional[string]{Set: true, Value: strp("blue")}},
			want: updatePlan{
				Write: true, Name: "Films", DisplayMode: "thumbnail", SortMode: "release_year",
				Icon: strp("i:film"), IconColor: strp("blue"),
			},
		},
		{
			name: "colour cleared alone", cur: func() List {
				l := movieList()
				l.Icon, l.IconColor = strp("i:film"), strp("teal")
				return l
			}(),
			def: movieDef, fields: movieFields,
			patch: Patch{IconColor: Optional[string]{Set: true}},
			want: updatePlan{
				Write: true, Name: "Films", DisplayMode: "thumbnail", SortMode: "release_year",
				Icon: strp("i:film"),
			},
		},
		{
			name: "emoji clears the stored colour", cur: func() List {
				l := movieList()
				l.Icon, l.IconColor = strp("i:star"), strp("red")
				return l
			}(),
			def: movieDef, fields: movieFields,
			patch: Patch{Icon: Optional[string]{Set: true, Value: strp("⭐")}},
			want: updatePlan{
				Write: true, Name: "Films", DisplayMode: "thumbnail", SortMode: "release_year",
				Icon: strp("⭐"),
			},
		},
		{
			name: "null icon resets the colour too", cur: func() List {
				l := movieList()
				l.Icon, l.IconColor = strp("i:star"), strp("red")
				return l
			}(),
			def: movieDef, fields: movieFields,
			patch: Patch{Icon: Optional[string]{Set: true}},
			want:  updatePlan{Write: true, Name: "Films", DisplayMode: "thumbnail", SortMode: "release_year"},
		},
		{
			name: "colour with an emoji is 422", cur: movieList(), def: movieDef, fields: movieFields,
			patch: Patch{Icon: Optional[string]{Set: true, Value: strp("🎬")}, IconColor: Optional[string]{Set: true, Value: strp("red")}}, wantErr: "icon_color",
		},
		{
			name: "colour on a list with no icon is 422", cur: movieList(), def: movieDef, fields: movieFields,
			patch: Patch{IconColor: Optional[string]{Set: true, Value: strp("red")}}, wantErr: "icon_color",
		},
		{
			name: "unknown colour is 422", cur: movieList(), def: movieDef, fields: movieFields,
			patch: Patch{Icon: Optional[string]{Set: true, Value: strp("i:star")}, IconColor: Optional[string]{Set: true, Value: strp("magenta")}}, wantErr: "icon_color",
		},
		{
			name: "invalid icon is 422", cur: movieList(), def: movieDef, fields: movieFields,
			patch: Patch{Icon: Optional[string]{Set: true, Value: strp("i:Star")}}, wantErr: "icon",
		},

		// Order of the errors: sort, then icon and colour.
		{
			name: "sort error comes before icon error", cur: movieList(), def: movieDef, fields: movieFields,
			patch: Patch{SortMode: strp("nope"), Icon: Optional[string]{Set: true, Value: strp("i:Star")}}, wantErr: "sort_mode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := planUpdate(tt.cur, tt.def, tt.fields, tt.patch)
			if tt.wantErr != "" {
				var ve *apperr.ValidationError
				if !errors.As(err, &ve) || len(ve.Errors) == 0 || ve.Errors[0].Field != tt.wantErr {
					t.Fatalf("err = %v, want a validation error on %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("plan = %+v\nwant   %+v", got, tt.want)
			}
		})
	}
}
