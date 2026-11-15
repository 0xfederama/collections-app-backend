package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// Move at a position (D-158): an optional before_id on the folder and list move. It names the
// sibling to insert in front of: a child folder of the target, or a list of the target folder (or of
// the top level). Absent or null appends last. Everything else about a move is unchanged.

// moveTo moves o (a folder: "folders", a list: "lists") with its current ETag. field is the target
// key of the body (parent_folder_id or folder_id). before_id is sent only when one is given, so a
// nil given explicitly is a JSON null.
func (e *treeEnv) moveTo(typ string, o obj, field string, to any, before ...any) *httptest.ResponseRecorder {
	e.t.Helper()
	body := obj{field: to}
	if len(before) > 0 {
		body["before_id"] = before[0]
	}
	raw, err := json.Marshal(body)
	if err != nil {
		e.t.Fatal(err)
	}
	prefix := "f"
	if typ == "lists" {
		prefix = "l"
	}
	return e.call("POST", "/api/v1/"+typ+"/"+o["id"].(string)+"/move", string(raw), etagOf(prefix, o))
}

// mustTrash moves o to the trash (DELETE with its current ETag).
func (e *treeEnv) mustTrash(typ, prefix string, o obj) {
	e.t.Helper()
	wantStatus(e.t, e.call("DELETE", "/api/v1/"+typ+"/"+o["id"].(string), "", etagOf(prefix, o)), 204)
}

// childrenOf returns the entries of typ whose parent is parent (nil = top level), in tree order.
func (e *treeEnv) childrenOf(typ, field string, parent any) []obj {
	e.t.Helper()
	var out []obj
	for _, o := range e.tree(typ) {
		if o[field] == parent {
			out = append(out, o)
		}
	}
	return out
}

// childNames is childrenOf as names.
func (e *treeEnv) childNames(typ, field string, parent any) []string {
	e.t.Helper()
	var out []string
	for _, o := range e.childrenOf(typ, field, parent) {
		out = append(out, o["name"].(string))
	}
	return out
}

func TestMoveBeforeID_FolderLandsInFrontOfTheSiblingOrLast(t *testing.T) {
	e := newTreeEnv(t)
	p := e.folder("P", nil)
	a := e.folder("A", p["id"])
	b := e.folder("B", p["id"])
	c := e.folder("C", p["id"])
	x := e.folder("X", nil)
	pid := p["id"]

	// move moves x (the object as of the previous step) and checks P's children, one bump per move.
	move := func(name string, to any, want []string, before ...any) {
		t.Run(name, func(t *testing.T) {
			v := x["version"].(float64)
			rec := e.moveTo("folders", x, "parent_folder_id", to, before...)
			wantStatus(t, rec, 200)
			x = decode(t, rec)
			if x["version"].(float64) != v+1 {
				t.Fatalf("version = %v, want %v: one move is one bump", x["version"], v+1)
			}
			if got := e.childNames("folders", "parent_folder_id", pid); !slices.Equal(got, want) {
				t.Fatalf("children of P = %v, want %v", got, want)
			}
		})
	}

	move("from the root, in front of the first child", pid, []string{"X", "A", "B", "C"}, a["id"])
	move("same parent, in front of the middle child", pid, []string{"A", "X", "B", "C"}, b["id"])
	move("same parent, in front of the last child", pid, []string{"A", "B", "X", "C"}, c["id"])
	move("null before_id appends last", pid, []string{"A", "B", "C", "X"}, nil)
	move("absent before_id appends last", pid, []string{"A", "B", "C", "X"})
	move("absent before_id from a folder to the root", nil, []string{"A", "B", "C"})
	if got := e.childNames("folders", "parent_folder_id", nil); !slices.Contains(got, "X") {
		t.Fatalf("X left the root without landing there: %v", got)
	}
	move("back into P, appended", pid, []string{"A", "B", "C", "X"})
}

