package database_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixed UUIDs used by the constraint cases.
const (
	wsA     = "00000000-0000-0000-0000-00000000000a"
	wsB     = "00000000-0000-0000-0000-00000000000b"
	folderA = "00000000-0000-0000-0000-0000000000f1"
	folderB = "00000000-0000-0000-0000-0000000000f2"
	listA   = "00000000-0000-0000-0000-0000000000c1"
	itemA   = "00000000-0000-0000-0000-0000000000d1"
	itemB   = "00000000-0000-0000-0000-0000000000d2"
	attA    = "00000000-0000-0000-0000-0000000000e1"
	attB    = "00000000-0000-0000-0000-0000000000e2"
	batchA  = "00000000-0000-0000-0000-0000000000b1"
)

// Reusable setup statements (all in workspace wsA unless stated).
var (
	sqlWorkspaces = `INSERT INTO workspaces (id, name) VALUES ('` + wsA + `', 'A'), ('` + wsB + `', 'B')`
	sqlFolderB    = `INSERT INTO folders (id, workspace_id, name, sort_order) VALUES ('` + folderB + `', '` + wsB + `', 'F', 0)`
	sqlListA      = `INSERT INTO lists (id, workspace_id, name, type, sort_order) VALUES ('` + listA + `', '` + wsA + `', 'L', 'generic', 0)`
	sqlBatchA     = `INSERT INTO trash_batches (id, workspace_id, root_type, root_id, root_name, former_path)
	                   VALUES ('` + batchA + `', '` + wsA + `', 'item', '` + itemA + `', 'I', '[]')`
	sqlAttA = attachmentSQL(attA, "a")
	sqlAttB = attachmentSQL(attB, "b")
)

const userA = "00000000-0000-0000-0000-0000000000a1"

func userSQL(id, ws string) string {
	return `INSERT INTO users (id, workspace_id, email) VALUES ('` + id + `', '` + ws + `', 'a@test.dev')`
}

func attachmentSQL(id, key string) string {
	return `INSERT INTO attachments (id, workspace_id, storage_key, thumbnail_storage_key, media_type, size_bytes, width, height, sha256)
	        VALUES ('` + id + `', '` + wsA + `', 'images/` + key + `.webp', 'images/` + key + `.thumb.jpg', 'image/webp', 10, 1, 1, '\x00')`
}

// itemSQL inserts an item in listA with the given id and extra column assignments.
func itemSQL(id, cols, vals string) string {
	if cols != "" {
		cols, vals = ", "+cols, ", "+vals
	}
	return `INSERT INTO items (id, workspace_id, list_id, title, sort_order` + cols + `)
	        VALUES ('` + id + `', '` + wsA + `', '` + listA + `', 'T', 0` + vals + `)`
}

func folderSQL(id, cols, vals string) string {
	return `INSERT INTO folders (id, workspace_id, name, sort_order, ` + cols + `)
	        VALUES ('` + id + `', '` + wsA + `', 'F', 0, ` + vals + `)`
}

func contentVersionSQL(itemID string, version int) string {
	return fmt.Sprintf(`INSERT INTO item_content_versions (item_id, version, body, size_bytes, sha256) VALUES ('%s', %d, 'hi', 2, '\x00')`, itemID, version)
}

type schemaCase struct {
	name       string
	stmts      []string // executed in order; only the last may fail
	wantCode   string   // SQLSTATE of the last statement; "" means success
	wantConstr string   // constraint name; "" when wantCode is ""
}

