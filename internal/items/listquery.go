package items

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

// ListDefs are the list facts Query needs, loaded by the caller.
type ListDefs struct {
	SortMode    string        // the list's sort_mode, the default sort
	AllowsPrice bool          // the type's allows_price; false for a list with no type
	Fields      []types.Field // the type's fields; none for a list with no type
}

// Query filters, sorts and pages the active items of one list with the
// semantics of the SQL it replaces (ListItems and CountItems before B3d). all
// must be every active item of that list and typ its type. It returns the page
// and the number of matches. Validation runs in the order of the old service:
// price sort, sort field, filter field, filter value; the errors are
// ErrInvalidSort and ErrInvalidFilter. A list whose type definition is missing
// is the caller's to report as ErrInvalidSort before it calls Query.
func Query(all []Item, typ *string, defs ListDefs, p ListParams) ([]Item, int, error) {
	p = p.Normalized(defs.SortMode)
	if p.Sort == "price" && !defs.AllowsPrice {
		return nil, 0, ErrInvalidSort
	}
	sortType := ""
	if !builtinSort(p.Sort) {
		ft, ok := fieldType(typ, defs.Fields, p.Sort)
		if !ok || ft == "stores" {
			// A stores field is neither sortable nor filterable (F-016).
			return nil, 0, ErrInvalidSort
		}
		sortType = ft
	}
	filterType := ""
	if p.FilterField != "" {
		ft, ok := fieldType(typ, defs.Fields, p.FilterField)
		if !ok || !validFilter(ft, p.FilterValue, p.FilterMin, p.FilterMax) {
			return nil, 0, ErrInvalidFilter
		}
		filterType = ft
	} else if p.FilterValue != "" || p.FilterMin != "" || p.FilterMax != "" {
		return nil, 0, ErrInvalidFilter
	}

	rows := make([]row, 0, len(all))
	for _, it := range all {
		meta, err := decodeMetadata(it.Metadata)
		if err != nil {
			return nil, 0, err
		}
		r := row{item: it, meta: meta}
		if matches(r, typ, p, filterType) {
			rows = append(rows, r)
		}
	}
	for i := range rows {
		rows[i].key = sortKeyOf(rows[i], p.Sort, sortType)
	}
	slices.SortFunc(rows, func(a, b row) int { return compareRows(a, b, p.Sort, sortType) })

	total := len(rows)
	start := min(p.Offset, total)
	end := min(start+p.Limit, total)
	page := make([]Item, 0, end-start)
	for _, r := range rows[start:end] {
		page = append(page, r.item)
	}
	return page, total, nil
}

// row is one item with its decoded metadata and, for a field or price sort, its sort key.
type row struct {
	item Item
	meta map[string]any
	key  sortKey
}

// sortKey is the value a field or price sort orders by. null is SQL NULL, which
// sorts last. num holds numbers and prices, flag booleans, text the rest.
type sortKey struct {
	null bool
	num  *big.Rat
	flag bool
	text string
}

// fieldType is the type of a metadata field of the list's type. Built-in sort
// modes are not fields.
func fieldType(typ *string, fields []types.Field, key string) (string, bool) {
	if typ == nil || key == "" || builtinSort(key) {
		return "", false
	}
	for _, f := range fields {
		if f.Key == key {
			return f.Type, true
		}
	}
	return "", false
}

// matches is the WHERE clause of ListItems: the title or (movie only) director
// contains q, and the field filter holds.
func matches(r row, typ *string, p ListParams, filterType string) bool {
	if !matchesQ(r, typ, p.Q) {
		return false
	}
	if p.FilterField == "" {
		return true
	}
	return matchesFilter(r.meta[p.FilterField], filterType, p)
}

func matchesQ(r row, typ *string, q string) bool {
	needle := strings.ToLower(q)
	if strings.Contains(strings.ToLower(r.item.Title), needle) {
		return true
	}
	if typ == nil || *typ != types.Movie {
		return false
	}
	director, _ := textOf(r.meta["director"])
	return strings.Contains(strings.ToLower(director), needle)
}

