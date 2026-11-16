package lookup

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// F-028 (D-162) on Postgres: the description write, its origin, its tracking columns and the history
// prune run through the real queries.

// dbVersion is one row of item_content_versions as the tests read it.
type dbVersion struct {
	version int
	origin  *string
	author  *uuid.UUID
	body    string
}

func dbVersions(t *testing.T, e *lookupEnv, itemID uuid.UUID) []dbVersion {
	t.Helper()
	rows, err := e.db.Query(t.Context(), `SELECT version, origin, author_user_id, body FROM item_content_versions WHERE item_id = $1 ORDER BY version`, itemID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []dbVersion
	for rows.Next() {
		var v dbVersion
		if err := rows.Scan(&v.version, &v.origin, &v.author, &v.body); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// tracking reads the description tracking columns and the import generation of an item's import row.
func tracking(t *testing.T, e *lookupEnv, itemID uuid.UUID) (version *int, sum []byte, generation int) {
	t.Helper()
	if err := e.db.QueryRow(t.Context(), `SELECT description_version, description_sha256, import_generation FROM lookup_imports WHERE item_id = $1`, itemID).Scan(&version, &sum, &generation); err != nil {
		t.Fatal(err)
	}
	return version, sum, generation
}

func TestF028B2_DB_CreateStoresTheLookupVersionAndTracksIt(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.November, 16, 9, 0, 0, 0, time.UTC))
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreate, ListID: e.list, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "matrix", CreateInput: &items.CreateInput{Title: "matrix"},
		Fields: map[string]any{
			"release_year": 1999, "genres": []any{"Science Fiction", "Action"},
			"tmdb_rating": 8.1, "tmdb_vote_count": 25000,
		},
		Synopsis: "A hacker learns the truth.", HistoryLimit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	versions := dbVersions(t, e, it.ID)
	if len(versions) != 1 || versions[0].version != 1 || versions[0].body != "A hacker learns the truth." {
		t.Fatalf("versions = %+v, want one version 1 with the summary", versions)
	}
	if versions[0].origin == nil || *versions[0].origin != "lookup" || versions[0].author != nil {
		t.Errorf("origin = %v author = %v, want lookup and no author", versions[0].origin, versions[0].author)
	}
	version, sum, generation := tracking(t, e, it.ID)
	want := sha256.Sum256([]byte("A hacker learns the truth."))
	if version == nil || *version != 1 || string(sum) != string(want[:]) || generation != currentImportGeneration {
		t.Errorf("tracking = %v %x generation %d, want version 1, the hash and generation %d", version, sum, generation, currentImportGeneration)
	}
	metadata := decodeMetadata(t, it.Metadata)
	if genres, _ := metadata["genres"].([]any); len(genres) != 2 || metadata["tmdb_rating"] != 8.1 || metadata["tmdb_vote_count"] != float64(25000) {
		t.Errorf("metadata = %#v, want the genres, rating and votes", metadata)
	}
}

// TestF028B2_DB_UserEditEndsTrackingAndRefreshNeverTouchesIt: a user save is a version with a NULL
// origin. The next scheduled refresh leaves it and clears the tracking.
func TestF028B2_DB_UserEditEndsTrackingAndRefreshNeverTouchesIt(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.November, 16, 9, 0, 0, 0, time.UTC))
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreate, ListID: e.list, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "matrix", CreateInput: &items.CreateInput{Title: "matrix"},
		Fields: map[string]any{"release_year": 1999}, Synopsis: "The imported summary.", HistoryLimit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	edit := "My own words."
	sum := sha256.Sum256([]byte(edit))
	if _, err := e.db.Exec(t.Context(),
		`INSERT INTO item_content_versions (item_id, version, body, size_bytes, sha256) VALUES ($1, 2, $2, $3, $4)`,
		it.ID, edit, len(edit), sum[:]); err != nil {
		t.Fatal(err)
	}

	if _, err := e.store.Apply(scope.With(t.Context(), e.ws), ApplyParams{
		Mode: ModeScheduled, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie",
		Provider: ProviderTMDB, SourceID: "603", Title: "The Matrix", OriginalTitle: "matrix",
		Fields: map[string]any{"release_year": 1999}, Synopsis: "A newer summary.", HistoryLimit: 20,
	}); err != nil {
		t.Fatal(err)
	}

	versions := dbVersions(t, e, it.ID)
	if len(versions) != 2 || versions[1].body != edit || versions[1].origin != nil {
		t.Fatalf("versions = %+v, want the user's edit to stay the last one", versions)
	}
	if version, sum, _ := tracking(t, e, it.ID); version != nil || sum != nil {
		t.Errorf("tracking = %v %x, want cleared", version, sum)
	}
}

