package lookup

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// F-028 (D-162 decisions 1 to 3): the provider's summary becomes the item's description. These tests
// run the apply rules on the fake repository; the Postgres ones are in description_db_test.go.

func TestF028B2_NormalizeSynopsis(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"empty":                  {"", ""},
		"whitespace only":        {" \n\t ", ""},
		"collapses whitespace":   {"  A  plot.\n\nIn   two lines. ", "A plot. In two lines."},
		"heading marker":         {"# Not a heading", `\# Not a heading`},
		"bullet":                 {"- not a list", `\- not a list`},
		"star bullet":            {"* not a list", `\* not a list`},
		"quote":                  {"> not a quote", `\> not a quote`},
		"numbered line":          {"2001. A Space Odyssey", `2001\. A Space Odyssey`},
		"numbered parenthesis":   {"1) first", `1\) first`},
		"number not a marker":    {"1984 is a year", "1984 is a year"},
		"inner markers are kept": {"Ends with a dash -", "Ends with a dash -"},
		"escaped backslash":      {`\start`, `\\start`},
	} {
		t.Run(name, func(t *testing.T) {
			if got := normalizeSynopsis(tc.in); got != tc.want {
				t.Errorf("normalizeSynopsis(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestF028B2_NormalizeSynopsisCapsAtFiveThousandCharacters(t *testing.T) {
	long := strings.Repeat("é", 6000)
	got := normalizeSynopsis(long)
	if n := len([]rune(got)); n != maxSynopsisRunes {
		t.Fatalf("runes = %d, want %d", n, maxSynopsisRunes)
	}
	// A leading escape counts inside the cap and is never cut apart.
	got = normalizeSynopsis("#" + strings.Repeat("a", 6000))
	if len(got) < 2 || got[:2] != `\#` || len([]rune(got)) != maxSynopsisRunes {
		t.Errorf("escaped long text starts %q with %d runes", got[:min(len(got), 4)], len([]rune(got)))
	}
}

func TestF028B2_CreateWritesSummaryAsLookupVersionAndTracksIt(t *testing.T) {
	e := newApplyEnv(t, time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC))
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreate, ListID: e.list, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "matrix", CreateInput: &items.CreateInput{Title: "matrix"},
		Fields: map[string]any{"release_year": 1999}, Synopsis: "A hacker learns the truth.", HistoryLimit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	versions := e.repo.content[it.ID]
	if len(versions) != 1 || versions[0].version != 1 || versions[0].body != "A hacker learns the truth." {
		t.Fatalf("content = %+v, want one version 1 with the summary", versions)
	}
	if versions[0].origin == nil || *versions[0].origin != "lookup" || versions[0].author.Valid {
		t.Errorf("origin = %v author = %v, want lookup and no author", versions[0].origin, versions[0].author)
	}
	row := e.repo.imports[it.ID]
	sum := sha256.Sum256([]byte("A hacker learns the truth."))
	if row.DescriptionVersion == nil || *row.DescriptionVersion != 1 || string(row.DescriptionSHA256) != string(sum[:]) {
		t.Errorf("tracking = %v %x, want version 1 and the text's hash", row.DescriptionVersion, row.DescriptionSHA256)
	}
	if row.ImportGeneration != currentImportGeneration {
		t.Errorf("import generation = %d, want %d", row.ImportGeneration, currentImportGeneration)
	}
}

func TestF028B2_EmptyDescriptionIsFilledButAUserTextIsNot(t *testing.T) {
	e := newApplyEnv(t, time.Now().UTC())
	empty := e.addItem("Empty", nil)
	filled := e.addItem("Filled", nil)
	e.repo.content[filled] = []fakeContent{{version: 1, body: "My own words.", sha: []byte("x")}}

	e.apply(t, empty, ModeFill, "The summary.")
	e.apply(t, filled, ModeFill, "The summary.")

	if got := e.repo.content[empty]; len(got) != 1 || got[0].body != "The summary." {
		t.Errorf("empty description = %+v, want the summary", got)
	}
	if got := e.repo.content[filled]; len(got) != 1 || got[0].body != "My own words." {
		t.Errorf("user description = %+v, want untouched", got)
	}
	// The import row still records the provider fields; only the description is untracked.
	if row := e.repo.imports[filled]; row.DescriptionVersion != nil || row.DescriptionSHA256 != nil {
		t.Errorf("a user's text was tracked as imported: %v", row.DescriptionVersion)
	}
}

func TestF028B2_TrackedSynopsisIsReplacedOnRematchAndRefreshOnlyWhenChanged(t *testing.T) {
	e := newApplyEnv(t, time.Now().UTC())
	id := e.addItem("Matrix", nil)
	e.apply(t, id, ModeFill, "First summary.")

	e.apply(t, id, ModeRematch, "Second summary.")
	if got := e.versionsOf(id); !equalInts(got, []int{1, 2}) {
		t.Fatalf("after rematch versions = %v, want [1 2]", got)
	}
	e.apply(t, id, ModeScheduled, "Second summary.")
	if got := e.versionsOf(id); !equalInts(got, []int{1, 2}) {
		t.Errorf("unchanged refresh versions = %v, want no new version", got)
	}
	e.apply(t, id, ModeScheduled, "Third summary.")
	if got := e.versionsOf(id); !equalInts(got, []int{1, 2, 3}) {
		t.Errorf("after refresh versions = %v, want [1 2 3]", got)
	}
	if row := e.repo.imports[id]; row.DescriptionVersion == nil || *row.DescriptionVersion != 3 {
		t.Errorf("tracking = %v, want version 3", row.DescriptionVersion)
	}
}

func TestF028B2_FillLeavesATrackedSynopsisAlone(t *testing.T) {
	e := newApplyEnv(t, time.Now().UTC())
	id := e.addItem("Matrix", nil)
	e.apply(t, id, ModeFill, "First summary.")
	e.apply(t, id, ModeFill, "Newer summary.")

	if got := e.versionsOf(id); !equalInts(got, []int{1}) {
		t.Errorf("versions = %v, want the first summary only", got)
	}
	if row := e.repo.imports[id]; row.DescriptionVersion == nil || *row.DescriptionVersion != 1 {
		t.Errorf("a fill that changed nothing dropped the tracking: %v", row.DescriptionVersion)
	}
}

// TestF028B2_UserEditEndsTrackingAndRefreshNeverTouchesIt: a user save makes a new version, so the
// text is the user's from then on: refresh and re-match leave it, and the tracking is cleared.
func TestF028B2_UserEditEndsTrackingAndRefreshNeverTouchesIt(t *testing.T) {
	e := newApplyEnv(t, time.Now().UTC())
	id := e.addItem("Matrix", nil)
	e.apply(t, id, ModeFill, "The imported summary.")
	e.repo.content[id] = append(e.repo.content[id], fakeContent{version: 2, body: "My edit.", sha: []byte("edit")})

	e.apply(t, id, ModeScheduled, "A newer summary.")
	e.apply(t, id, ModeRematch, "Another summary.")

	if got := e.versionsOf(id); !equalInts(got, []int{1, 2}) {
		t.Errorf("versions = %v, want the user's edit to stay the last one", got)
	}
	if last := e.repo.content[id][len(e.repo.content[id])-1]; last.body != "My edit." {
		t.Errorf("last body = %q, want the user's edit", last.body)
	}
	if row := e.repo.imports[id]; row.DescriptionVersion != nil || row.DescriptionSHA256 != nil {
		t.Errorf("tracking = %v, want cleared", row.DescriptionVersion)
	}
}

func TestF028B2_AnAbsentSummaryLeavesTheTrackedText(t *testing.T) {
	e := newApplyEnv(t, time.Now().UTC())
	id := e.addItem("Matrix", nil)
	e.apply(t, id, ModeFill, "The summary.")
	e.apply(t, id, ModeScheduled, "")

	if got := e.versionsOf(id); !equalInts(got, []int{1}) {
		t.Errorf("versions = %v, want the summary kept", got)
	}
	if row := e.repo.imports[id]; row.DescriptionVersion == nil || *row.DescriptionVersion != 1 {
		t.Errorf("tracking = %v, want version 1 kept", row.DescriptionVersion)
	}
}

// TestF028B2_HistoryIsPrunedToTheLimit: the lookup prunes like a content save, keeping the current
// version and the limit before it.
func TestF028B2_HistoryIsPrunedToTheLimit(t *testing.T) {
	e := newApplyEnv(t, time.Now().UTC())
	id := e.addItem("Matrix", nil)
	e.apply(t, id, ModeFill, "One.", 1)
	e.apply(t, id, ModeRematch, "Two.", 1)
	e.apply(t, id, ModeRematch, "Three.", 1)

	if got := e.versionsOf(id); !equalInts(got, []int{2, 3}) {
		t.Errorf("versions = %v, want [2 3] (the current one and one before it)", got)
	}
	for _, c := range e.repo.content[id] {
		if c.origin == nil || *c.origin != "lookup" {
			t.Errorf("version %d origin = %v, want lookup", c.version, c.origin)
		}
	}
}

// TestF028B2_UntrackedHardcoverCreateIsTheUsersText: nothing is imported from a Hardcover create, so the
// text is the user's own from the start (origin NULL) and no provenance row is written.
func TestF028B2_UntrackedHardcoverCreateIsTheUsersText(t *testing.T) {
	e := newApplyEnv(t, time.Now().UTC())
	books := uuid.New()
	e.repo.lists[books] = ptr("book")
	user := uuid.New()
	ctx := scope.WithUser(t.Context(), user)
	_, err := e.store.Apply(ctx, ApplyParams{
		Mode: ModeCreateUntracked, ListID: books, Type: "book", CreateInput: &items.CreateInput{Title: "Dune"},
		Title: "Dune", OriginalTitle: "Dune", Fields: map[string]any{"author": "Frank Herbert"},
		Synopsis: "A desert planet.", HistoryLimit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	var created uuid.UUID
	for id := range e.repo.content {
		created = id
	}
	versions := e.repo.content[created]
	if len(versions) != 1 || versions[0].origin != nil || versions[0].author.UUID != user {
		t.Fatalf("content = %+v, want one user version by the caller", versions)
	}
	if len(e.repo.imports) != 0 {
		t.Errorf("imports = %d, want none for an untracked create", len(e.repo.imports))
	}
}

// F-028 (D-162): the lookup's own values never come from the client. A create that sends a
// lookup-owned field is refused before anything is written.
func TestF028B1_CreateRefusesLookupOwnedMetadata(t *testing.T) {
	e := newApplyEnv(t, time.Now().UTC())
	_, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreate, ListID: e.list, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "matrix",
		CreateInput: &items.CreateInput{Title: "matrix", Metadata: map[string]any{"genres": []any{"Western"}}},
		Fields:      map[string]any{"genres": []any{"Drama"}, "release_year": 1999},
	})
	var v *apperr.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	if len(e.repo.items) != 0 || e.repo.commits != 0 {
		t.Errorf("items = %d commits = %d, want nothing written", len(e.repo.items), e.repo.commits)
	}
}

// apply runs a fill, rematch or scheduled apply of an item with its current version, and fails the
// test on error. history is the markdown history limit (default 20).
func (e *applyEnv) apply(t *testing.T, id uuid.UUID, mode Mode, synopsis string, history ...int) {
	t.Helper()
	limit := 20
	if len(history) > 0 {
		limit = history[0]
	}
	p := ApplyParams{
		Mode: mode, ItemID: id, ExpectedVersion: e.repo.items[id].version, Type: "movie",
		Provider: ProviderTMDB, SourceID: "603", Title: "The Matrix", OriginalTitle: "Matrix",
		Fields: map[string]any{"release_year": 1999}, Synopsis: synopsis, HistoryLimit: limit,
	}
	if _, err := e.store.Apply(t.Context(), p); err != nil {
		t.Fatalf("apply %s: %v", mode, err)
	}
}

// versionsOf lists the content version numbers of an item, oldest first.
func (e *applyEnv) versionsOf(id uuid.UUID) []int {
	var out []int
	for _, c := range e.repo.content[id] {
		out = append(out, c.version)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
