package httpapi_test

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/httpapi"
)

// Authorization matrix (F-027 B6). This file classifies EVERY operation of the API and proves the
// identity rules over all of them; authz_resources_test.go and authz_members_test.go prove the
// ownership and sharing rules per class. A new operation without an entry in authzOps fails
// TestF027B6_EveryOperationIsClassified, so it cannot ship without an authorization decision.

type authzClass string

const (
	clsPublic     authzClass = "public probe"
	clsAuthed     authzClass = "authenticated only"
	clsPath       authzClass = "resource by path"
	clsCollection authzClass = "collection"
	clsShare      authzClass = "share"
)

// authzOp is the authorization class of one operation. body marks operations that also name a
// resource in their body (a parent, a destination, ids to reorder): they have a body case too.
type authzOp struct {
	class authzClass
	body  bool
}

var authzOps = map[string]authzOp{
	// Public probes: no identity at all.
	"checkHealth":    {class: clsPublic},
	"checkReadiness": {class: clsPublic},

	// Any authenticated user; the data is shared by the whole instance or the caller's own.
	"listTypes":             {class: clsAuthed},
	"listStoreProviders":    {class: clsAuthed},
	"getFavicon":            {class: clsAuthed},
	"listTmdbSuggestions":   {class: clsAuthed},
	"searchBookSuggestions": {class: clsAuthed},
	"getSettings":           {class: clsAuthed},
	"updateSettings":        {class: clsAuthed},
	"getMe":                 {class: clsAuthed},
	"listUsers":             {class: clsAuthed},

	// Collections: filtered to what the caller may see.
	"getTree":         {class: clsCollection},
	"searchItems":     {class: clsCollection},
	"listRecentItems": {class: clsCollection},
	"listTrash":       {class: clsCollection},
	"emptyTrash":      {class: clsCollection},

	// Resource by path: folders, lists, items, attachments, trash batches.
	"getFolder":              {class: clsPath},
	"updateFolder":           {class: clsPath},
	"deleteFolder":           {class: clsPath},
	"moveFolder":             {class: clsPath, body: true},
	"getList":                {class: clsPath},
	"updateList":             {class: clsPath},
	"deleteList":             {class: clsPath},
	"moveList":               {class: clsPath, body: true},
	"listItems":              {class: clsPath},
	"createItem":             {class: clsPath},
	"reorderListItems":       {class: clsPath, body: true},
	"refreshListLookups":     {class: clsPath},
	"getItem":                {class: clsPath},
	"updateItem":             {class: clsPath},
	"deleteItem":             {class: clsPath},
	"moveItem":               {class: clsPath, body: true},
	"getItemContent":         {class: clsPath},
	"updateItemContent":      {class: clsPath},
	"restoreContentVersion":  {class: clsPath},
	"listContentVersions":    {class: clsPath},
	"getContentVersion":      {class: clsPath},
	"setItemImage":           {class: clsPath},
	"deleteItemImage":        {class: clsPath},
	"lookupItemMetadata":     {class: clsPath},
	"getAttachmentFile":      {class: clsPath},
	"getAttachmentThumbnail": {class: clsPath},
	"purgeTrashBatch":        {class: clsPath},
	"restoreTrashBatch":      {class: clsPath, body: true},

	// Body-referenced collections: a parent or the ids to order come in the body.
	"createFolder":   {class: clsCollection, body: true},
	"createList":     {class: clsCollection, body: true},
	"reorderFolders": {class: clsCollection, body: true},
	"reorderLists":   {class: clsCollection, body: true},

	// Shares.
	"addFolderMember":    {class: clsShare},
	"removeFolderMember": {class: clsShare},
}

type specOp struct{ id, method, path string }

