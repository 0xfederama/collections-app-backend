package items

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

// typeFields returns the fields a metadata object is checked against: a list
// with no type (nil) has none, so only an empty object is valid.
func typeFields(typ *string, fields []types.Field) []types.Field {
	if typ == nil {
		return nil
	}
	return fields
}

// ValidateMetadata checks metadata sent by a client against the fields of the
// list's type and returns the normalized map to store. stored is the item's
// current metadata (nil on create). streaming.link is server-managed (TMDB
// lookup): a client-sent link is dropped, and the stored one is kept while the
// streaming country is unchanged, so a client that round-trips the whole
// streaming object, or edits only the platforms, does not lose it.
func ValidateMetadata(typ *string, fields []types.Field, in, stored map[string]any) (map[string]any, error) {
	fields = typeFields(typ, fields)
	if in == nil {
		in = map[string]any{}
	}
	var out map[string]any
	for _, f := range fields {
		if f.Type != "streaming" && (!f.Special || f.Key != "streaming") {
			continue
		}
		obj, ok := in[f.Key].(map[string]any)
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(in))
			for k, v := range in {
				out[k] = v
			}
		}
		next := make(map[string]any, len(obj))
		for k, v := range obj {
			if k != "link" {
				next[k] = v
			}
		}
		old, _ := stored[f.Key].(map[string]any)
		country, cok := next["country"].(string)
		oldCountry, ook := old["country"].(string)
		if link, ok := old["link"].(string); ok && cok && ook && country == oldCountry {
			next["link"] = link
		}
		out[f.Key] = next
	}
	if out != nil {
		in = out
	}
	return types.Validate(fields, in)
}

// ValidateImport checks metadata written by the lookup (TMDB or Open Library),
// which may carry streaming.link: unlike ValidateMetadata it keeps the link as
// given. A nil type has no fields.
func ValidateImport(typ *string, fields []types.Field, in map[string]any) (map[string]any, error) {
	if in == nil {
		in = map[string]any{}
	}
	return types.Validate(typeFields(typ, fields), in)
}

// PriceAllowed is the 422 on price for a list type without a price: allowsPrice
// is the type definition's allows_price, false when the list has no type.
func PriceAllowed(allowsPrice bool) error {
	if allowsPrice {
		return nil
	}
	return apperr.Validation(apperr.FieldError{Field: "price", Message: "items of this type have no price"})
}

// Editable is the editable state of an item: the values a PATCH merges into.
// Metadata is the stored JSON object.
type Editable struct {
	Title    string
	Price    *Price
	Website  *string
	Metadata json.RawMessage
}

// MergePatch applies a patch that CleanPatch accepted to the current state of a
// locked item. It returns the full replacement values and whether anything
// differs from cur (a patch that changes nothing does not bump the version, D-059).
// typ and fields are the type of the item's list, which the metadata is checked
// against. It does not check the price rule (PriceAllowed): the service runs that
// before the merge, as Update did.
func MergePatch(cur Editable, typ *string, fields []types.Field, p Patch) (Editable, bool, error) {
	arg := cur
	changed := false
	if p.Title != nil && *p.Title != cur.Title {
		arg.Title, changed = *p.Title, true
	}
	if p.Website.Set && !equalPtr(p.Website.Value, cur.Website) {
		arg.Website, changed = p.Website.Value, true
	}
	if p.Price.Set && !samePrice(p.Price.Value, cur.Price) {
		var next *Price
		if p.Price.Value != nil {
			n := p.Price.Value.normalized()
			next = &n
		}
		arg.Price, changed = next, true
	}
	if p.Metadata != nil {
		var stored map[string]any
		if err := json.Unmarshal(cur.Metadata, &stored); err != nil {
			return cur, false, fmt.Errorf("decode item metadata: %w", err)
		}
		clean, err := ValidateMetadata(typ, fields, p.Metadata, stored)
		if err != nil {
			return cur, false, err
		}
		meta, err := json.Marshal(clean)
		if err != nil {
			return cur, false, fmt.Errorf("encode metadata: %w", err)
		}
		if !sameJSON(meta, cur.Metadata) {
			arg.Metadata, changed = meta, true
		}
	}
	return arg, changed, nil
}

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// samePrice compares two prices by value, with an amount folded into min and max.
func samePrice(a, b *Price) bool {
	if a == nil || b == nil {
		return a == b
	}
	na, nb := a.normalized(), b.normalized()
	return equalPtr(na.Min, nb.Min) && equalPtr(na.Max, nb.Max) && na.Currency == nb.Currency
}

// sameJSON compares two JSON documents by value (key order, spacing and
// number formatting do not matter).
func sameJSON(a, b []byte) bool {
	canon := func(raw []byte) []byte {
		var v any
		if json.Unmarshal(raw, &v) != nil {
			return raw
		}
		out, err := json.Marshal(v)
		if err != nil {
			return raw
		}
		return out
	}
	return bytes.Equal(canon(a), canon(b))
}
