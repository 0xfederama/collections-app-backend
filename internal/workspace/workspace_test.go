package workspace_test

import (
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/testdb"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testdb.Terminate()
	os.Exit(code)
}

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return testdb.NewMigrated(t, testdb.AdminURL(t))
}

func count(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAT02_BootstrapSequential(t *testing.T) {
	pool := newPool(t)

	first, err := workspace.Bootstrap(t.Context(), pool, "Our Collection")
	if err != nil {
		t.Fatal(err)
	}
	// Data from run 1 must survive: rename the workspace, then bootstrap again with another name.
	if _, err := pool.Exec(t.Context(), "UPDATE workspaces SET name = 'Renamed'"); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		got, err := workspace.Bootstrap(t.Context(), pool, "Should Be Ignored")
		if err != nil {
			t.Fatalf("run %d: %v", i+2, err)
		}
		if got != first {
			t.Errorf("run %d: id = %s, want %s", i+2, got, first)
		}
	}

	if n := count(t, pool, "workspaces"); n != 1 {
		t.Errorf("workspaces = %d, want 1", n)
	}
	if n := count(t, pool, "workspace_settings"); n != 1 {
		t.Errorf("workspace_settings = %d, want 1", n)
	}
	var name string
	if err := pool.QueryRow(t.Context(), "SELECT name FROM workspaces").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Renamed" {
		t.Errorf("name = %q, want it untouched by later bootstraps", name)
	}
}

func TestAT02_BootstrapConcurrent(t *testing.T) {
	pool := newPool(t)

	const runs = 3
	ids := make([]uuid.UUID, runs)
	errs := make([]error, runs)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ids[i], errs[i] = workspace.Bootstrap(t.Context(), pool, "Our Collection")
		}()
	}
	close(start)
	wg.Wait()

	for i := range runs {
		if errs[i] != nil {
			t.Fatalf("run %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Errorf("run %d: id = %s, want %s", i, ids[i], ids[0])
		}
	}
	if n := count(t, pool, "workspaces"); n != 1 {
		t.Errorf("workspaces = %d, want 1", n)
	}
	if n := count(t, pool, "workspace_settings"); n != 1 {
		t.Errorf("workspace_settings = %d, want 1", n)
	}
}

func TestAT02_BootstrapFailsWithTwoWorkspaces(t *testing.T) {
	pool := newPool(t)
	if _, err := pool.Exec(t.Context(), "INSERT INTO workspaces (name) VALUES ('A'), ('B')"); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Bootstrap(t.Context(), pool, "Our Collection"); err == nil {
		t.Fatal("want an error with two workspaces")
	}
}

func TestBootstrap_DefaultsAndInvalidName(t *testing.T) {
	pool := newPool(t)
	if _, err := workspace.Bootstrap(t.Context(), pool, ""); err == nil {
		t.Fatal("want an error for an empty workspace name")
	}
	if n := count(t, pool, "workspaces"); n != 0 {
		t.Errorf("workspaces = %d after a failed bootstrap, want 0 (rolled back)", n)
	}

	id, err := workspace.Bootstrap(t.Context(), pool, "Ours")
	if err != nil {
		t.Fatal(err)
	}
	got, err := workspace.NewService(pool).Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	want := workspace.Settings{WorkspaceName: "Ours", StreamingCountryCode: "IT", MetadataLanguage: "it-IT", MarkdownHistoryLimit: 20, Version: 1}
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
}

func TestBootstrap_SeedsBuiltinTypeDefinitions(t *testing.T) {
	pool := newPool(t)
	workspaceID, err := workspace.Bootstrap(t.Context(), pool, "Ours")
	if err != nil {
		t.Fatal(err)
	}
	var types, fields int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM type_definitions WHERE workspace_id = $1 AND is_locked`, workspaceID).Scan(&types); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM type_fields WHERE workspace_id = $1 AND is_locked`, workspaceID).Scan(&fields); err != nil {
		t.Fatal(err)
	}
	if types != 5 || fields != 29 {
		t.Errorf("seeded definitions = %d types, %d fields; want 5 types, 29 fields", types, fields)
	}
}
