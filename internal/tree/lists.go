package tree

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/database"
	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/items/kinds"
)

// CreateListInput is a new list. Kind is required. Empty DisplayMode / SortMode take the defaults.
type CreateListInput struct {
	Name        string
	Kind        string
	FolderID    *uuid.UUID
	Description *string
	DisplayMode string
	SortMode    string
	// PinnedFields nil = the kind default.
	PinnedFields *PinnedFields
}

// ListPatch is a partial list update; absent fields stay as they are.
type ListPatch struct {
	Name        *string
	Kind        *string
	Description Optional[string]
	DisplayMode *string
	SortMode    *string
	// PinnedFields: Set with a nil Value resets to the kind default.
	PinnedFields Optional[PinnedFields]
}

// CreateList appends a list after its active siblings. A missing or trashed
// folder is DestinationUnavailable.
func (s *Service) CreateList(ctx context.Context, in CreateListInput) (List, error) {
	if in.DisplayMode == "" {
		in.DisplayMode = DefaultDisplayMode
	}
	if in.SortMode == "" {
		in.SortMode = DefaultSortMode
	}
	if err := validateDescription(in.Description); err != nil {
		return List{}, err
	}
	in.Description = nilIfEmpty(in.Description)
	name, errs := validateListFields(&in.Name, &in.DisplayMode, &in.SortMode)
	errs = append(errs, kindErrors(&in.Kind)...)
	if kinds.Known(in.Kind) {
		errs = append(errs, sortKindErrors(in.Kind, in.SortMode, "sort_mode")...)
	}
	if in.PinnedFields != nil && kinds.Known(in.Kind) {
		var perr error
		in.PinnedFields, perr = validatePinned(in.Kind, in.PinnedFields)
		errs = appendValidation(errs, perr)
	}
	if err := validate(errs); err != nil {
		return List{}, err
	}
	pinnedJSON, err := pinnedToJSON(in.PinnedFields)
	if err != nil {
		return List{}, err
	}
	var id uuid.UUID
	err = database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		if err := s.requireActiveFolder(ctx, q, in.FolderID); err != nil {
			return err
		}
		next, err := q.NextListSortOrder(ctx, sqlc.NextListSortOrderParams{WorkspaceID: s.workspaceID, FolderID: nullUUID(in.FolderID)})
		if err != nil {
			return fmt.Errorf("next list sort order: %w", err)
		}
		row, err := q.InsertList(ctx, sqlc.InsertListParams{
			WorkspaceID: s.workspaceID, FolderID: nullUUID(in.FolderID), Name: name, Kind: in.Kind, Description: in.Description,
			DisplayMode: in.DisplayMode, SortMode: in.SortMode, PinnedFields: pinnedJSON, SortOrder: next, Now: s.now(),
		})
		if err != nil {
			return fmt.Errorf("insert list: %w", err)
		}
		id = row.ID
		return nil
	})
	if err != nil {
		return List{}, err
	}
	return s.getList(ctx, id, false)
}

// GetList returns an active list with its path (the ancestor folders).
func (s *Service) GetList(ctx context.Context, id uuid.UUID) (List, error) {
	return s.getList(ctx, id, true)
}

func (s *Service) getList(ctx context.Context, id uuid.UUID, withPath bool) (List, error) {
	q := sqlc.New(s.db)
	row, err := q.GetList(ctx, sqlc.GetListParams{ID: id, WorkspaceID: s.workspaceID})
	if err != nil {
		return List{}, notFoundOr(err, "list")
	}
	l := listFromRow(row)
	if withPath {
		l.Path = []PathFolder{}
		if l.FolderID != nil {
			rows, err := q.FolderPath(ctx, sqlc.FolderPathParams{FolderID: *l.FolderID, WorkspaceID: s.workspaceID})
			if err != nil {
				return List{}, fmt.Errorf("list path: %w", err)
			}
			for _, r := range rows {
				l.Path = append(l.Path, PathFolder{ID: r.ID, Name: r.Name})
			}
		}
	}
	return l, nil
}