func TestMoveBeforeID_InvalidSiblingIs422AndChangesNothing(t *testing.T) {
	e := newAccountsEnv(t) // the accounts env wires the trash service, which the trashed sibling needs
	p := e.folder("P", nil)
	q := e.folder("Q", nil)
	inQ := e.folder("In Q", q["id"])
	gone := e.folder("Gone", p["id"])
	e.mustTrash("folders", "f", gone)
	list := e.list("L", p["id"])
	x := e.folder("X", p["id"])
	inX := e.folder("Inside X", x["id"])

	cases := []struct {
		name   string
		before string
	}{
		{"unknown id", uuid.NewString()},
		{"trashed sibling", gone["id"].(string)},
		{"a list of the target (other kind)", list["id"].(string)},
		{"a folder of another parent", inQ["id"].(string)},
		{"a folder inside the moved one (not a sibling)", inX["id"].(string)},
		{"the moved folder itself", x["id"].(string)},
		{"the target itself", p["id"].(string)},
	}
	before := e.tree("folders")
	beforeLists := e.tree("lists")
	version := x["version"]
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.moveTo("folders", x, "parent_folder_id", p["id"], tt.before)
			wantProblem(t, rec, 422, "invalid_before_id")
		})
	}
	bb, _ := json.Marshal(before)
	if ab, _ := json.Marshal(e.tree("folders")); string(bb) != string(ab) {
		t.Fatalf("folders changed after rejected moves:\nbefore %s\nafter  %s", bb, ab)
	}
	bl, _ := json.Marshal(beforeLists)
	if al, _ := json.Marshal(e.tree("lists")); string(bl) != string(al) {
		t.Fatalf("lists changed after rejected moves:\nbefore %s\nafter  %s", bl, al)
	}
	if got := e.fetch("folders", x["id"].(string)); got["version"] != version {
		t.Fatalf("version of X = %v after rejected moves, want %v", got["version"], version)
	}
}

func TestMoveBeforeID_ListsLandInFrontOfTheSiblingOrLast(t *testing.T) {
	e := newTreeEnv(t)
	p := e.folder("P", nil)
	l1 := e.list("L1", p["id"])
	l2 := e.list("L2", p["id"])
	l3 := e.list("L3", p["id"])
	x := e.list("X", nil)

	move := func(name string, to any, want []string, before ...any) {
		t.Run(name, func(t *testing.T) {
			v := x["version"].(float64)
			rec := e.moveTo("lists", x, "folder_id", to, before...)
			wantStatus(t, rec, 200)
			x = decode(t, rec)
			if x["version"].(float64) != v+1 {
				t.Fatalf("version = %v, want %v", x["version"], v+1)
			}
			if got := e.childNames("lists", "folder_id", p["id"]); !slices.Equal(got, want) {
				t.Fatalf("lists of P = %v, want %v", got, want)
			}
		})
	}
	move("from the top level, in front of the middle list", p["id"], []string{"L1", "X", "L2", "L3"}, l2["id"])
	move("same folder, in front of the first list", p["id"], []string{"X", "L1", "L2", "L3"}, l1["id"])
	move("same folder, null appends last", p["id"], []string{"L1", "L2", "L3", "X"}, nil)
	move("same folder, in front of the last list", p["id"], []string{"L1", "L2", "X", "L3"}, l3["id"])
	move("absent appends last", p["id"], []string{"L1", "L2", "L3", "X"})

	// A folder id is another kind: 422, and nothing moves.
	before := e.tree("lists")
	wantProblem(t, e.moveTo("lists", x, "folder_id", p["id"], p["id"]), 422, "invalid_before_id")
	wantProblem(t, e.moveTo("lists", x, "folder_id", p["id"], x["id"]), 422, "invalid_before_id")
	if after := e.tree("lists"); fmt.Sprint(names(before)) != fmt.Sprint(names(after)) {
		t.Fatalf("lists changed after a rejected move: %v -> %v", names(before), names(after))
	}
}

