package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

// errPanic is mapped like any unexpected error: a generic 500.
var errPanic = errors.New("panic")

const problemTypeBase = "https://collection-app.invalid/problems/"

// problem is the RFC 9457 body of API.md §4. Optional extensions are omitted when unset.
type problem struct {
	Type           string          `json:"type"`
	Title          string          `json:"title"`
	Status         int             `json:"status"`
	Detail         string          `json:"detail,omitempty"`
	Code           string          `json:"code"`
	RequestID      string          `json:"request_id,omitempty"`
	CurrentVersion *int            `json:"current_version,omitempty"`
	Errors         []fieldProblem  `json:"errors,omitempty"`
	BlockingParent *blockingParent `json:"blocking_parent,omitempty"`
}

type fieldProblem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type blockingParent struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

func newProblem(status int, code, title, detail string) problem {
	return problem{
		Type:   problemTypeBase + strings.ReplaceAll(code, "_", "-"),
		Title:  title,
		Status: status,
		Detail: detail,
		Code:   code,
	}
}

// writeProblem sends p as application/problem+json.
func writeProblem(w http.ResponseWriter, p problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// Problem constructors shared by the error handlers and the middleware.

func malformedRequest(detail string) problem {
	return newProblem(http.StatusBadRequest, "malformed_request", "Malformed request", detail)
}

func invalidParameter(detail string) problem {
	return newProblem(http.StatusBadRequest, "invalid_parameter", "Invalid parameter", detail)
}

func unknownField(path string) problem {
	return newProblem(http.StatusBadRequest, "unknown_field", "Unknown field", "Unknown field "+`"`+path+`".`)
}

func preconditionRequired() problem {
	return newProblem(http.StatusPreconditionRequired, "precondition_required", "Precondition required",
		"Send the resource ETag in the If-Match header.")
}

func payloadTooLarge() problem {
	return newProblem(http.StatusRequestEntityTooLarge, "payload_too_large", "Payload too large",
		"The request body exceeds the limit for this route.")
}

// toProblem is the one mapper from a domain error to a problem (API.md §4).
// Unknown errors become a generic 500 carrying only the request id; the second
// result tells the caller to log the underlying error.
func toProblem(err error, requestID string) (p problem, unexpected bool) {
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
		bodyTooBig *http.MaxBytesError
	)
	switch {
	case errors.As(err, &notFound):
		return newProblem(http.StatusNotFound, "not_found", "Not found", capitalize(notFound.Resource)+" not found."), false
	case errors.As(err, &stale):
		p = newProblem(http.StatusPreconditionFailed, "version_conflict", "Resource was modified",
			"The resource was changed by someone else. Reload and try again.")
		p.CurrentVersion = &stale.Current
		return p, false
	case errors.As(err, &invalid):
		p = newProblem(http.StatusUnprocessableEntity, "validation_failed", "Validation failed", "One or more fields are invalid.")
		p.Errors = make([]fieldProblem, 0, len(invalid.Errors))
		for _, fe := range invalid.Errors {
			p.Errors = append(p.Errors, fieldProblem{Field: fe.Field, Message: fe.Message})
		}
		return p, false
	case errors.As(err, &unknown):
		return unknownField(unknown.Path), false
	case errors.As(err, &conflict):
		return conflictProblem(conflict), false
	case errors.As(err, &tooLarge), errors.As(err, &bodyTooBig):
		return payloadTooLarge(), false
	case errors.As(err, &badMedia):
		return newProblem(http.StatusUnsupportedMediaType, "unsupported_media_type", "Unsupported media type",
			"Only JPEG, PNG and WebP images are accepted."), false
	case errors.As(err, &lookupOff):
		return newProblem(http.StatusServiceUnavailable, "lookup_disabled", "Lookup disabled",
			"TMDB lookup is not configured on this server."), false
	case errors.As(err, &upstream):
		return newProblem(http.StatusServiceUnavailable, "upstream_unavailable", "Lookup unavailable",
			"TMDB is temporarily unavailable. Try again later."), false
	default:
		p = newProblem(http.StatusInternalServerError, "internal", "Internal error", "Something went wrong.")
		p.RequestID = requestID
		return p, true
	}
}

func conflictProblem(c *apperr.ConflictError) problem {
	switch c.Code {
	case apperr.CodeCycleDetected:
		return newProblem(http.StatusConflict, string(c.Code), "Cycle detected",
			"A folder cannot be moved into itself or one of its descendants.")
	case apperr.CodeParentInTrash:
		p := newProblem(http.StatusConflict, string(c.Code), "Parent is in the trash",
			"Restore the parent first, or choose a destination.")
		if c.Blocking != nil {
			p.BlockingParent = &blockingParent{Type: c.Blocking.Type, ID: c.Blocking.ID, Name: c.Blocking.Name}
		}
		return p
	case apperr.CodeDestinationUnavailable:
		return newProblem(http.StatusConflict, string(c.Code), "Destination unavailable",
			"The destination does not exist or is in the trash.")
	case apperr.CodeTypeMismatch:
		return newProblem(http.StatusConflict, string(c.Code), "Type mismatch",
			"The destination list has a different type than the item.")
	case apperr.CodeOrderStale:
		return newProblem(http.StatusConflict, string(c.Code), "Order is stale",
			"The items changed. Reload and try again.")
	default:
		return newProblem(http.StatusConflict, string(c.Code), "Conflict", "The request conflicts with the current state.")
	}
}

func capitalize(s string) string {
	if s == "" {
		return "Resource"
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// handlerErrors are the error callbacks plugged into the generated server.
type handlerErrors struct{ log *slog.Logger }

// request answers body decoding errors (strict server, RequestErrorHandlerFunc).
func (h handlerErrors) request(w http.ResponseWriter, r *http.Request, err error) {
	if p, _ := toProblem(err, ""); p.Code != "internal" {
		writeProblem(w, p) // oversize body, etc.
		return
	}
	writeProblem(w, malformedRequest("The request body is not valid JSON."))
}

// parameter answers path, query and header binding errors (ChiServerOptions.ErrorHandlerFunc).
func (h handlerErrors) parameter(w http.ResponseWriter, _ *http.Request, err error) {
	writeProblem(w, invalidParameter(parameterDetail(err)))
}

// response answers errors returned by handlers (strict server, ResponseErrorHandlerFunc).
func (h handlerErrors) response(w http.ResponseWriter, r *http.Request, err error) {
	p, unexpected := toProblem(err, requestIDFrom(r.Context()))
	if unexpected {
		h.log.ErrorContext(r.Context(), "unhandled error", "err", err, "request_id", p.RequestID)
	}
	writeProblem(w, p)
}
