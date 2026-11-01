package lists

import "github.com/0xfederama/collections-app-backend/internal/apperr"

// updatePlan is what one PATCH writes, decided without I/O. The service applies it inside one transaction, after
// it has locked the row, checked the version and read the definition of the type that applies after the PATCH:
//
//  1. TypeChanged: set the type to NewType clear the metadata of
//     every item of the list (ClearMetadata), and clear their price too when ClearPrice.
//  2. Write: UPDATE the row with the values below. Write is the row update only: a type change that leaves the
//     row as it is (same sort) sets TypeChanged and not Write.
//
// Neither TypeChanged nor Write is a no-op: nothing is written and the version does not move.
type updatePlan struct {
	Write         bool
	TypeChanged   bool
	NewType       *string
	ClearMetadata bool
	ClearPrice    bool

	Name        string
	Description *string
	DisplayMode string
	SortMode    string
	Icon        *string
	IconColor   *string
}

// planUpdate decides what a PATCH of cur does. cur is the locked row, def and fields the definition of the type
// the list has after the patch (zero and nil for no type), p the patch as normalizePatch returns it. The caller
// runs the version check first and resolves the type (an unknown type is the caller's unknownListType()), so the
// order of the errors is: sort, then icon and colour.
//
// The sort is the patch's sort, or on a type change the new type's default sort field (a manual sort when that
// is not valid), or else the stored sort, which must still be valid for the type. The icon colour is cleared
// when the icon is not a built-in one.
func planUpdate(cur List, def TypeInfo, fields []FieldInfo, p Patch) (updatePlan, error) {
	typeChanged := p.Type.Set && !equalPtr(p.Type.Value, cur.Type)
	newType := cur.Type
	if p.Type.Set {
		newType = p.Type.Value
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
			return updatePlan{}, err
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
	if errs := iconErrors(newIcon, sentColor); len(errs) > 0 {
		return updatePlan{}, apperr.Validation(errs...)
	}
	if !isBuiltinIcon(newIcon) {
		newIconColor = nil
	}
	iconChanged := !equalPtr(newIcon, cur.Icon) || !equalPtr(newIconColor, cur.IconColor)

	// A field sent with its stored value changes nothing.
	fieldsSet := (p.Name != nil && *p.Name != cur.Name) ||
		(p.Description.Set && !equalPtr(p.Description.Value, cur.Description)) ||
		(p.DisplayMode != nil && *p.DisplayMode != cur.DisplayMode) ||
		(p.SortMode != nil && *p.SortMode != cur.SortMode)

	fieldsSet = fieldsSet || iconChanged
	if !typeChanged && !fieldsSet {
		return updatePlan{}, nil
	}
	if typeChanged && p.SortMode == nil && cur.SortMode != newSort {
		fieldsSet = true
	}

	plan := updatePlan{
		Write:         fieldsSet,
		TypeChanged:   typeChanged,
		ClearMetadata: typeChanged,
		ClearPrice:    typeChanged && !def.AllowsPrice,
		Name:          cur.Name,
		Description:   cur.Description,
		DisplayMode:   cur.DisplayMode,
		SortMode:      newSort,
		Icon:          newIcon,
		IconColor:     newIconColor,
	}
	if typeChanged {
		plan.NewType = newType
	}
	if p.Name != nil {
		plan.Name = *p.Name
	}
	if p.Description.Set {
		plan.Description = p.Description.Value
	}
	if p.DisplayMode != nil {
		plan.DisplayMode = *p.DisplayMode
	}
	return plan, nil
}