// matchesFilter applies a field filter to one stored value. A missing or null
// value matches no range or bound, as SQL NULL does; text and link read a
// missing value as an empty string, so they match only a value that contains the filter.
func matchesFilter(v any, filterType string, p ListParams) bool {
	switch filterType {
	case "text", "link":
		s, _ := textOf(v)
		return strings.Contains(strings.ToLower(s), strings.ToLower(p.FilterValue))
	case "streaming":
		s, _ := streamingText(v)
		return strings.Contains(strings.ToLower(s), strings.ToLower(p.FilterValue))
	case "number":
		n, ok := numberOf(v)
		if !ok {
			return false
		}
		if p.FilterMin != "" && n.Cmp(ratOf(p.FilterMin)) < 0 {
			return false
		}
		if p.FilterMax != "" && n.Cmp(ratOf(p.FilterMax)) > 0 {
			return false
		}
		return true
	case "date":
		s, ok := textOf(v)
		if !ok {
			return false
		}
		if p.FilterMin != "" && s < p.FilterMin {
			return false
		}
		if p.FilterMax != "" && s > p.FilterMax {
			return false
		}
		return true
	case "boolean":
		b, ok := v.(bool)
		return ok && b == (p.FilterValue == "true")
	}
	return false
}

// sortKeyOf is the value a row sorts by for the sort mode. manual, title and
// updated need no key; price orders by the item's min price.
func sortKeyOf(r row, sortMode, sortType string) sortKey {
	if sortMode == "price" {
		if r.item.Price == nil || r.item.Price.Min == nil {
			return sortKey{null: true}
		}
		n, ok := new(big.Rat).SetString(*r.item.Price.Min)
		return sortKey{null: !ok, num: n}
	}
	if sortType == "" {
		return sortKey{}
	}
	v := r.meta[sortMode]
	switch sortType {
	case "number":
		n, ok := numberOf(v)
		return sortKey{null: !ok, num: n}
	case "date":
		s, ok := textOf(v)
		return sortKey{null: !ok, text: s}
	case "text", "link":
		// NULLIF(value, ''): an empty value sorts last like a missing one.
		s, ok := textOf(v)
		if !ok || s == "" {
			return sortKey{null: true}
		}
		return sortKey{text: strings.ToLower(s)}
	case "streaming":
		s, ok := streamingText(v)
		return sortKey{null: !ok, text: strings.ToLower(s)}
	case "boolean":
		b, ok := v.(bool)
		return sortKey{null: !ok, flag: b}
	}
	return sortKey{null: true}
}

// compareRows orders two rows: the sort mode, then the id (ties break on id in every sort).
func compareRows(a, b row, sortMode, sortType string) int {
	if c := compareMain(a, b, sortMode, sortType); c != 0 {
		return c
	}
	return bytes.Compare(a.item.ID[:], b.item.ID[:])
}

func compareMain(a, b row, sortMode, sortType string) int {
	switch sortMode {
	case SortManual:
		return cmp.Compare(a.item.SortOrder, b.item.SortOrder)
	case SortTitle:
		return strings.Compare(strings.ToLower(a.item.Title), strings.ToLower(b.item.Title))
	case SortUpdated:
		// Newest first.
		return b.item.UpdatedAt.Compare(a.item.UpdatedAt)
	case "price":
		if c := compareKeys(a.key, b.key, "number"); c != 0 {
			return c
		}
		return strings.Compare(strings.ToLower(a.item.Title), strings.ToLower(b.item.Title))
	}
	return compareKeys(a.key, b.key, sortType)
}

