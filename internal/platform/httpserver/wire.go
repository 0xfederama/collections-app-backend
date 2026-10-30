package httpserver

import "github.com/google/uuid"

// The wire types below are shared by the features. The contract publishes each
// one under a single name, so a feature that needs the same shape uses these
// instead of declaring its own (the registry panics on two types with one name).

// Page is the paging of a list answer (published as "Page").
type Page struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	Total  int `json:"total" doc:"Total rows matching, ignoring limit and offset."`
}

// PathSegment is one ancestor of a list, an item or a trashed root (published as
// "PathSegment"). Its type is a folder or a list.
type PathSegment struct {
	ID   uuid.UUID `json:"id" format:"uuid"`
	Name string    `json:"name"`
	Type string    `json:"type" enum:"folder,list"`
}
