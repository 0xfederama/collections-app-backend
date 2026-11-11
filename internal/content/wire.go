package content

import (
	"time"

	"github.com/google/uuid"
)

// The wire types below are declared outside handler.go so that the layer rules
// keep a feature's handler identifiers to itself. Their JSON keys are in
// alphabetical order, as the generated contract wrote them.

// ContentJSON is the wire shape of a content version (published as "Content").
// updated_at is null for version 0.
type ContentJSON struct {
	ItemID       uuid.UUID  `json:"item_id" format:"uuid"`
	Version      int        `json:"version" minimum:"0" doc:"0 = never saved."`
	Body         string     `json:"body" doc:"Markdown source, up to 1 MiB UTF-8."`
	UpdatedAt    *time.Time `json:"updated_at" nullable:"true" doc:"When this version was saved. null for version 0."`
	AuthorUserID *uuid.UUID `json:"author_user_id" format:"uuid" nullable:"true" doc:"The user who saved this version. null for version 0 and for versions saved before authorship was recorded."`
}

// UpdateContentRequest is the body of updateItemContent. The schema carries no
// length: the 1 MiB rule is the service's, counted in bytes (413).
type UpdateContentRequest struct {
	Body string `json:"body"`
}

// RestoreContentRequest is the body of restoreContentVersion.
type RestoreContentRequest struct {
	Version int `json:"version"`
}

// ContentVersionInfoJSON is one saved version without its body (published as
// "ContentVersionInfo").
type ContentVersionInfoJSON struct {
	CreatedAt    time.Time  `json:"created_at"`
	SizeBytes    int        `json:"size_bytes" minimum:"0"`
	Version      int        `json:"version"`
	AuthorUserID *uuid.UUID `json:"author_user_id" format:"uuid" nullable:"true" doc:"The user who saved this version; null when unknown."`
}

// ContentVersionListJSON is the listContentVersions answer (published as "ContentVersionList").
type ContentVersionListJSON struct {
	Data []ContentVersionInfoJSON `json:"data" nullable:"false"`
}

// contentJSON is the wire shape of c. The two structs have the same fields in the
// same order, so the conversion keeps them in step at compile time.
func contentJSON(c Content) ContentJSON {
	return ContentJSON(c)
}

// versionListJSON is the wire shape of a version list. Data is never null.
func versionListJSON(vs []VersionInfo) ContentVersionListJSON {
	data := make([]ContentVersionInfoJSON, 0, len(vs))
	for _, v := range vs {
		data = append(data, ContentVersionInfoJSON{CreatedAt: v.CreatedAt, SizeBytes: v.SizeBytes, Version: v.Version, AuthorUserID: v.AuthorUserID})
	}
	return ContentVersionListJSON{Data: data}
}
