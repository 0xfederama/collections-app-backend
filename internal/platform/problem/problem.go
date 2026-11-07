// Package problem maps errors to RFC 9457 problem+json responses (API.md §4).
// It is the only place that knows both apperr and the wire format, for the
// domain errors a service returns and for the errors Huma builds itself
// (body, parameter and content-type rejections).
package problem

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/validation"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

const typeBase = "https://collection-app.invalid/problems/"

// Problem is the body of every error response. It implements huma.StatusError,
// so Huma writes it as application/problem+json.
type Problem struct {
	Type           string          `json:"type" doc:"Problem type URI"`
	Title          string          `json:"title"`
	Status         int             `json:"status"`
	Detail         string          `json:"detail,omitempty"`
	Code           string          `json:"code" doc:"Stable machine-readable code"`
	RequestID      string          `json:"request_id,omitempty"`
	CurrentVersion *int            `json:"current_version,omitempty"`
	Errors         []FieldError    `json:"errors,omitempty"`
	BlockingParent *BlockingParent `json:"blocking_parent,omitempty"`
}

// FieldError is one failed field rule of a 422.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// BlockingParent names the trashed ancestor that blocks a restore (409 parent_in_trash).
type BlockingParent struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (p *Problem) Error() string  { return p.Code + ": " + p.Detail }
func (p *Problem) GetStatus() int { return p.Status }

// ContentType makes Huma answer with application/problem+json.
func (p *Problem) ContentType(string) string { return "application/problem+json" }

// New builds a problem with the type URI derived from code.
func New(status int, code, title, detail string) *Problem {
	return &Problem{
		Type: typeBase + strings.ReplaceAll(code, "_", "-"), Title: title, Status: status, Detail: detail, Code: code,
	}
}

// Malformed is the 400 for a request that cannot be read (body, content type).
func Malformed(detail string) *Problem {
	return New(http.StatusBadRequest, "malformed_request", "Malformed request", detail)
}

// InvalidParameter is the 400 for a path, query or header value that does not parse.
func InvalidParameter(name string) *Problem {
	return New(http.StatusBadRequest, "invalid_parameter", "Invalid parameter", `"`+name+`" is invalid.`)
}

// UnknownField is the 400 for a body key the schema does not declare.
func UnknownField(path string) *Problem {
	return New(http.StatusBadRequest, "unknown_field", "Unknown field", `Unknown field "`+path+`".`)
}

// PreconditionRequired is the 428 for a missing or malformed If-Match.
func PreconditionRequired() *Problem {
	return New(http.StatusPreconditionRequired, "precondition_required", "Precondition required",
		"Send the resource ETag in the If-Match header.")
}

// PayloadTooLarge is the 413 for a body over the operation's limit.
func PayloadTooLarge() *Problem {
	return New(http.StatusRequestEntityTooLarge, "payload_too_large", "Payload too large",
		"The request body exceeds the limit for this route.")
}

// UnsupportedContentType is the 400 for a body whose Content-Type is missing or
// not JSON. Huma's 415 would echo the body, so it is never sent.
func UnsupportedContentType() *Problem {
	return Malformed(detailUnsupportedContentType)
}

// Fixed details for the request errors Huma reports. Huma's own messages and
// the offending body never reach the client.
const (
	detailUnsupportedContentType = "Unsupported Content-Type."
	detailNotJSON                = "The request body is not valid JSON."
	msgUnexpectedProperty        = "unexpected property" // huma/validation.MsgUnexpectedProperty
)