// compareKeys orders two sort keys ascending with NULLs last. Booleans sort
// false before true.
func compareKeys(a, b sortKey, kind string) int {
	switch {
	case a.null && b.null:
		return 0
	case a.null:
		return 1
	case b.null:
		return -1
	}
	switch kind {
	case "number", "price":
		return a.num.Cmp(b.num)
	case "boolean":
		switch {
		case a.flag == b.flag:
			return 0
		case !a.flag:
			return -1
		default:
			return 1
		}
	}
	return strings.Compare(a.text, b.text)
}

// Filter bounds and sort keys of numbers use the decimal grammar of a
// Postgres numeric literal, the same as the cast the SQL did.
var numberRe = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

// validFilter is the rule set of one field filter (F-019 list semantics, T0).
func validFilter(ft, value, lo, hi string) bool {
	switch ft {
	case "text", "link", "streaming":
		return value != "" && lo == "" && hi == ""
	case "boolean":
		return (value == "true" || value == "false") && lo == "" && hi == ""
	case "number":
		return value == "" && (lo != "" || hi != "") && validNumber(lo) && validNumber(hi) && validNumberRange(lo, hi)
	case "date":
		return value == "" && (lo != "" || hi != "") && validDate(lo) && validDate(hi) && (lo == "" || hi == "" || lo <= hi)
	default:
		return false
	}
}

func validNumberRange(lo, hi string) bool {
	if lo == "" || hi == "" {
		return true
	}
	a, ok := new(big.Rat).SetString(lo)
	if !ok {
		return false
	}
	b, ok := new(big.Rat).SetString(hi)
	return ok && a.Cmp(b) <= 0
}

func validNumber(raw string) bool {
	if raw == "" {
		return true
	}
	v, err := strconv.ParseFloat(raw, 64)
	return err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) && numberRe.MatchString(raw)
}

func validDate(raw string) bool {
	if raw == "" {
		return true
	}
	_, err := time.Parse(time.DateOnly, raw)
	return err == nil
}

// ratOf parses a bound that validFilter already accepted.
func ratOf(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return new(big.Rat)
	}
	return r
}

// numberOf reads a stored value as a number, the way (metadata ->> key)::numeric does.
func numberOf(v any) (*big.Rat, bool) {
	s, ok := textOf(v)
	if !ok {
		return nil, false
	}
	n, ok := new(big.Rat).SetString(s)
	return n, ok
}

// decodeMetadata reads the stored metadata object. Numbers keep their literal
// text (json.Number), which the jsonb text and the numeric compares need.
func decodeMetadata(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode item metadata: %w", err)
	}
	return m, nil
}

// textOf is the Postgres ->> operator: a string is itself, a number or boolean
// its JSON text, an object or array its jsonb text. JSON null and a missing key
// are SQL NULL (ok false).
func textOf(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	default:
		return jsonText(x), true
	}
}

// streamingText is the streaming filter and sort value: an object without its
// server-managed link as jsonb text, anything else as textOf.
func streamingText(v any) (string, bool) {
	obj, ok := v.(map[string]any)
	if !ok {
		return textOf(v)
	}
	without := make(map[string]any, len(obj))
	for k, val := range obj {
		if k != "link" {
			without[k] = val
		}
	}
	return jsonText(without), true
}

// jsonText renders a value the way Postgres prints jsonb (::text): object keys
// shorter first and then bytewise, ": " and ", " separators, strings quoted
// with Postgres escapes.
func jsonText(v any) string {
	var b strings.Builder
	appendJSON(&b, v)
	return b.String()
}

func appendJSON(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case json.Number:
		b.WriteString(x.String())
	case string:
		appendQuoted(b, x)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			appendJSON(b, e)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, c string) int {
			if len(a) != len(c) {
				return len(a) - len(c)
			}
			return strings.Compare(a, c)
		})
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			appendQuoted(b, k)
			b.WriteString(": ")
			appendJSON(b, x[k])
		}
		b.WriteByte('}')
	default:
		b.WriteString("null")
	}
}

// appendQuoted writes s as a JSON string with the escapes Postgres uses for jsonb.
func appendQuoted(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}
