package httpapi_test

import (
	"reflect"
	"strings"
	"testing"
)

// iconFields returns the icon and icon_color of a list or search path segment, and whether both keys are present.
func iconFields(o obj) (icon, color any, present bool) {
	icon, okIcon := o["icon"]
	color, okColor := o["icon_color"]
	return icon, color, okIcon && okColor
}

func TestF012_ListIconCreate(t *testing.T) {
	e := newTreeEnv(t)
	tests := []struct {
		name, body, wantIcon, wantColor string
	}{
		{"no icon", `{"name":"A","kind":"generic"}`, "", ""},
		{"null icon", `{"name":"A","kind":"generic","icon":null,"icon_color":null}`, "", ""},
		{"emoji", `{"name":"A","kind":"movie","icon":"🎬"}`, "🎬", ""},
		{"emoji sequence", `{"name":"A","kind":"generic","icon":"👨‍👩‍👧"}`, "👨‍👩‍👧", ""},
		{"keycap", `{"name":"A","kind":"generic","icon":"1️⃣"}`, "1️⃣", ""},
		{"flag", `{"name":"A","kind":"generic","icon":"🇮🇹"}`, "🇮🇹", ""},
		{"built-in without colour", `{"name":"A","kind":"movie","icon":"i:film"}`, "i:film", ""},
		{"built-in with colour", `{"name":"A","kind":"movie","icon":"i:film","icon_color":"indigo"}`, "i:film", "indigo"},
		{"longest built-in name", `{"name":"A","kind":"generic","icon":"i:` + strings.Repeat("a", 40) + `"}`, "i:" + strings.Repeat("a", 40), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.call("POST", "/api/v1/lists", tt.body, "")
			wantStatus(t, rec, 201)
			l := decode(t, rec)
			icon, color, present := iconFields(l)
			if !present {
				t.Fatalf("icon fields missing: %v", l)
			}
			if tt.wantIcon == "" && icon != nil || tt.wantIcon != "" && icon != tt.wantIcon {
				t.Fatalf("icon = %v, want %q", icon, tt.wantIcon)
			}
			if tt.wantColor == "" && color != nil || tt.wantColor != "" && color != tt.wantColor {
				t.Fatalf("icon_color = %v, want %q", color, tt.wantColor)
			}
		})
	}
}

func TestF012_ListIconCreateInvalid(t *testing.T) {
	e := newTreeEnv(t)
	tests := []struct {
		name, body, field string
	}{
		{"empty icon", `{"name":"A","kind":"generic","icon":""}`, "icon"},
		{"plain word", `{"name":"A","kind":"generic","icon":"film"}`, "icon"},
		{"digits", `{"name":"A","kind":"generic","icon":"12"}`, "icon"},
		{"two emoji", `{"name":"A","kind":"generic","icon":"🎬🎬"}`, "icon"},
		{"emoji with text", `{"name":"A","kind":"generic","icon":"🎬 film"}`, "icon"},
		{"trailing space", `{"name":"A","kind":"generic","icon":"🎬 "}`, "icon"},
		{"control character", `{"name":"A","kind":"generic","icon":"🎬\u0007"}`, "icon"},
		{"uppercase built-in", `{"name":"A","kind":"movie","icon":"i:Film"}`, "icon"},
		{"empty built-in name", `{"name":"A","kind":"movie","icon":"i:"}`, "icon"},
		{"built-in name with space", `{"name":"A","kind":"movie","icon":"i:film camera"}`, "icon"},
		{"built-in name too long", `{"name":"A","kind":"movie","icon":"i:` + strings.Repeat("a", 41) + `"}`, "icon"},
		{"emoji with colour", `{"name":"A","kind":"movie","icon":"🎬","icon_color":"red"}`, "icon_color"},
		{"colour without icon", `{"name":"A","kind":"movie","icon_color":"red"}`, "icon_color"},
		{"unknown colour", `{"name":"A","kind":"movie","icon":"i:film","icon_color":"magenta"}`, "icon_color"},
		{"empty colour", `{"name":"A","kind":"movie","icon":"i:film","icon_color":""}`, "icon_color"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := wantProblem(t, e.call("POST", "/api/v1/lists", tt.body, ""), 422, "validation_failed")
			if len(p.Errors) == 0 || p.Errors[0].Field != tt.field {
				t.Fatalf("errors = %+v, want field %s", p.Errors, tt.field)
			}
		})
	}
	// Nothing was created by the rejected requests.
	if got := e.tree("lists"); len(got) != 0 {
		t.Fatalf("rejected creates left lists: %v", got)
	}
}

