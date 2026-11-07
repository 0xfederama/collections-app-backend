package problem

import (
	"fmt"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// Code is one machine-readable code a problem can carry, with its meaning.
type Code struct {
	Code    string
	Meaning string
}

// Codes is the single list of the codes the API returns. The Problem schema
// description is generated from it, and TestCodesCoverEveryEmittedCode fails when
// From or one of the helpers emits a code that is not listed here.
var Codes = []Code{
	{"malformed_request", "The request cannot be read: a body that is not JSON, a missing or unsupported Content-Type, or a value of the wrong JSON type (400)."},
	{"invalid_parameter", "A path, query or header value does not parse or is out of range (400). `detail` names the parameter."},
	{"unknown_field", "A body key the schema does not declare (400). `detail` gives its path."},
	{"precondition_required", "A versioned resource is changed without a usable `If-Match` (428)."},
	{"payload_too_large", "The body is over the limit of the operation (413)."},
	{"unsupported_media_type", "An upload is not JPEG, PNG or WebP (415)."},
	{"not_found", "The resource is missing or trashed, or the endpoint does not exist (404)."},
	{"favicon_not_found", "The site of a favicon request has no usable icon (404). The web shows its fallback."},
	{"method_not_allowed", "The path exists but does not accept this method (405)."},
	{"version_conflict", "The `If-Match` version is stale (412). `current_version` is the stored one."},
	{"validation_failed", "One or more fields break a rule (422). `errors` lists them."},
	{"cycle_detected", "A folder would be moved into itself or one of its descendants (409)."},
	{"parent_in_trash", "A restore without a destination whose parent is trashed (409). `blocking_parent` names it."},
	{"destination_unavailable", "A move, restore or create targets a folder or list that is missing or trashed (409)."},
	{"type_mismatch", "An item would move to a list of another type (409)."},
	{"duplicate_title", "An item create, rename or move would repeat a title of the target list, ignoring case, accents and outer spaces (409)."},
	{"order_stale", "A reorder names an id set that no longer matches the stored one (409)."},
	{"lookup_disabled", "A lookup was requested but its integration is not configured on this server (503)."},
	{"upstream_unavailable", "A lookup provider could not be reached or answered in time (503)."},
	{"client_closed_request", "The client closed the request before the answer was ready (499). Logged at DEBUG only."},
	{"internal", "An unexpected error (500). `request_id` is the only detail; the cause is in the server log."},
	{"conflict", "Generic 409 for a status Huma reports without detail."},
	{"precondition_failed", "Generic 412 for a status Huma reports without detail."},
	{"unprocessable_entity", "Generic 422 for a status Huma reports without detail."},
	{"service_unavailable", "Generic 503 for a status Huma reports without detail."},
}

// problemDescription is the Problem schema description: the list of codes.
func problemDescription() string {
	var b strings.Builder
	b.WriteString("An RFC 9457 problem, the body of every error response. `code` is stable and is one of:\n\n")
	for _, c := range Codes {
		fmt.Fprintf(&b, "- `%s`: %s\n", c.Code, c.Meaning)
	}
	return b.String()
}

// TransformSchema puts the code list in the Problem schema description.
func (*Problem) TransformSchema(_ huma.Registry, s *huma.Schema) *huma.Schema {
	s.Description = problemDescription()
	return s
}
