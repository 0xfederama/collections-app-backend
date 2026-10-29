package tree

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/lists"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
)

// CreateListInput is a new list. A nil Type is a list with no type. Empty DisplayMode / SortMode take the defaults.
type CreateListInput struct {
	Name        string
	Type        *string
	FolderID    *uuid.UUID
	Description *string
	DisplayMode string
	SortMode    string
	// PinnedFields nil = the type default.
	PinnedFields *PinnedFields
	// Icon and IconColor nil = the default icon (F012-B1).
	Icon      *string
	IconColor *string
}

// ListPatch is a partial list update; absent fields stay as they are.
type ListPatch struct {
	Name        *string
	Type        Optional[string] // a nil Value is no type
	Description Optional[string]
	DisplayMode *string
	SortMode    *string
	// PinnedFields: Set with a nil Value resets to the type default.
	PinnedFields Optional[PinnedFields]
	// Icon and IconColor (F012-B1): a nil Value clears them. A changed icon that is not built-in clears the colour.
	Icon      Optional[string]
	IconColor Optional[string]
}

// CreateList appends a list after its active siblings. A missing or trashed
// folder is DestinationUnavailable.
func (s *Service) CreateList(ctx context.Context, in CreateListInput) (List, error) {
	if in.DisplayMode == "" {
		in.DisplayMode = DefaultDisplayMode
	}
	if err := validateDescription(in.Description); err != nil {
		return List{}, err
	}
	in.Description = nilIfEmpty(in.Description)
	name, errs := validateListFields(&in.Name, &in.DisplayMode, &in.SortMode)
	errs = append(errs, typeErrors(in.Type)...)
	errs = append(errs, lists.IconErrors(in.Icon, in.IconColor)...)
	if err := validate(errs); err != nil {
		return List{}, err
	}
	var id uuid.UUID
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		def, fields, err := s.typeFields(ctx, q, in.Type)
		if err != nil {
			return unknownListType()
		}
		if in.SortMode == "" {
			in.SortMode = DefaultSortMode
			if def.DefaultSortField != nil {
				in.SortMode = *def.DefaultSortField
			}
		}
		if err := validateDefinitionSort(in.Type, in.SortMode, def.AllowsPrice, fields, "sort_mode"); err != nil {
			return err
		}
		if in.PinnedFields != nil {
			in.PinnedFields, err = validateDefinitionPins(def.AllowsPrice, fields, in.PinnedFields)
			if err != nil {
				return err
			}
		}
		pinnedJSON, err := pinnedToJSON(in.PinnedFields)
		if err != nil {
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
			WorkspaceID: s.workspaceID, FolderID: nullUUID(in.FolderID), Name: name, Type: in.Type, Description: in.Description,
			DisplayMode: in.DisplayMode, SortMode: in.SortMode, PinnedFields: pinnedJSON, Icon: in.Icon, IconColor: in.IconColor, SortOrder: next, Now: s.now(),
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
// A type that differs from the stored one (F-007) takes the tree lock first
// (same order as every structural write: tree lock, then the row lock), then
// updates the list and clears the metadata of all its items, trashed ones
// included, in the same transaction. A type equal to the stored one is a no-op.
// the resulting sort and pins are validated against that type's field definitions.
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
	if p.Type.Set {
		errs = append(errs, typeErrors(p.Type.Value)...)
	}
	if err := validate(errs); err != nil {
		return List{}, err
	}
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if p.Type.Set {
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
		typeChanged := p.Type.Set && !equalPtr(p.Type.Value, cur.Type)
		newType := cur.Type
		if p.Type.Set {
			newType = p.Type.Value
		}
		def, fields, err := s.typeFields(ctx, q, newType)
		if err != nil {
			return unknownListType()
		}
		newSort := cur.SortMode
		if p.SortMode != nil {
			newSort = *p.SortMode
		} else if typeChanged && def.DefaultSortField != nil {
			newSort = *def.DefaultSortField
		}
		if err := validateDefinitionSort(newType, newSort, def.AllowsPrice, fields, "sort_mode"); err != nil {
			if typeChanged && p.SortMode == nil {
				newSort = DefaultSortMode
			} else {
				return err
			}
		}
		// The icon that applies after this PATCH. A colour needs a built-in icon, and the stored colour
		// goes when the icon becomes an emoji or none (F012-B1).
		newIcon := cur.Icon
		if p.Icon.Set {
			newIcon = p.Icon.Value
		}
		newIconColor := cur.IconColor
		if p.IconColor.Set {
			newIconColor = p.IconColor.Value
		}
		var sentColor *string
		if p.IconColor.Set {
			sentColor = p.IconColor.Value
		}
		if errs := lists.IconErrors(newIcon, sentColor); len(errs) > 0 {
			return apperr.Validation(errs...)
		}
		if !lists.IsBuiltinIcon(newIcon) {
			newIconColor = nil
		}
		iconChanged := !equalPtr(newIcon, cur.Icon) || !equalPtr(newIconColor, cur.IconColor)
		// A field sent with its stored value changes nothing: no write, no version bump.
		fieldsSet := (p.Name != nil && *p.Name != cur.Name) ||
			(p.Description.Set && !equalPtr(p.Description.Value, cur.Description)) ||
			(p.DisplayMode != nil && *p.DisplayMode != cur.DisplayMode) ||
			(p.SortMode != nil && *p.SortMode != cur.SortMode)
		curPinned := pinnedFromJSON(cur.PinnedFields)
		var newPinned *PinnedFields
		if p.PinnedFields.Set {
			if p.PinnedFields.Value != nil {
				var perr error
				newPinned, perr = validateDefinitionPins(def.AllowsPrice, fields, p.PinnedFields.Value)
				if perr != nil {
					return perr
				}
			}
			// After a type change the stored value is NULL (UpdateListType), whatever was there.
			base := curPinned
			if typeChanged {
				base = nil
			}
			if !equalPinned(base, newPinned) {
				fieldsSet = true
			}
		}
		fieldsSet = fieldsSet || iconChanged
		if !typeChanged && !fieldsSet {
			return nil
		}
		resetSort := false
		if typeChanged {
			if p.SortMode == nil && cur.SortMode != newSort {
				resetSort = true
				fieldsSet = true
			}
			if _, err := q.UpdateListType(ctx, sqlc.UpdateListTypeParams{Type: p.Type.Value, Now: s.now(), ID: id, WorkspaceID: s.workspaceID}); err != nil {
				return fmt.Errorf("update list type: %w", err)
			}
			if _, err := q.ClearListItemsMetadata(ctx, sqlc.ClearListItemsMetadataParams{Now: s.now(), ListID: id, WorkspaceID: s.workspaceID}); err != nil {
				return fmt.Errorf("clear items metadata: %w", err)
			}
			if !def.AllowsPrice {
				if _, err := q.ClearListItemsPrice(ctx, sqlc.ClearListItemsPriceParams{Now: s.now(), ListID: id, WorkspaceID: s.workspaceID}); err != nil {
					return fmt.Errorf("clear items price: %w", err)
				}
			}
		}
		if p.SortMode != nil {
			if err := validateDefinitionSort(newType, *p.SortMode, def.AllowsPrice, fields, "sort_mode"); err != nil {
				return err
			}
		}
		if !fieldsSet {
			return nil
		}
		arg := sqlc.UpdateListParams{
			Name: cur.Name, Description: cur.Description, DisplayMode: cur.DisplayMode, SortMode: cur.SortMode,
			Icon: newIcon, IconColor: newIconColor,
			Now: s.now(), ID: id, WorkspaceID: s.workspaceID,
		}
		keep := curPinned
		if typeChanged {
			keep = nil // reset by UpdateListType in this transaction
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
		} else if resetSort {
			arg.SortMode = newSort
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

// validatePinned checks the ordered keys against the type and returns them normalized (never nil).
func (s *Service) typeFields(ctx context.Context, q *sqlc.Queries, typ *string) (sqlc.TypeDefinition, []sqlc.TypeField, error) {
	if typ == nil {
		// A list with no type has no definition: no fields, no price, no default sort field.
		return sqlc.TypeDefinition{}, nil, nil
	}
	def, err := q.GetTypeDefinitionByKey(ctx, sqlc.GetTypeDefinitionByKeyParams{WorkspaceID: s.workspaceID, Key: *typ})
	if err != nil {
		return sqlc.TypeDefinition{}, nil, err
	}
	fields, err := q.ListTypeFields(ctx, sqlc.ListTypeFieldsParams{WorkspaceID: s.workspaceID, TypeID: def.ID})
	return def, fields, err
}

func validateDefinitionSort(typ *string, sortMode string, allowsPrice bool, fields []sqlc.TypeField, field string) error {
	if sortMode == "manual" || sortMode == "title" || sortMode == "updated" || (sortMode == "price" && allowsPrice) {
		return nil
	}
	for _, f := range fields {
		if f.Key == sortMode && f.FieldType != "stores" && sortMode != "manual" && sortMode != "title" && sortMode != "updated" && sortMode != "price" {
			return nil
		}
	}
	if typ == nil {
		return apperr.Validation(apperr.FieldError{Field: field, Message: "must be a built-in sort mode (a list with no type has no fields)"})
	}
	return apperr.Validation(apperr.FieldError{Field: field, Message: fmt.Sprintf("must be a built-in sort mode or a field of type %s", *typ)})
}

func validateDefinitionPins(allowsPrice bool, fields []sqlc.TypeField, in *PinnedFields) (*PinnedFields, error) {
	if len(*in) > 3 {
		return nil, apperr.Validation(apperr.FieldError{Field: "pinned_fields", Message: "must have at most 3 entries"})
	}
	seen := make(map[string]bool, len(*in))
	for i, key := range *in {
		valid := key == "price" && allowsPrice
		for _, f := range fields {
			valid = valid || f.Key == key
		}
		if !valid {
			return nil, apperr.Validation(apperr.FieldError{Field: fmt.Sprintf("pinned_fields[%d]", i), Message: "must name a field of this type"})
		}
		if seen[key] {
			return nil, apperr.Validation(apperr.FieldError{Field: fmt.Sprintf("pinned_fields[%d]", i), Message: "must be unique"})
		}
		seen[key] = true
	}
	p := *in
	if p == nil {
		p = PinnedFields{}
	}
	return &p, nil
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

// typeErrors checks that a given type key is not blank (nil is a list with no
// type); existence is checked against this workspace's definitions inside the
// write transaction.
func typeErrors(typ *string) []apperr.FieldError {
	if typ != nil && strings.TrimSpace(*typ) == "" {
		return []apperr.FieldError{{Field: "type", Message: "must not be empty (send null for no type)"}}
	}
	return nil
}

func unknownListType() error {
	return apperr.Validation(apperr.FieldError{Field: "type", Message: "must name a type defined in this workspace"})
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
	return errs
}

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