func TestF012_ListIconPatch(t *testing.T) {
	e := newTreeEnv(t)
	l := e.listOfKind("Films", nil, "movie")
	lid := l["id"].(string)

	step := func(name, body, ifMatch string, wantVersion float64, wantIcon, wantColor any) obj {
		t.Helper()
		rec := e.call("PATCH", "/api/v1/lists/"+lid, body, ifMatch)
		wantStatus(t, rec, 200)
		got := decode(t, rec)
		icon, color, present := iconFields(got)
		if !present || !reflect.DeepEqual(icon, wantIcon) || !reflect.DeepEqual(color, wantColor) || got["version"] != wantVersion {
			t.Fatalf("%s: icon=%v icon_color=%v version=%v (present %v), want %v %v version %v",
				name, icon, color, got["version"], present, wantIcon, wantColor, wantVersion)
		}
		return got
	}

	// Set a built-in icon with a colour, then recolour it alone.
	l = step("set built-in", `{"icon":"i:film","icon_color":"indigo"}`, etagOf("l", l), 2, "i:film", "indigo")
	l = step("recolour only", `{"icon_color":"teal"}`, etagOf("l", l), 3, "i:film", "teal")
	// The same values again change nothing.
	l = step("same values", `{"icon":"i:film","icon_color":"teal"}`, etagOf("l", l), 3, "i:film", "teal")
	// Clearing the colour alone is fine on a built-in icon.
	l = step("clear colour", `{"icon_color":null}`, etagOf("l", l), 4, "i:film", nil)

	// An emoji clears the colour; a colour that comes with it is rejected and changes nothing.
	wantProblem(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"icon":"🎬","icon_color":"red"}`, etagOf("l", l)), 422, "validation_failed")
	if got := e.fetch("lists", lid); got["version"] != float64(4) || got["icon"] != "i:film" {
		t.Fatalf("rejected patch changed the list: %v", got)
	}
	l = step("emoji", `{"icon":"🎬"}`, etagOf("l", l), 5, "🎬", nil)

	// A colour on an emoji is rejected, also when the icon is kept.
	wantProblem(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"icon_color":"red"}`, etagOf("l", l)), 422, "validation_failed")

	// Switching back to a built-in icon with a colour in one request.
	l = step("built-in again", `{"icon":"i:star","icon_color":"red"}`, etagOf("l", l), 6, "i:star", "red")

	// Switching to an emoji without a colour clears a stored colour (the default only applies to a built-in icon).
	l = step("emoji clears colour", `{"icon":"⭐"}`, etagOf("l", l), 7, "⭐", nil)

	// null resets the icon to the default, colour included.
	l = step("null icon", `{"icon":null}`, etagOf("l", l), 8, nil, nil)
	wantProblem(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"icon_color":"red"}`, etagOf("l", l)), 422, "validation_failed")
	if got := e.fetch("lists", lid); got["version"] != float64(8) {
		t.Fatalf("rejected colour changed the version: %v", got)
	}
	// A null icon again changes nothing.
	l = step("null icon again", `{"icon":null}`, etagOf("l", l), 8, nil, nil)

	// Invalid values and a stale version.
	tests := []struct {
		name, body, field string
	}{
		{"bad icon", `{"icon":"i:Star"}`, "icon"},
		{"bad emoji", `{"icon":"star"}`, "icon"},
		{"unknown colour", `{"icon":"i:star","icon_color":"magenta"}`, "icon_color"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := wantProblem(t, e.call("PATCH", "/api/v1/lists/"+lid, tt.body, etagOf("l", l)), 422, "validation_failed")
			if len(p.Errors) == 0 || p.Errors[0].Field != tt.field {
				t.Fatalf("errors = %+v, want field %s", p.Errors, tt.field)
			}
		})
	}
	wantProblem(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"icon":"🎬"}`, `"l-1"`), 412, "version_conflict")
}

func TestF012_ListIconInTreeListAndSearch(t *testing.T) {
	e := newTreeEnv(t)
	media := e.folder("Media", nil)
	watchlist := e.listOfKind("Watchlist", media["id"], "movie")
	wid := watchlist["id"].(string)
	rec := e.call("PATCH", "/api/v1/lists/"+wid, `{"icon":"i:film","icon_color":"indigo"}`, etagOf("l", watchlist))
	wantStatus(t, rec, 200)
	inbox := e.list("Inbox", nil)
	iid := inbox["id"].(string)
	rec = e.call("PATCH", "/api/v1/lists/"+iid, `{"icon":"📥"}`, etagOf("l", inbox))
	wantStatus(t, rec, 200)
	e.titled(wid, "The Matrix")

	// Tree: every list carries both fields, set or null.
	byID := map[string]obj{}
	for _, l := range e.tree("lists") {
		byID[l["id"].(string)] = l
	}
	if icon, color, ok := iconFields(byID[wid]); !ok || icon != "i:film" || color != "indigo" {
		t.Fatalf("tree watchlist: %v %v (present %v)", icon, color, ok)
	}
	if icon, color, ok := iconFields(byID[iid]); !ok || icon != "📥" || color != nil {
		t.Fatalf("tree inbox: %v %v (present %v)", icon, color, ok)
	}

	// Get list: the same fields, and the default is null (present), not absent.
	got := e.fetch("lists", wid)
	if icon, color, ok := iconFields(got); !ok || icon != "i:film" || color != "indigo" {
		t.Fatalf("get list: %v %v (present %v)", icon, color, ok)
	}
	plain := e.list("Plain", nil)
	if icon, color, ok := iconFields(plain); !ok || icon != nil || color != nil {
		t.Fatalf("new list: %v %v (present %v), want null", icon, color, ok)
	}

	// Search: the list segment carries its icon, folder segments are null.
	res := e.search(q("matrix"))
	if len(res.Data) != 1 || len(res.Data[0].Path) != 2 {
		t.Fatalf("search = %+v", res)
	}
	folderSeg, listSeg := res.Data[0].Path[0], res.Data[0].Path[1]
	if icon, color, ok := iconFields(folderSeg); !ok || icon != nil || color != nil {
		t.Fatalf("folder segment: %v %v (present %v), want null", icon, color, ok)
	}
	if icon, color, ok := iconFields(listSeg); !ok || listSeg["type"] != "list" || icon != "i:film" || color != "indigo" {
		t.Fatalf("list segment: %v", listSeg)
	}
}
