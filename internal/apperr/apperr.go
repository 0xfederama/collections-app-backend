// Package apperr holds the typed domain errors services return. It knows
// nothing about HTTP: internal/httpapi alone maps these to problem+json
// (ARCHITECTURE §2 Rules, API.md §4). Use errors.As to inspect them.
package apperr

import "fmt"

// Code is a machine-readable conflict reason (the `code` of a 409 problem).
type Code string

const (
	CodeCycleDetected          Code = "cycle_detected"
	CodeParentInTrash          Code = "parent_in_trash"
	CodeDestinationUnavailable Code = "destination_unavailable"
	CodeOrderStale             Code = "order_stale"
	CodeTypeMismatch           Code = "type_mismatch"
	CodeDuplicateTitle         Code = "duplicate_title"
	CodeFolderNotTopLevel      Code = "folder_not_top_level"
	CodeFolderShared           Code = "folder_shared"
)

// NotFoundError means the resource is missing or trashed.
type NotFoundError struct{ Resource string }

func (e *NotFoundError) Error() string { return e.Resource + " not found" }

// NotFound reports a missing (or trashed) resource such as "folder" or "item".
func NotFound(resource string) error { return &NotFoundError{Resource: resource} }

// ForbiddenError means the caller can see the resource but only its owner may do this (403).
type ForbiddenError struct{}

func (e *ForbiddenError) Error() string { return "forbidden: only the owner may do this" }

// Forbidden reports an owner-only operation attempted by another user who can see the resource.
func Forbidden() error { return &ForbiddenError{} }

// VersionConflictError means a stale If-Match; Current is the stored version.
type VersionConflictError struct{ Current int }

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("version conflict: current version is %d", e.Current)
}

// VersionConflict reports a stale write against the current stored version.
func VersionConflict(current int) error { return &VersionConflictError{Current: current} }

// FieldError is one failed field rule (API.md §4, 422 `errors[]`).
type FieldError struct {
	Field   string
	Message string
}

// ValidationError carries every failed field rule of a request.
type ValidationError struct{ Errors []FieldError }

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed: %d field error(s)", len(e.Errors))
}

// Validation reports failed field rules. Pass at least one.
func Validation(errs ...FieldError) error { return &ValidationError{Errors: errs} }

// UnknownFieldError means a request carried a key the schema does not declare
// and the generic body guard could not see (free-form `metadata`). Path is the
// dotted path, e.g. "metadata.runtime".
type UnknownFieldError struct{ Path string }

func (e *UnknownFieldError) Error() string { return "unknown field " + e.Path }

// UnknownField reports an undeclared key at path (400 `unknown_field`).
func UnknownField(path string) error { return &UnknownFieldError{Path: path} }

// BlockingParent names the trashed ancestor that blocks a restore.
type BlockingParent struct {
	Type string // "folder" or "list"
	ID   string
	Name string
}

// ConflictError is a 409 with a stable Code.
type ConflictError struct {
	Code Code
	// Blocking is set for CodeParentInTrash only.
	Blocking *BlockingParent
}

func (e *ConflictError) Error() string { return "conflict: " + string(e.Code) }

// Cycle reports a folder moved into itself or a descendant.
func Cycle() error { return &ConflictError{Code: CodeCycleDetected} }

// ParentInTrash reports a restore without destination whose parent is trashed.
func ParentInTrash(b BlockingParent) error {
	return &ConflictError{Code: CodeParentInTrash, Blocking: &b}
}

// DestinationUnavailable reports a move, restore or create target that is missing or trashed.
func DestinationUnavailable() error { return &ConflictError{Code: CodeDestinationUnavailable} }

// TypeMismatch reports an item moved to a list of another type (F-007).
func TypeMismatch() error { return &ConflictError{Code: CodeTypeMismatch} }

// DuplicateTitle reports an item title already used in the target list (F-026).
func DuplicateTitle() error { return &ConflictError{Code: CodeDuplicateTitle} }

// OrderStale reports a reorder whose id set no longer matches the stored one.
func OrderStale() error { return &ConflictError{Code: CodeOrderStale} }

// PayloadTooLargeError means a body or upload exceeded its limit.
type PayloadTooLargeError struct{ Limit int64 }

func (e *PayloadTooLargeError) Error() string {
	return fmt.Sprintf("payload larger than %d bytes", e.Limit)
}

// PayloadTooLarge reports a body over limit bytes.
func PayloadTooLarge(limit int64) error { return &PayloadTooLargeError{Limit: limit} }

// UnsupportedMediaTypeError means an upload is not an accepted image type.
type UnsupportedMediaTypeError struct{ Detail string }

func (e *UnsupportedMediaTypeError) Error() string { return "unsupported media type: " + e.Detail }

// UnsupportedMediaType reports an upload that is not JPEG, PNG or WebP.
func UnsupportedMediaType(detail string) error { return &UnsupportedMediaTypeError{Detail: detail} }

// LookupDisabledError means a lookup-backed write was requested without a
// configured server-side credential.
type LookupDisabledError struct{}

func (e *LookupDisabledError) Error() string { return "lookup is disabled" }

// LookupDisabled reports that the optional lookup integration (TMDB, Open Library) is not configured or turned off.
func LookupDisabled() error { return &LookupDisabledError{} }

// UpstreamUnavailableError means a lookup provider (TMDB, Open Library) could not satisfy a request.
// It deliberately carries no underlying client error across the domain
// boundary, keeping upstream diagnostics and credentials out of API responses.
type UpstreamUnavailableError struct{}

func (e *UpstreamUnavailableError) Error() string { return "lookup provider is unavailable" }

// UpstreamUnavailable reports a failed request to a lookup provider.
func UpstreamUnavailable() error { return &UpstreamUnavailableError{} }

// FaviconNotFoundError means a site has no usable favicon (404 `favicon_not_found`).
type FaviconNotFoundError struct{}

func (e *FaviconNotFoundError) Error() string { return "favicon not found" }

// FaviconNotFound reports a site without a favicon, so the web shows its fallback.
func FaviconNotFound() error { return &FaviconNotFoundError{} }

// FolderNotTopLevel reports a share request for a folder that is not at the top level.
func FolderNotTopLevel() error { return &ConflictError{Code: CodeFolderNotTopLevel} }

// FolderShared reports a folder with members moved into another folder.
func FolderShared() error { return &ConflictError{Code: CodeFolderShared} }
