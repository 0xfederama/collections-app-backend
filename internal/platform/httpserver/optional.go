package httpserver

import (
	"encoding/json"
	"reflect"

	"github.com/danielgtaylor/huma/v2"
)

// Optional is a body field of a PATCH that tells three cases apart: absent
// (Set false), null (Set true, Value nil) and a value (Set true, Value non-nil).
// It is nullable in the schema. Absent fields need `json:",omitempty"` so
// Huma does not mark them required.
type Optional[T any] struct {
	Set   bool
	Value *T
}

// UnmarshalJSON runs only for a key that is present in the body.
func (o *Optional[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

// Schema is T's schema, nullable. T must be a primitive or enum type: a struct
// would be a $ref, which OpenAPI 3.0 cannot mark nullable.
func (Optional[T]) Schema(r huma.Registry) *huma.Schema {
	s := huma.SchemaFromType(r, reflect.TypeFor[T]())
	s.Nullable = true
	return s
}
