package httpapi

import (
	"github.com/danielgtaylor/huma/v2"
)

// Spec returns the OpenAPI 3.1 document of the API as YAML: the operations of
// API(Deps{}) plus the document metadata (info, tags). It needs no dependency,
// so `make spec` runs without a database. The output is deterministic.
func Spec() ([]byte, error) {
	api := API(Deps{})
	doc := api.OpenAPI()
	doc.Info.Description = specDescription
	doc.Servers = nil
	doc.Tags = specTags()
	return doc.YAML()
}

// specDescription is the info.description of the contract.
const specDescription = `Private REST API of the Collection App. There is no auth in the MVP, so it must stay on a private network.

**Conventions**
- Bodies are JSON (` + "`application/json`" + `) unless noted. Field names are ` + "`snake_case`" + `, IDs are UUIDs, timestamps are RFC 3339 UTC.
- Money is a decimal string with 2 decimals (` + "`\"1299.00\"`" + `).
- Unknown request fields return ` + "`400 unknown_field`" + `. ` + "`PATCH`" + ` is a partial update: send ` + "`null`" + ` explicitly to clear a nullable field. ` + "`PUT`" + ` replaces a sub-resource.
- Create returns ` + "`201`" + ` with the resource and a ` + "`Location`" + ` header. Delete and purge return ` + "`204`" + `.
- Every response to a ` + "`GET`" + ` of a single resource, and to a mutation that returns one, carries an ` + "`ETag`" + ` header.
- **Every response carries an ` + "`X-Request-Id`" + ` header** (set by middleware, so it is not repeated on each operation).

**Optimistic concurrency.** ETags are typed and derived from ` + "`version`" + `: folder ` + "`\"f-N\"`" + `, list ` + "`\"l-N\"`" + `, item ` + "`\"i-N\"`" + `,
content ` + "`\"c-N\"`" + `, settings ` + "`\"s-N\"`" + `. Operations marked *requires ` + "`If-Match`" + `* answer ` + "`428 precondition_required`" + ` when the header is
missing (or is ` + "`*`" + `) and ` + "`412 version_conflict`" + ` (with ` + "`current_version`" + `) when it is stale. Creates, the three ` + "`…/order`" + ` endpoints and
trash restore/purge do not take ` + "`If-Match`" + `.

**Errors** are RFC 9457 ` + "`application/problem+json`" + ` with a stable ` + "`code`" + ` (see the ` + "`Problem`" + ` schema). A missing **or trashed** resource is
` + "`404 not_found`" + ` on every non-trash endpoint.

**Limits.** JSON bodies: 64 KiB (2 MiB on the content ` + "`PUT`" + ` and on list create and update, whose ` + "`description`" + ` can be 64 KiB). Multipart image upload: 10 MiB plus 64 KiB overhead. Markdown: 1 MiB UTF-8.
Item pages: ` + "`limit`" + ` 1–200 (default 50), ` + "`offset`" + ` ≥ 0.
`

// specTags are the tags of the contract, in display order.
func specTags() []*huma.Tag {
	return []*huma.Tag{
		{Name: "health", Description: "Liveness and readiness probes (outside `/api/v1`)."},
		{Name: "settings", Description: "Workspace name and settings (one resource, one ETag)."},
		{Name: "types", Description: "Workspace type and field definitions."},
		{Name: "tree", Description: "The sidebar tree of active folders and lists."},
		{Name: "folders", Description: "Folders, their moves and ordering."},
		{Name: "lists", Description: "Lists, their moves and ordering."},
		{Name: "items", Description: "Items and their metadata."},
		{Name: "tmdb", Description: "Optional movie and TV metadata lookup through the server-side TMDB integration, and the item lookup operation shared with books."},
		{Name: "books", Description: "Book metadata lookup through Open Library (no key needed)."},
		{Name: "image", Description: "Item images (upload or URL) and attachment files."},
		{Name: "content", Description: "Markdown content of an item and its version history."},
		{Name: "search", Description: "Title search across all active items."},
		{Name: "trash", Description: "Deleted folders, lists and items, addressed by trash batch."},
	}
}
