# API notes: what the generated contract does not carry

`api/openapi.yaml` is generated from the Huma operations (`make spec`, `cmd/specgen`) and
checked by `make check-spec`. This file lists what that document leaves out on purpose, and why.

## Request constraints kept in the services, not in the spec

The old hand-written contract declared these with `minLength`, `maxLength`, `minimum`,
`maximum`, `pattern`, `enum`, `maxItems` or `uniqueItems`. The services still enforce every
one of them; the generated spec does not tag them, because a Huma tag is checked before the
handler runs. That check runs before the `If-Match` comparison and before the lookup of the
target resource, so a tag would change which answer a request gets (a 422 where the server
answers 404 or 412 today) or the message of a 422. Keep the rule in the service and this list
in step with it.

| Operation | Field | Constraint (old contract) |
|---|---|---|
| `updateSettings` | `workspace_name` | 1 to 200 characters |
| `updateSettings` | `streaming_country_code` | `^[A-Z]{2}$` |
| `updateSettings` | `markdown_history_limit` | 0 to 200 |
| `updateSettings` | `trash_retention_days` | 1 to 365, or `null` |
| `createType`, `updateType` | `name` | 1 to 100 characters |
| `createType`, `updateType` | `default_sort_field` | at most 32 characters |
| `createType`, `updateType` | `default_pinned_fields` | at most 3 entries, each at most 32 characters |
| `createTypeField`, `updateTypeField` | `label` | 1 to 100 characters |
| `createTypeField` | `type` | one of `text`, `number`, `date`, `boolean`, `link` |
| `reorderTypeFields` | `keys` | unique strings |
| `reorderFolders`, `reorderLists`, `reorderListItems` | `ids` | at most 10000 UUIDs |
| `createList`, `updateList` | `name` | 1 to 200 characters |
| `createList`, `updateList` | `description` | Markdown, at most 65536 UTF-8 bytes |
| `createItem`, `updateItem` | `title` | 1 to 300 characters |
| `createItem`, `updateItem` | `website` | at most 2048 characters |
| `createItem` | `tmdb_id` | at least 1 |
| `createItem`, `lookupItemMetadata` | `source_id` | `^OL[0-9]+W$`, at most 64 characters |
| `lookupItemMetadata` | `mode` | one of `fill`, `rematch`, `refresh` (default `fill`) |
| `lookupItemMetadata` | `tmdb_id` | at least 1 |
| `createItem`, `updateItem` | `price` | `amount`, `min`, `max`: `^\d{1,10}\.\d{2}$`; `currency`: `^[A-Z]{3}$` |
| `updateItemContent` | `body` | at most 1048576 UTF-8 bytes (the service counts bytes; the guard limit is 2 MiB) |
| `restoreContentVersion` | `version` | at least 1 |
| `setItemImage` (JSON body) | `url` | an absolute http or https URL of at most 2048 characters |

## Path and header constraints kept in the services

- Every `{key}` of a type or field route: `^[a-z][a-z0-9_]{0,31}$`. An unknown key answers 404.
- `getContentVersion`, `{version}`: at least 1. Version 0 never exists, so it answers 404 today.
- `If-Match` on every operation that takes it: at most 64 characters.

## Response-side constraints not carried

The old contract also declared `format`, `maxLength` and enum values on response fields
(for example `Problem.code`, which is now listed in the `Problem` description, and
`UploadImage.original_filename`). The server does not validate responses; the generated
schema carries the type and the nullability of each field.

## Errors in the spec that the server does not return as such

- Every operation that takes a path, query or body value lists `422`, because Huma adds it.
  A path or query value that does not parse is answered `400 invalid_parameter`, never 422.
  A 422 that a service returns is possible on a body operation, and on `previewDeleteTypeField`
  for a locked field. Other GETs with path or query values never answer 422.
- `createType` lists no `409`. A duplicate type key reaches the store and is answered as a
  `500 internal` today, not as the `409` the old contract declared. That is an open defect.
- `deleteType` lists no `409`. Deleting a type that a list uses is a `422 validation_failed`
  on the field `type`.
- The health probes list `500`, the answer of the recovery middleware for a panic. The old
  contract did not list it.

## Examples

The generated document has no examples. The old hand-written contract had them, and the web
mocks (MSW) were built from them. Redocly keeps its example rules (`redocly.yaml`), which now
check nothing.
