package content_test

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/content"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
)

// F-028 (D-162 decision 5): the content API reports who wrote each version. A lookup version reads
// "lookup"; a NULL origin (every user save, and version 0) reads "user".
func TestF028B2_OriginReadsOnCurrentContentAndVersions(t *testing.T) {
	adminURL := testdb.AdminURL(t)
	db := testdb.NewMigrated(t, adminURL)
	ws, list, item := uuid.New(), uuid.New(), uuid.New()
	ctx := scope.With(t.Context(), ws)
	for _, q := range []string{
		`INSERT INTO workspaces (id, name) VALUES ('` + ws.String() + `', 'W')`,
		`INSERT INTO lists (id, workspace_id, name, type, sort_order) VALUES ('` + list.String() + `', '` + ws.String() + `', 'L', 'movie', 0)`,
		`INSERT INTO items (id, workspace_id, list_id, title, sort_order) VALUES ('` + item.String() + `', '` + ws.String() + `', '` + list.String() + `', 'I', 0)`,
	} {
		if _, err := db.Exec(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, time.November, 16, 9, 0, 0, 0, time.UTC)
	sum := func(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
	if _, err := db.Exec(t.Context(),
		`INSERT INTO item_content_versions (item_id, version, body, size_bytes, sha256, created_at, origin) VALUES ($1, 1, 'Imported.', 9, $2, $3, 'lookup')`,
		item, sum("Imported."), at); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(),
		`INSERT INTO item_content_versions (item_id, version, body, size_bytes, sha256, created_at) VALUES ($1, 2, 'Mine.', 5, $2, $3)`,
		item, sum("Mine."), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	svc := content.NewService(content.NewPostgres(db), nil, clock.System{})

	cur, err := svc.Get(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Version != 2 || cur.Origin != content.OriginUser {
		t.Errorf("current = version %d origin %q, want version 2 by the user", cur.Version, cur.Origin)
	}
	versions, err := svc.Versions(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]string{}
	for _, v := range versions {
		got[v.Version] = v.Origin
	}
	if got[1] != content.OriginLookup || got[2] != content.OriginUser {
		t.Errorf("version origins = %v, want 1 lookup and 2 user", got)
	}
	first, err := svc.Version(ctx, item, 1)
	if err != nil || first.Origin != content.OriginLookup {
		t.Errorf("version 1 = %+v err %v, want origin lookup", first, err)
	}
}

// Version 0 (never saved) is the empty user content.
func TestF028B2_OriginOfVersionZeroIsUser(t *testing.T) {
	db := testdb.NewMigrated(t, testdb.AdminURL(t))
	ws, list, item := uuid.New(), uuid.New(), uuid.New()
	for _, q := range []string{
		`INSERT INTO workspaces (id, name) VALUES ('` + ws.String() + `', 'W')`,
		`INSERT INTO lists (id, workspace_id, name, type, sort_order) VALUES ('` + list.String() + `', '` + ws.String() + `', 'L', 'movie', 0)`,
		`INSERT INTO items (id, workspace_id, list_id, title, sort_order) VALUES ('` + item.String() + `', '` + ws.String() + `', '` + list.String() + `', 'I', 0)`,
	} {
		if _, err := db.Exec(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	svc := content.NewService(content.NewPostgres(db), nil, clock.System{})
	cur, err := svc.Get(scope.With(t.Context(), ws), item)
	if err != nil || cur.Version != 0 || cur.Origin != content.OriginUser {
		t.Fatalf("version 0 = %+v err %v, want origin user", cur, err)
	}
}