// UpdateList applies a partial update. An empty patch changes nothing and does
// not bump the version. version is the caller's If-Match version.
//
// A kind that differs from the stored one (F-007) takes the tree lock first
// (same order as every structural write: tree lock, then the row lock), then
// updates the list and clears the metadata of all its items, trashed ones
// included, in the same transaction. A kind equal to the stored one is a no-op.
// kind_key sorting is rejected (422 sort_mode) on a generic list, and a change to
// generic of a kind_key list is rejected too (422 kind).
func (s *Service) UpdateList(ctx context.Context, id uuid.UUID, version int, p ListPatch) (List, error) {
	var errs []apperr.FieldError
	if p.Name != nil {
		name, fe := cleanName(*p.Name)
		p.Name = &name
		if fe != nil {
			errs = append(errs, *fe)
		}
	}
	if p.Description.Set {
		if err := validateDescription(p.Description.Value); err != nil {
			return List{}, err
		}
		p.Description.Value = nilIfEmpty(p.Description.Value)
	}
	errs = append(errs, enumErrors(p.DisplayMode, p.SortMode)...)
	if p.Kind != nil {
		errs = append(errs, kindErrors(p.Kind)...)
		if p.SortMode != nil && kinds.Known(*p.Kind) {
			errs = append(errs, sortKindErrors(*p.Kind, *p.SortMode, "sort_mode")...)
		}
	}
	if err := validate(errs); err != nil {
		return List{}, err
	}
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if p.Kind != nil {
			if err := s.lockTree(ctx, q); err != nil {
				return err
			}
		}
		cur, err := q.LockList(ctx, sqlc.LockListParams{ID: id, WorkspaceID: s.workspaceID})
		if err != nil {
			return notFoundOr(err, "list")
		}
		if int(cur.Version) != version {
			return apperr.VersionConflict(int(cur.Version))
		}
		kindChanged := p.Kind != nil && *p.Kind != cur.Kind
		// A field sent with its stored value changes nothing: no write, no version bump.
		fieldsSet := (p.Name != nil && *p.Name != cur.Name) ||
			(p.Description.Set && !equalPtr(p.Description.Value, cur.Description)) ||
			(p.DisplayMode != nil && *p.DisplayMode != cur.DisplayMode) ||
			(p.SortMode != nil && *p.SortMode != cur.SortMode)
		curPinned := pinnedFromJSON(cur.PinnedFields)
		var newPinned *PinnedFields
		if p.PinnedFields.Set {
			// Validated against the kind that applies after this PATCH.
			kind := cur.Kind
			if p.Kind != nil {
				kind = *p.Kind
			}
			if p.PinnedFields.Value != nil {
				var perr error
				if newPinned, perr = validatePinned(kind, p.PinnedFields.Value); perr != nil {
					return perr
				}
			}
			// After a kind change the stored value is NULL (UpdateListKind), whatever was there.
			base := curPinned
			if kindChanged {
				base = nil
			}
			if !equalPinned(base, newPinned) {
				fieldsSet = true
			}
		}
		if !kindChanged && !fieldsSet {
			return nil
		}
		if kindChanged {
			// The sort mode that applies after the PATCH must still exist for the new kind.
			sortMode := cur.SortMode
			if p.SortMode != nil {
				sortMode = *p.SortMode
			}
			field := "kind"
			if p.SortMode != nil {
				field = "sort_mode"
			}
			if errs := sortKindErrors(*p.Kind, sortMode, field); len(errs) > 0 {
				return apperr.Validation(errs...)
			}
			if _, err := q.UpdateListKind(ctx, sqlc.UpdateListKindParams{Kind: *p.Kind, Now: s.now(), ID: id, WorkspaceID: s.workspaceID}); err != nil {
				return fmt.Errorf("update list kind: %w", err)
			}
			if _, err := q.ClearListItemsMetadata(ctx, sqlc.ClearListItemsMetadataParams{Now: s.now(), ListID: id, WorkspaceID: s.workspaceID}); err != nil {
				return fmt.Errorf("clear items metadata: %w", err)
			}
		} else if p.SortMode != nil {
			if errs := sortKindErrors(cur.Kind, *p.SortMode, "sort_mode"); len(errs) > 0 {
				return apperr.Validation(errs...)
			}
		}
		if !fieldsSet {
			return nil
		}
		arg := sqlc.UpdateListParams{
			Name: cur.Name, Description: cur.Description, DisplayMode: cur.DisplayMode, SortMode: cur.SortMode,
			Now: s.now(), ID: id, WorkspaceID: s.workspaceID,
		}
		keep := curPinned
		if kindChanged {
			keep = nil // reset by UpdateListKind in this transaction
		}
		if p.PinnedFields.Set {
			keep = newPinned
		}
		if arg.PinnedFields, err = pinnedToJSON(keep); err != nil {
			return err
		}
		if p.Name != nil {
			arg.Name = *p.Name
		}
		if p.Description.Set {
			arg.Description = p.Description.Value
		}
		if p.DisplayMode != nil {
			arg.DisplayMode = *p.DisplayMode
		}
		if p.SortMode != nil {
			arg.SortMode = *p.SortMode
		}
		if _, err := q.UpdateList(ctx, arg); err != nil {
			return fmt.Errorf("update list: %w", err)
		}
		return nil
	})
	if err != nil {
		return List{}, err
	}
	return s.getList(ctx, id, false)
}

// validatePinned checks the slots against the kind and returns them normalized (never nil).
func validatePinned(kind string, in *PinnedFields) (*PinnedFields, error) {
	under, right, err := kinds.ValidatePinned(kind, in.Under, in.Right)
	if err != nil {
		return nil, err
	}
	return &PinnedFields{Under: under, Right: right}, nil
}

// appendValidation adds the field errors of a validation error to errs. Any other error is not expected here.
func appendValidation(errs []apperr.FieldError, err error) []apperr.FieldError {
	var ve *apperr.ValidationError
	if errors.As(err, &ve) {
		return append(errs, ve.Errors...)
	}
	return errs
}