// From maps a service error to a problem. It unwraps with errors.As, so wrapped
// and joined domain errors map too. Unknown errors become a generic 500 that
// carries only requestID; unexpected reports that the caller should log err.
func From(err error, requestID string) (p *Problem, unexpected bool) {
	var (
		notFound   *apperr.NotFoundError
		stale      *apperr.VersionConflictError
		invalid    *apperr.ValidationError
		unknown    *apperr.UnknownFieldError
		conflict   *apperr.ConflictError
		tooLarge   *apperr.PayloadTooLargeError
		badMedia   *apperr.UnsupportedMediaTypeError
		lookupOff  *apperr.LookupDisabledError
		upstream   *apperr.UpstreamUnavailableError
		noFavicon  *apperr.FaviconNotFoundError
		bodyTooBig *http.MaxBytesError
	)
	switch {
	case errors.As(err, &notFound):
		return New(http.StatusNotFound, "not_found", "Not found", capitalize(notFound.Resource)+" not found."), false
	case errors.As(err, &noFavicon):
		return New(http.StatusNotFound, "favicon_not_found", "Favicon not found", "The site has no favicon."), false
	case errors.As(err, &stale):
		p = New(http.StatusPreconditionFailed, "version_conflict", "Resource was modified",
			"The resource was changed by someone else. Reload and try again.")
		p.CurrentVersion = &stale.Current
		return p, false
	case errors.As(err, &invalid):
		fields := make([]FieldError, 0, len(invalid.Errors))
		for _, fe := range invalid.Errors {
			fields = append(fields, FieldError{Field: fe.Field, Message: fe.Message})
		}
		return validationProblem(fields), false
	case errors.As(err, &unknown):
		return UnknownField(unknown.Path), false
	case errors.As(err, &conflict):
		return conflictProblem(conflict), false
	case errors.As(err, &tooLarge), errors.As(err, &bodyTooBig):
		return PayloadTooLarge(), false
	case errors.As(err, &badMedia):
		return New(http.StatusUnsupportedMediaType, "unsupported_media_type", "Unsupported media type",
			"Only JPEG, PNG and WebP images are accepted."), false
	case errors.As(err, &lookupOff):
		return New(http.StatusServiceUnavailable, "lookup_disabled", "Lookup disabled",
			"Lookup is not configured on this server."), false
	case errors.As(err, &upstream):
		return New(http.StatusServiceUnavailable, "upstream_unavailable", "Lookup unavailable",
			"The lookup service is temporarily unavailable. Try again later."), false
	default:
		return internal(requestID), true
	}
}

func internal(requestID string) *Problem {
	p := New(http.StatusInternalServerError, "internal", "Internal error", "Something went wrong.")
	p.RequestID = requestID
	return p
}

func conflictProblem(c *apperr.ConflictError) *Problem {
	switch c.Code {
	case apperr.CodeCycleDetected:
		return New(http.StatusConflict, string(c.Code), "Cycle detected",
			"A folder cannot be moved into itself or one of its descendants.")
	case apperr.CodeParentInTrash:
		p := New(http.StatusConflict, string(c.Code), "Parent is in the trash",
			"Restore the parent first, or choose a destination.")
		if c.Blocking != nil {
			p.BlockingParent = &BlockingParent{Type: c.Blocking.Type, ID: c.Blocking.ID, Name: c.Blocking.Name}
		}
		return p
	case apperr.CodeDestinationUnavailable:
		return New(http.StatusConflict, string(c.Code), "Destination unavailable",
			"The destination does not exist or is in the trash.")
	case apperr.CodeTypeMismatch:
		return New(http.StatusConflict, string(c.Code), "Type mismatch",
			"The destination list has a different type than the item.")
	case apperr.CodeDuplicateTitle:
		return New(http.StatusConflict, string(c.Code), "Duplicate title",
			"An item with this title already exists in the list.")
	case apperr.CodeOrderStale:
		return New(http.StatusConflict, string(c.Code), "Order is stale",
			"The items changed. Reload and try again.")
	default:
		return New(http.StatusConflict, string(c.Code), "Conflict", "The request conflicts with the current state.")
	}
}

