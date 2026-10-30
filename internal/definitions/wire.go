package definitions

// TypeFieldJSON is the wire shape of a field (published as the schema "TypeField").
// It lives outside handler.go so that the layer rules keep a feature's handler
// identifiers to itself, as the other features' wire.go files do.
type TypeFieldJSON struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type" enum:"text,number,date,boolean,link,streaming,stores"`
	Position int    `json:"position" minimum:"0"`
	IsLocked bool   `json:"is_locked"`
	Version  int    `json:"version"`
}

// TypeDefinitionJSON is the wire shape of a type with its fields (published as
// the schema "TypeDefinition"). The nullable defaults are always present.
type TypeDefinitionJSON struct {
	Key                 string          `json:"key"`
	Name                string          `json:"name"`
	DefaultIcon         *string         `json:"default_icon" nullable:"true"`
	AllowsPrice         bool            `json:"allows_price"`
	DefaultSortField    *string         `json:"default_sort_field" nullable:"true" maxLength:"32"`
	DefaultPinnedFields *[]string       `json:"default_pinned_fields" nullable:"true" maxItems:"3"`
	IsLocked            bool            `json:"is_locked"`
	Version             int             `json:"version"`
	Fields              []TypeFieldJSON `json:"fields" nullable:"false"`
}

// TypeFieldDeletePreviewJSON is the count of item values a field delete removes
// (published as "TypeFieldDeletePreview").
type TypeFieldDeletePreviewJSON struct {
	PopulatedItemCount int `json:"populated_item_count" minimum:"0" doc:"Number of items, including trashed items, with a value for this field."`
}

// fieldJSON is the wire shape of f.
func fieldJSON(f Field) TypeFieldJSON {
	return TypeFieldJSON{Key: f.Key, Label: f.Label, Type: f.Type, Position: f.Position, IsLocked: f.Locked, Version: f.Version}
}

// typeJSON is the wire shape of t. Fields is never null: a type without fields
// (a create or an update answer, which carries none) is `[]`.
func typeJSON(t Type) TypeDefinitionJSON {
	fields := make([]TypeFieldJSON, 0, len(t.Fields))
	for _, f := range t.Fields {
		fields = append(fields, fieldJSON(f))
	}
	return TypeDefinitionJSON{
		Key: t.Key, Name: t.Name, DefaultIcon: t.DefaultIcon, AllowsPrice: t.AllowsPrice,
		DefaultSortField: t.DefaultSortField, DefaultPinnedFields: t.DefaultPinnedFields,
		IsLocked: t.Locked, Version: t.Version, Fields: fields,
	}
}