func TestMoveBeforeID_TopLevelOrderIsTheCallersOwn(t *testing.T) {
	e := newAccountsEnv(t)
	a1 := e.folder("A1", nil)
	b1 := e.folder("B1", nil)
	c1 := e.folder("C1", nil)
	for _, f := range []obj{a1, b1, c1} {
		e.mustShare(f, "b")
	}
	b := e.as("b")
	// B orders the shared folders C, A, B for themselves.
	wantStatus(t, b.call("PUT", "/api/v1/folders/order", fmt.Sprintf(`{"parent_folder_id":null,"ids":[%q,%q,%q]}`,
		str(c1, "id"), str(a1, "id"), str(b1, "id")), ""), 204)
	bBefore := b.childNames("folders", "parent_folder_id", nil)

	x := e.folder("X", nil) // a's private top-level folder: a's order is A1, B1, C1, X (shared order)
	rec := e.moveTo("folders", x, "parent_folder_id", nil, b1["id"])
	wantStatus(t, rec, 200)
	if got := e.childNames("folders", "parent_folder_id", nil); !slices.Equal(got, []string{"A1", "X", "B1", "C1"}) {
		t.Fatalf("a's top level = %v, want [A1 X B1 C1]", got)
	}
	if got := b.childNames("folders", "parent_folder_id", nil); !slices.Equal(got, bBefore) {
		t.Fatalf("b's own order changed: %v, was %v", got, bBefore)
	}
	if n := e.count(`SELECT count(*) FROM user_top_order WHERE user_id = $1`, testUsers["b"]); n != 3 {
		t.Fatalf("b has %d order rows, want the 3 they set", n)
	}

	// A nested folder of a's shared box moves to the top level in front of C1: a owns it now.
	n := e.folder("N", a1["id"])
	rec = e.moveTo("folders", n, "parent_folder_id", nil, c1["id"])
	wantStatus(t, rec, 200)
	if got := e.childNames("folders", "parent_folder_id", nil); !slices.Equal(got, []string{"A1", "X", "B1", "N", "C1"}) {
		t.Fatalf("a's top level after the nested move = %v", got)
	}
	if moved := decode(t, rec); moved["owner_user_id"] != testUsers["a"].String() {
		t.Fatalf("owner after moving to the top = %v, want a", moved["owner_user_id"])
	}
	if got := b.childNames("folders", "parent_folder_id", nil); !slices.Equal(got, bBefore) {
		t.Fatalf("b's own order changed by a's move: %v", got)
	}
}

func TestMoveBeforeID_TopLevelSiblingMustBeVisibleAtTheTop(t *testing.T) {
	e := newAccountsEnv(t)
	a1 := e.folder("A1", nil)
	inA1 := e.folder("Inside A1", a1["id"])
	gone := e.folder("Gone", nil)
	e.mustTrash("folders", "f", gone)
	bPrivate := e.as("b").folder("B private", nil)
	x := e.folder("X", nil)

	for _, tt := range []struct {
		name, before string
	}{
		{"a folder nobody shared with a (b's private)", str(bPrivate, "id")},
		{"a nested folder (not at the top level)", str(inA1, "id")},
		{"a trashed top-level folder", str(gone, "id")},
		{"the moved folder itself", str(x, "id")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wantProblem(t, e.moveTo("folders", x, "parent_folder_id", nil, tt.before), 422, "invalid_before_id")
		})
	}
	if got := e.fetch("folders", str(x, "id")); got["version"] != x["version"] {
		t.Fatalf("X moved by a rejected request: %v", got)
	}
}

func TestMoveBeforeID_OwnershipAnswersBeforeThePosition(t *testing.T) {
	e := newAccountsEnv(t)
	a1 := e.folder("A1", nil)
	b1 := e.folder("B1", nil)
	e.mustShare(a1, "b")
	b := e.as("b")

	// A member does not move a top-level folder: 403, whatever the position says.
	wantProblem(t, b.moveTo("folders", a1, "parent_folder_id", nil, b1["id"]), 403, "forbidden")
	wantProblem(t, b.moveTo("folders", a1, "parent_folder_id", nil, uuid.NewString()), 403, "forbidden")
}

func TestMoveBeforeID_MemberReordersInsideASharedFolder(t *testing.T) {
	e := newAccountsEnv(t)
	s := e.folder("Shared", nil)
	n1 := e.folder("n1", s["id"])
	n2 := e.folder("n2", s["id"])
	n3 := e.folder("n3", s["id"])
	e.mustShare(s, "b")
	b := e.as("b")
	outside := e.as("b").folder("b's own", nil)

	wantStatus(t, b.moveTo("folders", n1, "parent_folder_id", s["id"], n3["id"]), 200)
	if got := e.childNames("folders", "parent_folder_id", s["id"]); !slices.Equal(got, []string{"n2", "n1", "n3"}) {
		t.Fatalf("shared folder children = %v, want [n2 n1 n3]", got)
	}
	// A sibling outside the shared folder is not one.
	wantProblem(t, b.moveTo("folders", n2, "parent_folder_id", s["id"], str(outside, "id")), 422, "invalid_before_id")
}

