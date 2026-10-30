package lookup

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/google/uuid"
)

// typeKey is the list type key, "" when the list has no type (which no provider serves).
func typeKey(typ *string) string {
	if typ == nil {
		return ""
	}
	return *typ
}

func flatten(in map[string]any) map[string]any {
	out := map[string]any{}
	var walk func(string, any)
	walk = func(prefix string, value any) {
		if nested, ok := value.(map[string]any); ok {
			for key, child := range nested {
				path := key
				if prefix != "" {
					path = prefix + "." + key
				}
				walk(path, child)
			}
			return
		}
		if prefix != "" {
			out[prefix] = value
		}
	}
	for key, value := range in {
		walk(key, value)
	}
	return out
}

func getPath(in map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	cur := in
	for i, part := range parts {
		value, ok := cur[part]
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return value, true
		}
		cur, ok = value.(map[string]any)
		if !ok {
			return nil, false
		}
	}
	return nil, false
}

func setPath(in map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	cur := in
	for _, part := range parts[:len(parts)-1] {
		next, ok := cur[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[part] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = value
}

func deletePath(in map[string]any, path string) {
	parts := strings.Split(path, ".")
	parents := []map[string]any{in}
	cur := in
	for _, part := range parts[:len(parts)-1] {
		next, ok := cur[part].(map[string]any)
		if !ok {
			return
		}
		cur = next
		parents = append(parents, cur)
	}
	delete(cur, parts[len(parts)-1])
	for i := len(parts) - 2; i >= 0; i-- {
		child, ok := parents[i][parts[i]].(map[string]any)
		if !ok || len(child) != 0 {
			break
		}
		delete(parents[i], parts[i])
	}
}

func cloneLookupMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	b, _ := json.Marshal(in)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}

func equalValue(a, b any) bool {
	na, nb := normalizeValue(a), normalizeValue(b)
	return reflect.DeepEqual(na, nb)
}

func normalizeValue(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func sameJSON(a, b []byte) bool {
	var av, bv any
	return json.Unmarshal(a, &av) == nil && json.Unmarshal(b, &bv) == nil && reflect.DeepEqual(av, bv)
}

func imageURLChanged(a, b *string) bool {
	if a == nil || b == nil {
		return a != nil || b != nil
	}
	return *a != *b
}

func nullUUIDPtr(id uuid.NullUUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	return &id.UUID
}

func valueOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