func schemaCases() []schemaCase {
	itemParents := []string{sqlWorkspaces, sqlListA}
	with := func(parents []string, last string) []string {
		return append(append([]string{}, parents...), last)
	}
	return []schemaCase{
		{
			name: "F-027 folder owned by a user of another workspace",
			stmts: []string{sqlWorkspaces, userSQL(userA, wsA),
				`INSERT INTO folders (workspace_id, name, sort_order, owner_user_id) VALUES ('` + wsB + `', 'F', 0, '` + userA + `')`},
			wantCode: "23503", wantConstr: "folders_owner_fk",
		},
		{
			name: "F-027 list owned by a user of another workspace",
			stmts: []string{sqlWorkspaces, userSQL(userA, wsA),
				`INSERT INTO lists (workspace_id, name, type, sort_order, owner_user_id) VALUES ('` + wsB + `', 'L', 'generic', 0, '` + userA + `')`},
			wantCode: "23503", wantConstr: "lists_owner_fk",
		},
		{
			name: "F-027 an owner is optional (the previous release inserts none) and no check ties it to the parent",
			stmts: []string{sqlWorkspaces, userSQL(userA, wsA),
				`INSERT INTO folders (workspace_id, name, sort_order) VALUES ('` + wsA + `', 'old release', 0)`,
				`INSERT INTO folders (id, workspace_id, name, sort_order, owner_user_id) VALUES ('` + folderA + `', '` + wsA + `', 'top', 0, '` + userA + `')`,
				`INSERT INTO folders (workspace_id, parent_folder_id, name, sort_order, owner_user_id) VALUES ('` + wsA + `', '` + folderA + `', 'nested', 0, '` + userA + `')`},
		},
		{
			name: "F-027 order row with an unknown entry type",
			stmts: []string{sqlWorkspaces, userSQL(userA, wsA),
				`INSERT INTO user_top_order (workspace_id, user_id, entry_type, entry_id, sort_order) VALUES ('` + wsA + `', '` + userA + `', 'item', '` + folderA + `', 0)`},
			wantCode: "23514", wantConstr: "user_top_order_type_check",
		},
		{
			name: "F-027 order row of a user of another workspace",
			stmts: []string{sqlWorkspaces, userSQL(userA, wsA),
				`INSERT INTO user_top_order (workspace_id, user_id, entry_type, entry_id, sort_order) VALUES ('` + wsB + `', '` + userA + `', 'folder', '` + folderA + `', 0)`},
			wantCode: "23503", wantConstr: "user_top_order_user_fk",
		},
		{
			name: "F-027 an order row needs no folder or list (no foreign key: the previous purge must keep working)",
			stmts: []string{sqlWorkspaces, userSQL(userA, wsA),
				`INSERT INTO user_top_order (workspace_id, user_id, entry_type, entry_id, sort_order) VALUES ('` + wsA + `', '` + userA + `', 'folder', '` + folderA + `', 0)`},
		},
		{
			name: "F-027 B3 a member of a user of another workspace",
			stmts: []string{sqlWorkspaces, userSQL(userA, wsA),
				`INSERT INTO folder_members (workspace_id, folder_id, user_id) VALUES ('` + wsB + `', '` + folderA + `', '` + userA + `')`},
			wantCode: "23503", wantConstr: "folder_members_user_fk",
		},
		{
			name: "F-027 B3 a member row needs no folder (no foreign key: the previous purge must keep working)",
			stmts: []string{sqlWorkspaces, userSQL(userA, wsA),
				`INSERT INTO folder_members (workspace_id, folder_id, user_id) VALUES ('` + wsA + `', '` + folderA + `', '` + userA + `')`},
		},
		{
			name: "list references folder of another workspace",
			stmts: []string{sqlWorkspaces, sqlFolderB,
				`INSERT INTO lists (workspace_id, folder_id, name, type, sort_order) VALUES ('` + wsA + `', '` + folderB + `', 'L', 'generic', 0)`},
			wantCode: "23503", wantConstr: "lists_folder_fkey",
		},
		{
			name: "AT-07 item with both image_url and image_attachment_id",
			stmts: with(append(append([]string{}, itemParents...), sqlAttA),
				itemSQL(itemA, "image_url, image_attachment_id", `'https://example.com/a.png', '`+attA+`'`)),
			wantCode: "23514", wantConstr: "items_image_source_check",
		},
		{
			name:     "folder deleted_at without trash_batch_id",
			stmts:    with([]string{sqlWorkspaces}, folderSQL(folderA, "deleted_at", "now()")),
			wantCode: "23514", wantConstr: "folders_trash_check",
		},
		{
			name:     "folder trash_batch_id without deleted_at",
			stmts:    with([]string{sqlWorkspaces, sqlBatchA}, folderSQL(folderA, "trash_batch_id", `'`+batchA+`'`)),
			wantCode: "23514", wantConstr: "folders_trash_check",
		},
		{
			name:     "item deleted_at without trash_batch_id",
			stmts:    with(itemParents, itemSQL(itemA, "deleted_at", "now()")),
			wantCode: "23514", wantConstr: "items_trash_check",
		},
		{
			name:     "item trash_batch_id without deleted_at",
			stmts:    with(append(append([]string{}, itemParents...), sqlBatchA), itemSQL(itemA, "trash_batch_id", `'`+batchA+`'`)),
			wantCode: "23514", wantConstr: "items_trash_check",
		},
		{
			name:     "price_min greater than price_max",
			stmts:    with(itemParents, itemSQL(itemA, "price_min, price_max, currency", "10, 5, 'EUR'")),
			wantCode: "23514", wantConstr: "items_price_range_check",
		},
		{
			name:     "price without currency",
			stmts:    with(itemParents, itemSQL(itemA, "price_min, price_max", "5, 10")),
			wantCode: "23514", wantConstr: "items_price_currency_check",
		},
		{
			name:     "folder is its own parent",
			stmts:    with([]string{sqlWorkspaces}, folderSQL(folderA, "parent_folder_id", `'`+folderA+`'`)),
			wantCode: "23514", wantConstr: "folders_no_self_parent_check",
		},
		{
			name:     "metadata is an array",
			stmts:    with(itemParents, itemSQL(itemA, "metadata", "'[]'")),
			wantCode: "23514", wantConstr: "items_metadata_check",
		},
		{
			name: "F007 list type is not lowercase snake case",
			stmts: []string{sqlWorkspaces,
				`INSERT INTO lists (workspace_id, name, type, sort_order) VALUES ('` + wsA + `', 'L', 'Movie', 0)`},
			wantCode: "23514", wantConstr: "lists_type_check",
		},
		{
			name: "F018 list type is optional (no type)",
			stmts: []string{sqlWorkspaces,
				`INSERT INTO lists (workspace_id, name, sort_order) VALUES ('` + wsA + `', 'L', 0)`},
		},
		{
			name: "F014-B4 list sort_mode field key is valid",
			stmts: []string{sqlWorkspaces,
				`INSERT INTO lists (workspace_id, name, type, sort_mode, sort_order) VALUES ('` + wsA + `', 'L', 'movie', 'release_year', 0)`},
		},
		{
			name: "F002 list sort_mode price is valid",
			stmts: []string{sqlWorkspaces,
				`INSERT INTO lists (workspace_id, name, type, sort_mode, sort_order) VALUES ('` + wsA + `', 'L', 'movie', 'price', 0)`},
		},
		{
			name: "F014-B4 list sort_mode is unconstrained storage for definition keys",
			stmts: []string{sqlWorkspaces,
				`INSERT INTO lists (workspace_id, name, type, sort_mode, sort_order) VALUES ('` + wsA + `', 'L', 'movie', 'random', 0)`},
		},
		{
			name: "F007 list description up to 64 KiB is valid",
			stmts: []string{sqlWorkspaces,
				`INSERT INTO lists (workspace_id, name, type, description, sort_order) VALUES ('` + wsA + `', 'L', 'generic', repeat('x', 65536), 0)`},
		},
		{
			name: "F007 list description over 64 KiB",
			stmts: []string{sqlWorkspaces,
				`INSERT INTO lists (workspace_id, name, type, description, sort_order) VALUES ('` + wsA + `', 'L', 'generic', repeat('x', 65537), 0)`},
			wantCode: "23514", wantConstr: "lists_description_check",
		},
		{
			name:     "F007 item website_url over 2048 characters",
			stmts:    with(itemParents, itemSQL(itemA, "website_url", "repeat('x', 2049)")),
			wantCode: "23514", wantConstr: "items_website_url_check",
		},
		{
			name:     "F007 items no longer have a type column",
			stmts:    with(itemParents, itemSQL(itemA, "type", "'movie'")),
			wantCode: "42703", wantConstr: "",
		},
		{
			name: "folder name empty",
			stmts: []string{sqlWorkspaces,
				`INSERT INTO folders (workspace_id, name, sort_order) VALUES ('` + wsA + `', '', 0)`},
			wantCode: "23514", wantConstr: "folders_name_check",
		},
		{
			name: "folder name 201 chars",
			stmts: []string{sqlWorkspaces,
				`INSERT INTO folders (workspace_id, name, sort_order) VALUES ('` + wsA + `', repeat('x', 201), 0)`},
			wantCode: "23514", wantConstr: "folders_name_check",
		},
		{
			name: "folder name 200 chars is valid",
			stmts: []string{sqlWorkspaces,
				`INSERT INTO folders (workspace_id, name, sort_order) VALUES ('` + wsA + `', repeat('x', 200), 0)`},
		},
		{
			name: "streaming country code lowercase",
			stmts: []string{sqlWorkspaces,
				`INSERT INTO workspace_settings (workspace_id, streaming_country_code) VALUES ('` + wsA + `', 'it')`},
			wantCode: "23514", wantConstr: "workspace_settings_streaming_country_code_check",
		},
		{
			name: "duplicate attachment storage_key",
			stmts: []string{sqlWorkspaces, sqlAttA,
				`INSERT INTO attachments (workspace_id, storage_key, thumbnail_storage_key, media_type, size_bytes, width, height, sha256)
				 VALUES ('` + wsA + `', 'images/a.webp', 'images/other.thumb.jpg', 'image/webp', 10, 1, 1, '\x00')`},
			wantCode: "23505", wantConstr: "attachments_storage_key_key",
		},
		{
			name: "two items with the same image_attachment_id",
			stmts: []string{sqlWorkspaces, sqlListA, sqlAttA,
				itemSQL(itemA, "image_attachment_id", `'`+attA+`'`),
				itemSQL(itemB, "image_attachment_id", `'`+attA+`'`)},
			wantCode: "23505", wantConstr: "items_image_attachment_id_key",
		},
		{
			name: "duplicate content version for the same item and version",
			stmts: []string{sqlWorkspaces, sqlListA,
				itemSQL(itemA, "", ""),
				contentVersionSQL(itemA, 1),
				contentVersionSQL(itemA, 1)},
			wantCode: "23505", wantConstr: "item_content_versions_pkey",
		},
		{
			name: "valid item with price range and currency",
			stmts: with(itemParents,
				itemSQL(itemA, "price_min, price_max, currency", "5, 10, 'EUR'")),
		},
		{
			name: "valid item with equal price bounds and image attachment",
			stmts: with(append(append([]string{}, itemParents...), sqlAttB),
				itemSQL(itemA, "price_min, price_max, currency, image_attachment_id", `7.50, 7.50, 'USD', '`+attB+`'`)),
		},
	}
}