func equalPinned(a, b *PinnedFields) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.equal(*b)
}

// MoveList moves a list into a folder (nil = root), appended last. Errors in
// order: NotFound, VersionConflict, DestinationUnavailable.
func (s *Service) MoveList(ctx context.Context, id uuid.UUID, version int, folder *uuid.UUID) (List, error) {
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		cur, err := q.LockList(ctx, sqlc.LockListParams{ID: id, WorkspaceID: s.workspaceID})
		if err != nil {
			return notFoundOr(err, "list")
		}
		if int(cur.Version) != version {
			return apperr.VersionConflict(int(cur.Version))
		}
		if err := s.requireActiveFolder(ctx, q, folder); err != nil {
			return err
		}
		next, err := q.NextListSortOrder(ctx, sqlc.NextListSortOrderParams{WorkspaceID: s.workspaceID, FolderID: nullUUID(folder)})
		if err != nil {
			return fmt.Errorf("next list sort order: %w", err)
		}
		if _, err := q.MoveList(ctx, sqlc.MoveListParams{FolderID: nullUUID(folder), SortOrder: next, Now: s.now(), ID: id, WorkspaceID: s.workspaceID}); err != nil {
			return fmt.Errorf("move list: %w", err)
		}
		return nil
	})
	if err != nil {
		return List{}, err
	}
	return s.getList(ctx, id, false)
}

// ReorderLists rewrites the order of the child lists of folder (nil = root).
// ids must be exactly the active children, else OrderStale. A missing or
// trashed folder is NotFound.
func (s *Service) ReorderLists(ctx context.Context, folder *uuid.UUID, ids []uuid.UUID) error {
	return database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		if folder != nil {
			if _, err := q.GetFolder(ctx, sqlc.GetFolderParams{ID: *folder, WorkspaceID: s.workspaceID}); err != nil {
				return notFoundOr(err, "folder")
			}
		}
		current, err := q.ListActiveListIDsByFolder(ctx, sqlc.ListActiveListIDsByFolderParams{WorkspaceID: s.workspaceID, FolderID: nullUUID(folder)})
		if err != nil {
			return fmt.Errorf("list sibling lists: %w", err)
		}
		if !sameSet(current, ids) {
			return apperr.OrderStale()
		}
		if err := q.ReorderLists(ctx, sqlc.ReorderListsParams{Now: s.now(), WorkspaceID: s.workspaceID, Ids: ids}); err != nil {
			return fmt.Errorf("reorder lists: %w", err)
		}
		return nil
	})
}

// validateListFields checks a create request and returns the trimmed name.
func validateListFields(name *string, displayMode, sortMode *string) (string, []apperr.FieldError) {
	var errs []apperr.FieldError
	clean, fe := cleanName(*name)
	if fe != nil {
		errs = append(errs, *fe)
	}
	errs = append(errs, enumErrors(displayMode, sortMode)...)
	return clean, errs
}

// kindErrors checks a supplied kind: it must be one of the known kinds.
func kindErrors(kind *string) []apperr.FieldError {
	if kind == nil || !kinds.Known(*kind) {
		return []apperr.FieldError{{Field: "kind", Message: "must be one of generic, movie, tv_show, motorcycle, car, camera"}}
	}
	return nil
}

// sortKindErrors rejects the kind_key sort mode on a kind without a sort key.
func sortKindErrors(kind, sortMode, field string) []apperr.FieldError {
	if sortMode != "kind_key" {
		return nil
	}
	if _, ok := kinds.SortKey(kind); !ok {
		return []apperr.FieldError{{Field: field, Message: "kind_key is not available on lists of this kind"}}
	}
	return nil
}

// validateDescription applies the Markdown description rules, like the item
// content: at most 64 KiB of decoded UTF-8 bytes (413), valid UTF-8 without NUL
// (422; Postgres text cannot store NUL).
func validateDescription(d *string) error {
	if d == nil {
		return nil
	}
	if len(*d) > maxDescriptionBytes {
		return apperr.PayloadTooLarge(maxDescriptionBytes)
	}
	if !utf8.ValidString(*d) {
		return apperr.Validation(apperr.FieldError{Field: "description", Message: "must be valid UTF-8"})
	}
	if strings.ContainsRune(*d, 0) {
		return apperr.Validation(apperr.FieldError{Field: "description", Message: "must not contain NUL characters"})
	}
	return nil
}

// nilIfEmpty turns the empty description into nil: both clear it.
func nilIfEmpty(d *string) *string {
	if d != nil && *d == "" {
		return nil
	}
	return d
}

func enumErrors(displayMode, sortMode *string) []apperr.FieldError {
	var errs []apperr.FieldError
	if displayMode != nil && !displayModes[*displayMode] {
		errs = append(errs, apperr.FieldError{Field: "display_mode", Message: "must be one of compact, thumbnail, grid"})
	}
	if sortMode != nil && !sortModes[*sortMode] {
		errs = append(errs, apperr.FieldError{Field: "sort_mode", Message: "must be one of manual, title, updated, kind_key, price"})
	}
	return errs
}

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
