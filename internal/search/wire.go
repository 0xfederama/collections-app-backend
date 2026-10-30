package search

// The wire types below are declared outside handler.go, as in the other features,
// so that the layer rules keep a feature's handler identifiers to itself. Their
// JSON keys are in alphabetical order, as the generated contract wrote them.

import (
	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
)

// SearchPathSegmentJSON is one ancestor of a search result (published as
// "SearchPathSegment"). Icon and icon_color are the list's own: both are null on folder segments.
type SearchPathSegmentJSON struct {
	Icon      *string   `json:"icon" nullable:"true"`
	IconColor *string   `json:"icon_color" nullable:"true" enum:"red,orange,amber,green,teal,blue,indigo,purple,pink,gray"`
	ID        uuid.UUID `json:"id" format:"uuid"`
	Name      string    `json:"name"`
	Type      string    `json:"type" enum:"folder,list"`
}

// SearchResultJSON is one match with its path (published as "SearchResult").
type SearchResultJSON struct {
	Item     items.ItemSummaryJSON   `json:"item"`
	Path     []SearchPathSegmentJSON `json:"path" nullable:"false"`
	TypeName *string                 `json:"type_name" nullable:"true" doc:"The display name from this workspace's type definition, null when the list has no type."`
}

// SearchPageJSON is the searchItems answer (published as "SearchPage").
type SearchPageJSON struct {
	Data []SearchResultJSON `json:"data" nullable:"false"`
	Page httpserver.Page    `json:"page"`
}

// RecentItemsJSON is the listRecentItems answer (published as "RecentItems"). It is not paginated.
type RecentItemsJSON struct {
	Data []SearchResultJSON `json:"data" nullable:"false"`
}

// resultJSON is the wire shape of one match.
func resultJSON(r Result) SearchResultJSON {
	path := make([]SearchPathSegmentJSON, 0, len(r.Path))
	for _, seg := range r.Path {
		path = append(path, SearchPathSegmentJSON{
			Icon: seg.Icon, IconColor: seg.IconColor, ID: seg.ID, Name: seg.Name, Type: seg.Type,
		})
	}
	return SearchResultJSON{Item: items.ToItemSummaryJSON(r.Item), Path: path, TypeName: r.TypeName}
}
