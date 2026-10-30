package httpapi

import (
	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
)

func nullableString(s *string) nullable.Nullable[string] {
	if s == nil {
		return nullable.NewNullNullable[string]()
	}
	return nullable.NewNullableWithValue(*s)
}

// nullableIconColor always sets the field (F012-B1), like nullableString.
func nullableIconColor(s *string) nullable.Nullable[oapi.ListIconColor] {
	if s == nil {
		return nullable.NewNullNullable[oapi.ListIconColor]()
	}
	return nullable.NewNullableWithValue(oapi.ListIconColor(*s))
}

// optionalUUID reads an optional nullable UUID field: absent and null both mean "none" (the root).
func optionalUUID(n nullable.Nullable[uuid.UUID]) *uuid.UUID {
	if v, err := n.Get(); err == nil {
		return &v
	}
	return nil
}
