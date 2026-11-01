package definitions

import "github.com/0xfederama/collections-app-backend/internal/items/types"

// TypeFieldJSON is the wire shape of a field (published as the schema "TypeField").
// It lives outside handler.go so that the layer rules keep a feature's handler
// identifiers to itself, as the other features' wire.go files do.
type TypeFieldJSON struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type" enum:"text,number,date,boolean,link,streaming,stores"`
	Position int    `json:"position" minimum:"0"`
}

// TypeDefinitionJSON is the wire shape of a built-in type with its fields
// (published as the schema "TypeDefinition"). The nullable defaults are always present.
type TypeDefinitionJSON struct {
	Key              string          `json:"key"`
	Name             string          `json:"name"`
	DefaultIcon      *string         `json:"default_icon" nullable:"true"`
	AllowsPrice      bool            `json:"allows_price"`
	DefaultSortField *string         `json:"default_sort_field" nullable:"true" maxLength:"32"`
	Fields           []TypeFieldJSON `json:"fields" nullable:"false"`
}

// typeJSON is the wire shape of d. Fields is never null: a type without fields is `[]`.
func typeJSON(d types.Def) TypeDefinitionJSON {
	fields := make([]TypeFieldJSON, 0, len(d.Fields))
	for i, f := range d.Fields {
		fields = append(fields, TypeFieldJSON{Key: f.Key, Label: f.Label, Type: f.Type, Position: i})
	}
	return TypeDefinitionJSON{
		Key: d.Key, Name: d.Name, DefaultIcon: d.DefaultIcon, AllowsPrice: d.AllowsPrice,
		DefaultSortField: d.DefaultSortField, Fields: fields,
	}
}