func capitalize(s string) string {
	if s == "" {
		return "Resource"
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

type requestIDKey struct{}

// WithRequestID returns ctx carrying the request id that internal problems echo.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFrom returns the request id of ctx, or "" when there is none.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

var installOnce sync.Once

// Install replaces Huma's error constructors so every error Huma writes (its
// own validation and body errors, and errors returned by handlers) is a
// Problem. Huma keeps these as package variables, which is the one global this
// approach costs; the replacement runs once per process. Handlers should be
// registered through httpserver.Op, which also logs unexpected errors and
// answers a client that went away with 499.
func Install() {
	installOnce.Do(func() {
		huma.NewError = func(status int, _ string, errs ...error) huma.StatusError {
			return fromHuma(context.Background(), status, errs)
		}
		huma.NewErrorWithContext = func(ctx huma.Context, status int, _ string, errs ...error) huma.StatusError {
			return fromHuma(ctx.Context(), status, errs)
		}
	})
}

// fromHuma turns the error Huma builds for status into a Problem. Huma's
// messages are not used: the details are fixed texts, and nothing from an
// unexpected error reaches the client.
func fromHuma(ctx context.Context, status int, errs []error) *Problem {
	for _, err := range errs {
		var p *Problem
		if errors.As(err, &p) {
			return p // already a problem (a middleware or a handler built it)
		}
	}
	switch status {
	case http.StatusUnsupportedMediaType:
		return UnsupportedContentType()
	case http.StatusRequestEntityTooLarge:
		return PayloadTooLarge()
	case http.StatusInternalServerError:
		// A handler returned an error Huma does not know: map it as a service error.
		p, _ := From(errors.Join(errs...), RequestIDFrom(ctx))
		return p
	}

	var details []*huma.ErrorDetail
	for _, err := range errs {
		var d *huma.ErrorDetail
		if errors.As(err, &d) {
			details = append(details, d)
		}
	}
	if len(details) == 0 {
		return generic(status)
	}
	for _, d := range details {
		if isBodyTypeError(d) {
			// A value of the wrong JSON type is the decoder's answer (400), not a field rule.
			return Malformed(detailNotJSON)
		}
	}
	var fields []FieldError
	for _, d := range details {
		switch {
		case d.Message == msgUnexpectedProperty:
			return UnknownField(strings.TrimPrefix(d.Location, "body."))
		case strings.HasPrefix(d.Location, "path."), strings.HasPrefix(d.Location, "query."), strings.HasPrefix(d.Location, "header."):
			return InvalidParameter(d.Location[strings.Index(d.Location, ".")+1:])
		case d.Location == "body" && status == http.StatusBadRequest:
			return Malformed(detailNotJSON)
		}
		fields = append(fields, fieldError(d))
	}
	return validationProblem(fields)
}

// maxDetailFields bounds how many fields the validation detail names.
const maxDetailFields = 3

// validationProblem is the 422 validation_failed. Its detail names the failed
// fields ("Invalid field: title (must be 1 to 300 characters)."); the full list
// stays in Errors.
func validationProblem(fields []FieldError) *Problem {
	p := New(http.StatusUnprocessableEntity, "validation_failed", "Validation failed", validationDetail(fields))
	p.Errors = fields
	return p
}

func validationDetail(fields []FieldError) string {
	if len(fields) == 0 {
		return "One or more fields are invalid."
	}
	parts := make([]string, 0, maxDetailFields)
	for i, fe := range fields {
		if i == maxDetailFields {
			break
		}
		part := fe.Field
		if part == "" {
			part = "body"
		}
		if fe.Message != "" {
			part += " (" + strings.TrimSuffix(fe.Message, ".") + ")"
		}
		parts = append(parts, part)
	}
	label := "Invalid field: "
	if len(fields) > 1 {
		label = "Invalid fields: "
	}
	detail := label + strings.Join(parts, "; ")
	if more := len(fields) - len(parts); more > 0 {
		detail += "; and " + strconv.Itoa(more) + " more"
	}
	return detail + "."
}

// bodyTypeMessages are Huma's messages for a value of the wrong JSON type.
// Constraint messages that start with the same words (`expected number >= 0`)
// are not in the set: they stay field errors.
var bodyTypeMessages = map[string]bool{
	validation.MsgExpectedBoolean: true,
	validation.MsgExpectedNumber:  true,
	validation.MsgExpectedInteger: true,
	validation.MsgExpectedString:  true,
	validation.MsgExpectedArray:   true,
	validation.MsgExpectedObject:  true,
}

// isBodyTypeError reports whether d is a wrong JSON type on the body or a body field.
func isBodyTypeError(d *huma.ErrorDetail) bool {
	return (d.Location == "body" || strings.HasPrefix(d.Location, "body.")) && bodyTypeMessages[d.Message]
}

// fieldError names the body field a 422 rule is about.
func fieldError(d *huma.ErrorDetail) FieldError {
	if name, ok := requiredProperty(d); ok {
		return FieldError{Field: name, Message: d.Message}
	}
	return FieldError{Field: strings.TrimPrefix(d.Location, "body."), Message: d.Message}
}

// Huma's message for a missing property (validation.MsgExpectedRequiredProperty).
const (
	msgRequiredPrefix = "expected required property "
	msgRequiredSuffix = " to be present"
)

// requiredProperty returns the property named by a missing-property error of
// the body root. Huma reports those at "body", not at the property.
func requiredProperty(d *huma.ErrorDetail) (string, bool) {
	if d.Location != "body" {
		return "", false
	}
	rest, ok := strings.CutPrefix(d.Message, msgRequiredPrefix)
	if !ok {
		return "", false
	}
	return strings.CutSuffix(rest, msgRequiredSuffix)
}

// generic answers a Huma status that carries no detail the mapping knows, with
// its standard text only.
func generic(status int) *Problem {
	if status == http.StatusBadRequest {
		return New(status, "malformed_request", "Malformed request", "")
	}
	code := strings.ReplaceAll(strings.ToLower(http.StatusText(status)), " ", "_")
	return New(status, code, http.StatusText(status), "")
}
