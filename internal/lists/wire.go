package lists

import (
	"time"

	"github.com/google/uuid"
)

// ListJSON is the wire shape of a list (published as the schema "List"). It lives
// outside handler.go so that the tree read can reuse it: the layer rules keep a
// feature's handler identifiers to itself.
type ListJSON struct {
	ID          uuid.UUID  `json:"id" format:"uuid"`
	FolderID    *uuid.UUID `json:"folder_id" format:"uuid" nullable:"true"`
	Name        string     `json:"name"`
	Type        *string    `json:"type" nullable:"true"`
	Description *string    `json:"description" nullable:"true"`
	DisplayMode string     `json:"display_mode" enum:"compact,thumbnail,grid"`
	SortMode    string     `json:"sort_mode"`
	Icon        *string    `json:"icon" nullable:"true"`
	IconColor   *string    `json:"icon_color" nullable:"true" enum:"red,orange,amber,green,teal,blue,indigo,purple,pink,gray"`
	SortOrder   int        `json:"sort_order"`
	ItemCount   int        `json:"item_count"`
	Version     int        `json:"version"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// ToJSON is the wire shape of l.
func ToJSON(l List) ListJSON {
	out := ListJSON{
		ID: l.ID, FolderID: l.FolderID, Name: l.Name, Type: l.Type, Description: l.Description,
		DisplayMode: l.DisplayMode, SortMode: l.SortMode, Icon: l.Icon, IconColor: l.IconColor,
		SortOrder: l.SortOrder, ItemCount: l.ItemCount, Version: l.Version, CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt,
	}
	return out
}