func TestMoveBeforeID_ConcurrentMovesIntoOneFolderKeepAPermutation(t *testing.T) {
	e := newTreeEnv(t)
	p := e.folder("P", nil)
	first := e.folder("First", p["id"])
	const n = 8
	movers := make([]obj, 0, n)
	for i := range n {
		movers = append(movers, e.folder(fmt.Sprintf("X%d", i), nil))
	}

	codes := make([]int, n)
	var wg sync.WaitGroup
	for i, x := range movers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = e.moveTo("folders", x, "parent_folder_id", p["id"], first["id"]).Code
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Fatalf("move %d answered %d, want 200", i, c)
		}
	}

	kids := e.childrenOf("folders", "parent_folder_id", p["id"])
	if len(kids) != n+1 {
		t.Fatalf("P has %d children, want %d", len(kids), n+1)
	}
	seen := map[string]bool{}
	for i, k := range kids {
		if k["sort_order"].(float64) != float64(i) {
			t.Errorf("child %v has sort_order %v, want %d", k["name"], k["sort_order"], i)
		}
		seen[k["name"].(string)] = true
	}
	if len(seen) != n+1 || !seen["First"] {
		t.Fatalf("children are not a permutation of the movers and First: %v", names(kids))
	}
	if last := kids[len(kids)-1]["name"]; last != "First" {
		t.Fatalf("last child = %v, want First (every mover went in front of it)", last)
	}
}

// Absent or null before_id at the top level with access is the same as "last" in the caller's own
// order: the caller's top-level entries are rewritten, so an entry that already has a caller order
// row moves too (a shared sort_order alone would leave it where the caller had it).
func TestMoveBeforeID_TopLevelWithoutBeforeLandsLastInTheCallersOwnOrder(t *testing.T) {
	e := newAccountsEnv(t)

	t.Run("folders", func(t *testing.T) {
		a1 := e.folder("A1", nil)
		b1 := e.folder("B1", nil)
		c1 := e.folder("C1", nil)
		x := e.folder("X", nil)
		wantStatus(t, e.call("PUT", "/api/v1/folders/order", fmt.Sprintf(`{"parent_folder_id":null,"ids":[%q,%q,%q,%q]}`,
			str(a1, "id"), str(b1, "id"), str(c1, "id"), str(x, "id")), ""), 204)

		// A1 has a caller row at 0: moving it to the top without before must still put it last.
		wantStatus(t, e.moveTo("folders", a1, "parent_folder_id", nil), 200)
		if got := e.childNames("folders", "parent_folder_id", nil); !slices.Equal(got, []string{"B1", "C1", "X", "A1"}) {
			t.Fatalf("top level after absent before_id = %v, want [B1 C1 X A1]", got)
		}
		wantStatus(t, e.moveTo("folders", b1, "parent_folder_id", nil, nil), 200)
		if got := e.childNames("folders", "parent_folder_id", nil); !slices.Equal(got, []string{"C1", "X", "A1", "B1"}) {
			t.Fatalf("top level after null before_id = %v, want [C1 X A1 B1]", got)
		}

		// A nested folder moved to the top without before is last too.
		nested := e.folder("N", c1["id"])
		wantStatus(t, e.moveTo("folders", nested, "parent_folder_id", nil), 200)
		if got := e.childNames("folders", "parent_folder_id", nil); !slices.Equal(got, []string{"C1", "X", "A1", "B1", "N"}) {
			t.Fatalf("top level after a nested folder moved up = %v", got)
		}
		if got := e.count(`SELECT count(*) FROM user_top_order WHERE user_id = $1 AND entry_type = 'folder'`, testUsers["a"]); got != 5 {
			t.Fatalf("a has %d folder order rows, want the 5 top-level folders", got)
		}
	})

	t.Run("lists", func(t *testing.T) {
		l1 := e.list("L1", nil)
		l2 := e.list("L2", nil)
		l3 := e.list("L3", nil)
		lx := e.list("LX", nil)
		f := e.folder("F", nil)
		inF := e.list("In F", f["id"])
		wantStatus(t, e.call("PUT", "/api/v1/lists/order", fmt.Sprintf(`{"folder_id":null,"ids":[%q,%q,%q,%q]}`,
			str(l1, "id"), str(l2, "id"), str(l3, "id"), str(lx, "id")), ""), 204)

		wantStatus(t, e.moveTo("lists", l1, "folder_id", nil), 200)
		if got := e.childNames("lists", "folder_id", nil); !slices.Equal(got, []string{"L2", "L3", "LX", "L1"}) {
			t.Fatalf("top-level lists after absent before_id = %v, want [L2 L3 LX L1]", got)
		}
		wantStatus(t, e.moveTo("lists", l2, "folder_id", nil, nil), 200)
		if got := e.childNames("lists", "folder_id", nil); !slices.Equal(got, []string{"L3", "LX", "L1", "L2"}) {
			t.Fatalf("top-level lists after null before_id = %v, want [L3 LX L1 L2]", got)
		}

		wantStatus(t, e.moveTo("lists", inF, "folder_id", nil), 200)
		if got := e.childNames("lists", "folder_id", nil); !slices.Equal(got, []string{"L3", "LX", "L1", "L2", "In F"}) {
			t.Fatalf("top-level lists after a nested list moved up = %v", got)
		}
	})
}

