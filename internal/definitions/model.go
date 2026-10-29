package definitions

import "github.com/google/uuid"

// Field is a field of a type. Key is stable; Position orders the fields (locked
// fields keep their seed positions). ID addresses the row in the write queries.
type Field struct {
	ID                uuid.UUID
	Key, Label, Type  string
	Position, Version int
	Locked            bool
}

// Type is a type definition. ID addresses the row in the write queries. Fields
// is filled by the service, not by the repository.
type Type struct {
	ID                  uuid.UUID
	Key, Name           string
	AllowsPrice, Locked bool
	Version             int
	DefaultIcon         *string
	DefaultSortField    *string
	DefaultPinnedFields *[]string
	Fields              []Field
}

type CreateType struct {
	Name                string
	DefaultIcon         *string
	AllowsPrice         bool
	DefaultSortField    *string
	DefaultPinnedFields *[]string
}

// NullableString distinguishes an omitted property from a supplied null.
type NullableString struct {
	Set   bool
	Value *string
}

// NullableStrings distinguishes an omitted property from a supplied null.
type NullableStrings struct {
	Set   bool
	Value *[]string
}

type UpdateType struct {
	Name                *string
	DefaultIcon         NullableString
	AllowsPrice         *bool
	DefaultSortField    NullableString
	DefaultPinnedFields NullableStrings
}

type CreateField struct{ Label, Type string }

// ListRef names a list that uses a type (the delete refusal names the first one).
type ListRef struct {
	ID   uuid.UUID
	Name string
}
