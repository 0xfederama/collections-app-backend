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
	Fields              []Field
}

type CreateField struct{ Label, Type string }