// concurrentTopMoves moves n entries of one kind at once within the top level of a (they are created
// there first): half in front of First, a quarter with before_id absent, a quarter with it null.
// Every move must answer 200, and a's caller order must stay a permutation: the top-level entries
// have distinct positions 0..n, none lost or doubled.
func concurrentTopMoves(t *testing.T, e *imageEnv, typ, field string, create func(string, any) obj) {
	t.Helper()
	first := create("First", nil)
	const n = 8
	movers := make([]obj, 0, n)
	for i := range n {
		movers = append(movers, create(fmt.Sprintf("X%d", i), nil))
	}
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i, x := range movers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch {
			case i%2 == 0:
				codes[i] = e.moveTo(typ, x, field, nil, first["id"]).Code
			case i%4 == 1:
				codes[i] = e.moveTo(typ, x, field, nil).Code
			default:
				codes[i] = e.moveTo(typ, x, field, nil, nil).Code
			}
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Fatalf("move %d answered %d, want 200", i, c)
		}
	}

	kids := e.childrenOf(typ, field, nil)
	if len(kids) != n+1 {
		t.Fatalf("top level has %d entries, want %d", len(kids), n+1)
	}
	seen := map[string]bool{}
	for _, k := range kids {
		seen[k["name"].(string)] = true
	}
	if len(seen) != n+1 || !seen["First"] {
		t.Fatalf("top level is not a permutation of the movers and First: %v", names(kids))
	}

	kind := map[string]string{"folders": "folder", "lists": "list"}[typ]
	uid := testUsers["a"]
	if got := e.count(`SELECT count(*) FROM user_top_order WHERE user_id = $1 AND entry_type = $2`, uid, kind); got != n+1 {
		t.Fatalf("a has %d %s order rows, want %d", got, kind, n+1)
	}
	if got := e.count(`SELECT count(DISTINCT sort_order) FROM user_top_order WHERE user_id = $1 AND entry_type = $2
		AND sort_order BETWEEN 0 AND $3`, uid, kind, n); got != n+1 {
		t.Fatalf("a's %s positions are not 0..%d, each used once", kind, n)
	}
}

func TestMoveBeforeID_ConcurrentMovesIntoANestedListFolderKeepAPermutation(t *testing.T) {
	e := newTreeEnv(t)
	p := e.folder("P", nil)
	q := e.folder("Q", p["id"]) // nested: the target is inside another folder
	first := e.list("First", q["id"])
	const n = 8
	movers := make([]obj, 0, n)
	for i := range n {
		movers = append(movers, e.list(fmt.Sprintf("L%d", i), nil))
	}

	codes := make([]int, n)
	var wg sync.WaitGroup
	for i, x := range movers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = e.moveTo("lists", x, "folder_id", q["id"], first["id"]).Code
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Fatalf("move %d answered %d, want 200", i, c)
		}
	}

	kids := e.childrenOf("lists", "folder_id", q["id"])
	if len(kids) != n+1 {
		t.Fatalf("Q has %d lists, want %d", len(kids), n+1)
	}
	seen := map[string]bool{}
	for i, k := range kids {
		if k["sort_order"].(float64) != float64(i) {
			t.Errorf("list %v has sort_order %v, want %d", k["name"], k["sort_order"], i)
		}
		seen[k["name"].(string)] = true
	}
	if len(seen) != n+1 || !seen["First"] {
		t.Fatalf("lists are not a permutation of the movers and First: %v", names(kids))
	}
	if last := kids[len(kids)-1]["name"]; last != "First" {
		t.Fatalf("last list = %v, want First (every mover went in front of it)", last)
	}
}

func TestMoveBeforeID_ConcurrentTopLevelMovesKeepACallerPermutation(t *testing.T) {
	t.Run("folders", func(t *testing.T) {
		e := newAccountsEnv(t)
		concurrentTopMoves(t, e, "folders", "parent_folder_id", e.folder)
	})
	t.Run("lists", func(t *testing.T) {
		e := newAccountsEnv(t)
		concurrentTopMoves(t, e, "lists", "folder_id", e.list)
	})
}