func runSchemaCase(t *testing.T, pool *pgxpool.Pool, c schemaCase) {
	t.Helper()
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // always rolled back

	last := len(c.stmts) - 1
	for i, s := range c.stmts[:last] {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("setup statement %d failed: %v", i, err)
		}
	}
	_, err = tx.Exec(ctx, c.stmts[last])

	if c.wantCode == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v, want PgError %s %s", err, c.wantCode, c.wantConstr)
	}
	if pgErr.Code != c.wantCode || pgErr.ConstraintName != c.wantConstr {
		t.Errorf("got %s %q (%s), want %s %q", pgErr.Code, pgErr.ConstraintName, pgErr.Message, c.wantCode, c.wantConstr)
	}
}

func TestSchema_Constraints(t *testing.T) {
	pool := newMigratedPool(t)
	for _, c := range schemaCases() {
		t.Run(c.name, func(t *testing.T) { runSchemaCase(t, pool, c) })
	}
}

func TestSchema_SettingsDefaults(t *testing.T) {
	pool := newMigratedPool(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, sqlWorkspaces); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspace_settings (workspace_id) VALUES ($1)`, wsA); err != nil {
		t.Fatal(err)
	}
	var (
		country        string
		limit, version int
	)
	err := pool.QueryRow(ctx, `SELECT streaming_country_code, markdown_history_limit, version
	                           FROM workspace_settings WHERE workspace_id = $1`, wsA).
		Scan(&country, &limit, &version)
	if err != nil {
		t.Fatal(err)
	}
	if country != "IT" || limit != 20 || version != 1 {
		t.Errorf("defaults = %q %d %d, want IT 20 1", country, limit, version)
	}
}

func TestSchema_D051_BatchStillReferenced(t *testing.T) {
	pool := newMigratedPool(t)
	ctx := t.Context()
	for _, s := range []string{
		sqlWorkspaces,
		sqlBatchA,
		folderSQL(folderA, "deleted_at, trash_batch_id", `now(), '`+batchA+`'`),
	} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	_, err := pool.Exec(ctx, `DELETE FROM trash_batches WHERE id = $1`, batchA)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("delete error = %v, want PgError", err)
	}
	if pgErr.Code != "23503" || pgErr.ConstraintName != "folders_trash_batch_id_fkey" {
		t.Errorf("got %s %q, want 23503 folders_trash_batch_id_fkey", pgErr.Code, pgErr.ConstraintName)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM trash_batches WHERE id = $1`, batchA).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("trash batch rows = %d, want 1", n)
	}
}