// specOps lists every operation registered in the Huma API.
func specOps() []specOp {
	var out []specOp
	for path, item := range httpapi.API(httpapi.Deps{}).OpenAPI().Paths {
		for method, op := range operationsOf(item) {
			out = append(out, specOp{op.OperationID, method, path})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func TestF027B6_EveryOperationIsClassified(t *testing.T) {
	seen := map[string]specOp{}
	for _, op := range specOps() {
		seen[op.id] = op
		c, ok := authzOps[op.id]
		if !ok {
			t.Errorf("%s %s (%s) has no authorization case: add it to authzOps and to the matching case table", op.method, op.path, op.id)
			continue
		}
		hasParam := strings.Contains(op.path, "{id}") || strings.Contains(op.path, "{batch_id}")
		switch {
		case c.class == clsPublic && strings.HasPrefix(op.path, "/api/v1/"):
			t.Errorf("%s: a public probe under /api/v1", op.id)
		case c.class != clsPublic && !strings.HasPrefix(op.path, "/api/v1/"):
			t.Errorf("%s: %s outside /api/v1 must be a public probe", op.id, op.path)
		case (c.class == clsPath || c.class == clsShare) != hasParam:
			t.Errorf("%s: class %q does not fit path %s", op.id, c.class, op.path)
		}
	}
	for id := range authzOps {
		if _, ok := seen[id]; !ok {
			t.Errorf("authzOps names %q, which is not an operation of the API", id)
		}
	}
	if len(seen) < 40 {
		t.Fatalf("only %d operations found: the walk is not seeing the API", len(seen))
	}
}

// fillPath turns an operation path into a concrete one with a random resource id.
func fillPath(path string) string { return substitute(path, uuid.NewString()) }

func apiRequest(op specOp, headers map[string]string) req {
	r := req{method: op.method, path: fillPath(op.path), headers: headers}
	if op.method != http.MethodGet && op.method != http.MethodDelete {
		r.body, r.contentType = `{}`, jsonCT
	}
	return r
}

// TestF027B6_NoValidIdentityIs401OnEveryAPIOperation: whatever a client sends without a verified
// identity, every /api/v1 operation (read, write, with or without a body) answers 401
// unauthenticated before anything else; only the probes stay open.
func TestF027B6_NoValidIdentityIs401OnEveryAPIOperation(t *testing.T) {
	dev := []string{"owner@dev.test", "alice@dev.test", "bob@dev.test"}
	routers := map[string]func() http.Handler{}
	build := func(name string, dev []string, lan string) {
		var h http.Handler
		routers[name] = func() http.Handler {
			if h == nil {
				h = identityRouter(t, dev, lan)
			}
			return h
		}
	}
	build("nothing", nil, "")
	build("dev and lan", dev, "owner@dev.test")
	build("lan only", nil, "owner@dev.test")

	forged := map[string]string{"Cf-Access-Authenticated-User-Email": "owner@dev.test"}
	for _, tc := range []struct {
		name, router string
		headers      map[string]string
	}{
		{"no headers", "nothing", nil},
		{"forged plain email header", "nothing", forged},
		{"dev header with dev identity off", "nothing", map[string]string{"X-Dev-User": "alice@dev.test"}},
		{"dev cookie with dev identity off", "nothing", map[string]string{"Cookie": "dev_user=alice@dev.test"}},
		{"garbage token, nothing else", "nothing", map[string]string{"Cf-Access-Jwt-Assertion": "garbage"}},
		{"garbage token, dev identity and LAN on", "dev and lan", map[string]string{"Cf-Access-Jwt-Assertion": "garbage"}},
		{"garbage token, dev header and forged email", "dev and lan", map[string]string{
			"Cf-Access-Jwt-Assertion": "a.b.c", "X-Dev-User": "alice@dev.test", "Cf-Access-Authenticated-User-Email": "alice@dev.test"}},
		{"empty token, dev identity and LAN on", "dev and lan", map[string]string{"Cf-Access-Jwt-Assertion": ""}},
		{"LAN owner behind Cloudflare (Cf-Ray), no token", "lan only", map[string]string{"Cf-Ray": "abc-MXP"}},
		{"LAN owner behind Cloudflare (Cf-Connecting-Ip), no token", "lan only", map[string]string{"Cf-Connecting-Ip": "203.0.113.9"}},
		{"LAN request with a bad token", "lan only", map[string]string{"Cf-Access-Jwt-Assertion": "a.b.c"}},
		{"LAN request with an empty token", "lan only", map[string]string{"Cf-Access-Jwt-Assertion": ""}},
		{"forged email behind Cloudflare, LAN on", "lan only", map[string]string{"Cf-Ray": "x", "Cf-Access-Authenticated-User-Email": "owner@dev.test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := routers[tc.router]()
			checked := 0
			for _, op := range specOps() {
				rec := do(t, h, apiRequest(op, tc.headers))
				if authzOps[op.id].class == clsPublic {
					if rec.Code != http.StatusOK {
						t.Errorf("%s: probe answered %d", op.id, rec.Code)
					}
					continue
				}
				if rec.Code != http.StatusUnauthorized {
					t.Errorf("%s %s: status %d, want 401: %s", op.method, op.path, rec.Code, rec.Body)
					continue
				}
				wantProblem(t, rec, http.StatusUnauthorized, "unauthenticated")
				checked++
			}
			if checked < 40 {
				t.Fatalf("only %d operations checked", checked)
			}
		})
	}
}

func meOf(t *testing.T, h http.Handler, headers map[string]string) (int, meBody) {
	t.Helper()
	rec := do(t, h, req{method: "GET", path: "/api/v1/me", headers: headers})
	var me meBody
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, me
}

// TestF027B6_ForgedEmailHeaderNeverActsAsThatUser: Cf-Access-Authenticated-User-Email is never
// read. Without any other identity source it is a 401; with one, the request acts as that
// source's user, never as the forged address.
func TestF027B6_ForgedEmailHeaderNeverActsAsThatUser(t *testing.T) {
	dev := []string{"owner@dev.test", "alice@dev.test", "bob@dev.test"}
	devRouter := identityRouter(t, dev, "")
	lanRouter := identityRouter(t, nil, "lan-owner@dev.test")
	offRouter := identityRouter(t, nil, "")

	for _, tc := range []struct {
		name    string
		h       http.Handler
		headers map[string]string
		want    string // "" = 401
	}{
		{"nothing configured", offRouter, map[string]string{"Cf-Access-Authenticated-User-Email": "owner@dev.test"}, ""},
		{"dev identity on, forged known dev user", devRouter, map[string]string{"Cf-Access-Authenticated-User-Email": "bob@dev.test"}, "owner@dev.test"},
		{"dev identity on, forged stranger", devRouter, map[string]string{"Cf-Access-Authenticated-User-Email": "mallory@evil.test"}, "owner@dev.test"},
		{"dev identity on, dev header wins", devRouter, map[string]string{"X-Dev-User": "alice@dev.test", "Cf-Access-Authenticated-User-Email": "bob@dev.test"}, "alice@dev.test"},
		{"LAN owner, forged email", lanRouter, map[string]string{"Cf-Access-Authenticated-User-Email": "owner@dev.test"}, "lan-owner@dev.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, me := meOf(t, tc.h, tc.headers)
			if tc.want == "" {
				if code != http.StatusUnauthorized {
					t.Fatalf("status %d, want 401", code)
				}
				return
			}
			if code != http.StatusOK || me.Email != tc.want {
				t.Fatalf("acted as %q (status %d), want %q", me.Email, code, tc.want)
			}
		})
	}
}

// TestF027B6_LANOwner (D-150): with OWNER_EMAIL set and DEV_IDENTITY off, a request with no
// token acts as the owner, always, unless it carries a Cloudflare edge header (then 401); a
// present token is verified and never falls back; with no OWNER_EMAIL it is 401.
func TestF027B6_LANOwner(t *testing.T) {
	on := identityRouter(t, nil, "owner@lan.test")
	off := identityRouter(t, nil, "")

	for _, tc := range []struct {
		name    string
		h       http.Handler
		headers map[string]string
		want    string // "" = 401
	}{
		{"no headers acts as the owner", on, nil, "owner@lan.test"},
		{"unrelated headers", on, map[string]string{"X-Forwarded-For": "192.168.1.20", "User-Agent": "x"}, "owner@lan.test"},
		{"forged Cf-Ray", on, map[string]string{"Cf-Ray": "abc-MXP"}, ""},
		{"forged Cf-Connecting-Ip", on, map[string]string{"Cf-Connecting-Ip": "203.0.113.9"}, ""},
		{"both edge headers (tunnel, no token)", on, map[string]string{"Cf-Ray": "abc", "Cf-Connecting-Ip": "203.0.113.9"}, ""},
		{"bad token", on, map[string]string{"Cf-Access-Jwt-Assertion": "garbage"}, ""},
		{"X-Dev-User is ignored without dev identity", on, map[string]string{"X-Dev-User": "alice@dev.test"}, "owner@lan.test"},
		{"no OWNER_EMAIL, no headers", off, nil, ""},
		{"no OWNER_EMAIL, Cf-Ray", off, map[string]string{"Cf-Ray": "abc"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, me := meOf(t, tc.h, tc.headers)
			if tc.want == "" {
				if code != http.StatusUnauthorized {
					t.Fatalf("status %d, want 401", code)
				}
				return
			}
			if code != http.StatusOK || me.Email != tc.want || me.IdentitySource != "lan" {
				t.Fatalf("acted as %q via %q (status %d), want %q via lan", me.Email, me.IdentitySource, code, tc.want)
			}
		})
	}
}

// TestF027B6_LANOwnerHasOwnerRights (D-150): the LAN user is the owner account, so owner-only
// rules pass: share and unshare a top-level folder, then delete it.
func TestF027B6_LANOwnerHasOwnerRights(t *testing.T) {
	h, ensure := identityRouterDB(t, nil, "owner@lan.test")
	alice := ensure("alice@lan.test")
	rec := do(t, h, req{method: "POST", path: "/api/v1/folders", body: `{"name":"Home"}`, contentType: jsonCT})
	wantStatus(t, rec, http.StatusCreated)
	f := decode(t, rec)
	id := f["id"].(string)

	rec = do(t, h, req{method: "POST", path: "/api/v1/folders/" + id + "/members", body: `{"email":"alice@lan.test"}`, contentType: jsonCT})
	wantStatus(t, rec, http.StatusOK)
	rec = do(t, h, req{method: "DELETE", path: "/api/v1/folders/" + id + "/members/" + alice.ID.String()})
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("unshare: status %d: %s", rec.Code, rec.Body)
	}
	got := do(t, h, req{method: "GET", path: "/api/v1/folders/" + id})
	wantStatus(t, got, http.StatusOK)
	rec = do(t, h, req{method: "DELETE", path: "/api/v1/folders/" + id, headers: map[string]string{"If-Match": got.Header().Get("ETag")}})
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("delete top-level folder: status %d: %s", rec.Code, rec.Body)
	}
}

// The production-refuses-DEV_IDENTITY rule is proved in internal/platform/config (config_test.go,
// "production refuses dev identity"); the dev identity itself in TestMe_DevIdentity.