// TestF028B2_DB_TrackedSummaryReplacedAndHistoryPruned: each changed re-match writes a lookup version,
// and the history keeps the current one and the limit before it.
func TestF028B2_DB_TrackedSummaryReplacedAndHistoryPruned(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.November, 16, 9, 0, 0, 0, time.UTC))
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreate, ListID: e.list, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "matrix", CreateInput: &items.CreateInput{Title: "matrix"},
		Fields: map[string]any{"release_year": 1999}, Synopsis: "One.", HistoryLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	cur := it
	for i, text := range []string{"Two.", "Three."} {
		cur, err = e.store.Apply(t.Context(), ApplyParams{
			Mode: ModeRematch, ItemID: cur.ID, ExpectedVersion: cur.Version, Type: "movie",
			Provider: ProviderTMDB, SourceID: "603", Title: "The Matrix", OriginalTitle: "matrix",
			Fields: map[string]any{"release_year": 1999}, Synopsis: text, HistoryLimit: 1,
		})
		if err != nil {
			t.Fatalf("rematch %d: %v", i, err)
		}
	}
	versions := dbVersions(t, e, it.ID)
	if len(versions) != 2 || versions[0].version != 2 || versions[1].version != 3 || versions[1].body != "Three." {
		t.Fatalf("versions = %+v, want [2 3] with the current text last", versions)
	}
	for _, v := range versions {
		if v.origin == nil || *v.origin != "lookup" {
			t.Errorf("version %d origin = %v, want lookup", v.version, v.origin)
		}
	}
	if version, _, _ := tracking(t, e, it.ID); version == nil || *version != 3 {
		t.Errorf("tracking = %v, want version 3", version)
	}
}

// TestF028B2_DB_UntrackedHardcoverCreateIsTheUsersText: the user's own text (origin NULL) by the caller,
// with no provenance row.
func TestF028B2_DB_UntrackedHardcoverCreateIsTheUsersText(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.November, 16, 9, 0, 0, 0, time.UTC))
	user, books := uuid.New(), uuid.New()
	if _, err := e.db.Exec(t.Context(), `INSERT INTO users (id, workspace_id, email) VALUES ($1, $2, 'alice@example.test')`, user, e.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(t.Context(), `INSERT INTO lists (id, workspace_id, name, type, sort_order) VALUES ($1, $2, 'Books', 'book', 0)`, books, e.ws); err != nil {
		t.Fatal(err)
	}
	ctx := scope.WithUser(t.Context(), user)
	it, err := e.store.Apply(ctx, ApplyParams{
		Mode: ModeCreateUntracked, ListID: books, Type: "book", CreateInput: &items.CreateInput{Title: "Dune"},
		Title: "Dune", OriginalTitle: "Dune", Fields: map[string]any{"author": "Frank Herbert"},
		Synopsis: "A desert planet.", HistoryLimit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	versions := dbVersions(t, e, it.ID)
	if len(versions) != 1 || versions[0].origin != nil || versions[0].author == nil || *versions[0].author != user {
		t.Fatalf("versions = %+v, want one user version by the caller", versions)
	}
	var rows int
	if err := e.db.QueryRow(t.Context(), `SELECT count(*) FROM lookup_imports WHERE item_id = $1`, it.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("provenance rows = %d, want none", rows)
	}
}