// TestSchema_NoCascades pins that the baseline has no ON DELETE CASCADE: a parent
// with children cannot be deleted, the application deletes children explicitly.
func TestSchema_NoCascades(t *testing.T) {
	folder := `INSERT INTO folders (id, workspace_id, name, sort_order) VALUES ('` + folderA + `', '` + wsA + `', 'F', 0)`
	list := `INSERT INTO lists (id, workspace_id, folder_id, name, type, sort_order) VALUES ('` + listA + `', '` + wsA + `', '` + folderA + `', 'L', 'movie', 0)`
	cases := []struct {
		name       string
		setup      []string
		stmt       string
		wantConstr string
	}{
		{
			name:       "deleting a workspace with a folder",
			setup:      []string{sqlWorkspaces, folder},
			stmt:       `DELETE FROM workspaces WHERE id = '` + wsA + `'`,
			wantConstr: "folders_workspace_id_fkey",
		},
		{
			name:       "deleting a folder with a list",
			setup:      []string{sqlWorkspaces, folder, list},
			stmt:       `DELETE FROM folders WHERE id = '` + folderA + `'`,
			wantConstr: "lists_folder_fkey",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pool := newMigratedPool(t)
			ctx := t.Context()
			for i, s := range c.setup {
				if _, err := pool.Exec(ctx, s); err != nil {
					t.Fatalf("setup statement %d: %v", i, err)
				}
			}

			_, err := pool.Exec(ctx, c.stmt)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) {
				t.Fatalf("delete error = %v, want PgError 23503 %s", err, c.wantConstr)
			}
			if pgErr.Code != "23503" || pgErr.ConstraintName != c.wantConstr {
				t.Errorf("got %s %q, want 23503 %s", pgErr.Code, pgErr.ConstraintName, c.wantConstr)
			}
		})
	}
}

// TestSchema_NoTriggersOrFunctions pins that the baseline carries no logic:
// no user trigger and no function in public (extension functions excluded).
func TestSchema_NoTriggersOrFunctions(t *testing.T) {
	pool := newMigratedPool(t)
	ctx := t.Context()

	var triggers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE NOT tgisinternal`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if triggers != 0 {
		t.Errorf("user triggers = %d, want 0", triggers)
	}

	var functions int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'public'
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d
		                  WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')`).Scan(&functions); err != nil {
		t.Fatal(err)
	}
	if functions != 0 {
		t.Errorf("user functions in public = %d, want 0", functions)
	}
}
