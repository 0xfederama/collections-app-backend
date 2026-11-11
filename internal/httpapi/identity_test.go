package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/httpapi"
	"github.com/0xfederama/collections-app-backend/internal/identity"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
	"github.com/0xfederama/collections-app-backend/internal/storage"
	"github.com/0xfederama/collections-app-backend/internal/trash"
	"github.com/0xfederama/collections-app-backend/internal/users"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

type meBody struct {
	ID             string    `json:"id"`
	Email          string    `json:"email"`
	Name           *string   `json:"name"`
	AvatarURL      *string   `json:"avatar_url"`
	DevIdentities  *[]string `json:"dev_identities"`
	IdentitySource string    `json:"identity_source"`
}

// identityRouter is a router with the real identity middleware over a migrated database.
// owner is OWNER_EMAIL: the identity of LAN requests (D-150); "" = none.
func identityRouter(t *testing.T, dev []string, owner string) http.Handler {
	t.Helper()
	h, _ := identityRouterDB(t, dev, owner)
	return h
}

// identityRouterDB is identityRouter that also returns a function creating a user of its workspace.
func identityRouterDB(t *testing.T, dev []string, owner string) (http.Handler, func(email string) users.User) {
	t.Helper()
	pool := testdb.NewMigrated(t, sharedPostgres(t))
	ws, err := workspace.Bootstrap(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	svc := users.NewService(users.NewPostgres(pool), clock.System{})
	dir := t.TempDir()
	store, err := storage.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return newRouter(t, httpapi.Deps{
			DB: pool, DataDir: dir, Logger: quietLogger(), WorkspaceID: ws,
			Trash:    trash.NewService(trash.NewPostgres(pool), store, clock.System{}, quietLogger()),
			Settings: workspace.NewService(workspace.NewPostgres(pool)),
			Identity: identity.New(identity.Options{Users: svc, Logger: quietLogger(), DevIdentities: dev, OwnerEmail: owner}),
			Users:    svc, DevIdentities: dev,
		}), func(email string) users.User {
			u, err := svc.EnsureByEmail(scope.With(t.Context(), ws), email)
			if err != nil {
				t.Fatal(err)
			}
			return u
		}
}

func TestMe_DevIdentity(t *testing.T) {
	dev := []string{"owner@dev.test", "alice@dev.test", "bob@dev.test"}
	h := identityRouter(t, dev, "")

	rec := do(t, h, req{method: "GET", path: "/api/v1/me"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var me meBody
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me.Email != "owner@dev.test" || me.ID == "" || me.Name != nil || me.AvatarURL != nil {
		t.Fatalf("me = %+v", me)
	}
	if me.IdentitySource != "dev" {
		t.Fatalf("identity_source = %q, want dev", me.IdentitySource)
	}
	if me.DevIdentities == nil || len(*me.DevIdentities) != 3 {
		t.Fatalf("dev_identities = %v", me.DevIdentities)
	}
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	if v, ok := raw["name"]; !ok || v != nil {
		t.Errorf("name must be present and null: %v", raw)
	}

	// Another user gets another id; the same user keeps theirs.
	alice := do(t, h, req{method: "GET", path: "/api/v1/me", headers: map[string]string{"X-Dev-User": "alice@dev.test"}})
	var a meBody
	_ = json.Unmarshal(alice.Body.Bytes(), &a)
	if a.Email != "alice@dev.test" || a.ID == me.ID {
		t.Fatalf("alice = %+v", a)
	}
	again := do(t, h, req{method: "GET", path: "/api/v1/me"})
	var me2 meBody
	_ = json.Unmarshal(again.Body.Bytes(), &me2)
	if me2.ID != me.ID {
		t.Fatalf("owner id changed: %s -> %s", me.ID, me2.ID)
	}

	if rec := do(t, h, req{method: "GET", path: "/api/v1/me", headers: map[string]string{"X-Dev-User": "mallory@example.test"}}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown dev user: status %d", rec.Code)
	}
}

func TestMe_DevIdentitiesNullWhenOff(t *testing.T) {
	h := identityRouter(t, nil, "owner@example.test")
	rec := do(t, h, req{method: "GET", path: "/api/v1/me"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	if v, ok := raw["dev_identities"]; !ok || v != nil {
		t.Fatalf("dev_identities must be present and null: %v", raw)
	}
	if raw["email"] != "owner@example.test" || raw["identity_source"] != "lan" {
		t.Fatalf("email = %v, identity_source = %v", raw["email"], raw["identity_source"])
	}
}

func TestUnauthenticated_401ProblemOnEveryAPIPathButNotProbes(t *testing.T) {
	h := identityRouter(t, nil, "")
	for _, p := range []string{"/api/v1/me", "/api/v1/settings", "/api/v1/tree", "/api/v1/unknown"} {
		rec := do(t, h, req{method: "GET", path: p})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d", p, rec.Code)
		}
		var pb problemBody
		_ = json.Unmarshal(rec.Body.Bytes(), &pb)
		if pb.Code != "unauthenticated" || pb.RequestID != "" && rec.Header().Get("X-Request-Id") != pb.RequestID {
			t.Errorf("%s: problem = %+v", p, pb)
		}
		if rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s: content type %q", p, rec.Header().Get("Content-Type"))
		}
	}
	// A write is refused before its If-Match / body checks.
	rec := do(t, h, req{method: "PATCH", path: "/api/v1/settings", body: `{}`, contentType: jsonCT})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("PATCH without identity: status %d", rec.Code)
	}
	for _, p := range []string{"/healthz", "/readyz"} {
		if rec := do(t, h, req{method: "GET", path: p}); rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, probes need no identity", p, rec.Code)
		}
	}
}

func TestNewRouterRefusesToBuildWithoutIdentity(t *testing.T) {
	if _, err := httpapi.NewRouter(httpapi.Deps{DB: fakePinger{}, Logger: quietLogger()}); err == nil {
		t.Fatal("NewRouter accepted Deps without Identity")
	}
}
