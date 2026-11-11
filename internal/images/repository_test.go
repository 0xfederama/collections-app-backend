package images_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testdb.Terminate()
	os.Exit(code)
}

var (
	now   = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	later = now.Add(time.Hour)
)

// env is a migrated database with one bootstrapped workspace. ctx is scoped to it.
type env struct {
	t    *testing.T
	pool *pgxpool.Pool
	repo *images.Postgres
	ctx  context.Context
	ws   uuid.UUID
}

func newEnv(t *testing.T) env {
	t.Helper()
	pool := testdb.NewMigrated(t, testdb.AdminURL(t))
	ws, err := workspace.Bootstrap(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	return env{t: t, pool: pool, repo: images.NewPostgres(pool), ctx: scope.With(t.Context(), ws), ws: ws}
}

func ptr[T any](v T) *T { return &v }

// tx runs fn in one transaction through the repository.
func (e env) tx(fn func(r images.Repo) error) {
	e.t.Helper()
	if err := e.repo.InTx(e.ctx, fn); err != nil {
		e.t.Fatal(err)
	}
}

// insertAttachment stores an attachment created at created and returns it.
func (e env) insertAttachment(created time.Time) images.Attachment {
	e.t.Helper()
	a := images.Attachment{
		ID: uuid.New(), StorageKey: "images/aa/bb/" + uuid.NewString() + ".jpg",
		ThumbnailStorageKey: "images/aa/bb/" + uuid.NewString() + ".thumb.jpg",
		MediaType:           "image/jpeg", SizeBytes: 10, OriginalFilename: ptr("pic.jpg"),
		Width: 40, Height: 30, SHA256: make([]byte, 32),
	}
	e.tx(func(r images.Repo) error { return r.InsertAttachment(e.ctx, a, created) })
	return a
}

// insertItem inserts an active item with one sort row in a new list of the workspace.
func (e env) insertItem(title string) uuid.UUID {
	e.t.Helper()
	var list uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `INSERT INTO lists (workspace_id, name, sort_order) VALUES ($1, 'L', 0) RETURNING id`, e.ws).Scan(&list); err != nil {
		e.t.Fatal(err)
	}
	var id uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `INSERT INTO items (workspace_id, list_id, title, sort_order) VALUES ($1, $2, $3, 0) RETURNING id`,
		e.ws, list, title).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// point sets the attachment of an item directly, as an upload would.
func (e env) point(item, attachment uuid.UUID) {
	e.t.Helper()
	if _, err := e.pool.Exec(e.ctx, `UPDATE items SET image_attachment_id = $2 WHERE id = $1`, item, attachment); err != nil {
		e.t.Fatal(err)
	}
}

func (e env) itemRow(id uuid.UUID) (version int, updated time.Time, attachment *uuid.UUID, url *string) {
	e.t.Helper()
	var nullAtt uuid.NullUUID
	if err := e.pool.QueryRow(e.ctx, `SELECT version, updated_at, image_attachment_id, image_url FROM items WHERE id = $1`, id).
		Scan(&version, &updated, &nullAtt, &url); err != nil {
		e.t.Fatal(err)
	}
	if nullAtt.Valid {
		attachment = &nullAtt.UUID
	}
	return version, updated, attachment, url
}

func isNotFound(err error) bool {
	var nf *apperr.NotFoundError
	return errors.As(err, &nf)
}

func TestRepoAttachmentRoundTrip(t *testing.T) {
	e := newEnv(t)
	a := e.insertAttachment(now)

	var got images.Attachment
	var err error
	e.tx(func(r images.Repo) error {
		got, err = r.GetAttachment(e.ctx, a.ID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID || got.StorageKey != a.StorageKey || got.ThumbnailStorageKey != a.ThumbnailStorageKey ||
		got.MediaType != "image/jpeg" || got.SizeBytes != 10 {
		t.Errorf("attachment = %+v, want %+v", got, a)
	}

	e.tx(func(r images.Repo) error {
		_, err = r.GetAttachment(e.ctx, uuid.New())
		return nil
	})
	if !isNotFound(err) {
		t.Errorf("missing attachment err = %v, want NotFound", err)
	}
}

func TestRepoGetAttachmentIsWorkspaceScoped(t *testing.T) {
	e := newEnv(t)
	a := e.insertAttachment(now)

	var otherWS uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `INSERT INTO workspaces (name) VALUES ('Other') RETURNING id`).Scan(&otherWS); err != nil {
		t.Fatal(err)
	}
	other := scope.With(t.Context(), otherWS)
	var err error
	e.tx(func(r images.Repo) error {
		_, err = r.GetAttachment(other, a.ID)
		return nil
	})
	if !isNotFound(err) {
		t.Errorf("attachment of another workspace err = %v, want NotFound", err)
	}
}

func TestRepoDeleteAttachmentReturnsKeysOnce(t *testing.T) {
	e := newEnv(t)
	a := e.insertAttachment(now)

	keys, err := e.repo.DeleteAttachment(e.ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if keys.StorageKey != a.StorageKey || keys.ThumbnailStorageKey != a.ThumbnailStorageKey {
		t.Errorf("keys = %+v, want the stored keys of %s", keys, a.ID)
	}
	if _, err := e.repo.DeleteAttachment(e.ctx, a.ID); !isNotFound(err) {
		t.Errorf("second delete err = %v, want NotFound", err)
	}
}

func TestRepoDeleteAttachmentKeepsReferencedRow(t *testing.T) {
	e := newEnv(t)
	a := e.insertAttachment(now)
	item := e.insertItem("Photo")
	e.point(item, a.ID)

	if _, err := e.repo.DeleteAttachment(e.ctx, a.ID); !isNotFound(err) {
		t.Fatalf("referenced delete err = %v, want NotFound", err)
	}
	var count int
	if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM attachments WHERE id = $1`, a.ID).Scan(&count); err != nil || count != 1 {
		t.Errorf("referenced row count = %d (%v), want 1", count, err)
	}
}

func TestRepoListAttachmentKeysBothFilesAndWorkspace(t *testing.T) {
	e := newEnv(t)
	a := e.insertAttachment(now)

	var otherWS uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `INSERT INTO workspaces (name) VALUES ('Other') RETURNING id`).Scan(&otherWS); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO attachments (workspace_id, storage_key, thumbnail_storage_key, media_type, size_bytes, width, height, sha256)
		VALUES ($1, 'images/cc/dd/foreign.jpg', 'images/cc/dd/foreign.thumb.jpg', 'image/jpeg', 1, 1, 1, $2)`, otherWS, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}

	var keys []string
	var err error
	e.tx(func(r images.Repo) error {
		keys, err = r.ListAttachmentKeys(e.ctx)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(keys)
	want := []string{a.StorageKey, a.ThumbnailStorageKey}
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v (both files, this workspace only)", keys, want)
	}
}

func TestRepoListUnreferencedAttachments(t *testing.T) {
	e := newEnv(t)
	cutoff := now.Add(-time.Hour)
	oldOrphan := e.insertAttachment(now.Add(-2 * time.Hour))
	e.insertAttachment(now) // too new: still between "files written" and "committed"
	oldUsed := e.insertAttachment(now.Add(-2 * time.Hour))
	e.point(e.insertItem("Used"), oldUsed.ID)

	var ids []uuid.UUID
	var err error
	e.tx(func(r images.Repo) error {
		ids, err = r.ListUnreferencedAttachments(e.ctx, cutoff)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != oldOrphan.ID {
		t.Errorf("unreferenced = %v, want only %s", ids, oldOrphan.ID)
	}
}

func TestRepoLockItemReturnsVersionAndImage(t *testing.T) {
	e := newEnv(t)
	item := e.insertItem("Locked")

	var st images.ItemImageState
	var err error
	e.tx(func(r images.Repo) error {
		st, err = r.LockItem(e.ctx, item)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != 1 || st.AttachmentID != nil || st.URL != nil {
		t.Errorf("state = %+v, want version 1 and no image", st)
	}

	att := e.insertAttachment(now)
	e.point(item, att.ID)
	e.tx(func(r images.Repo) error {
		st, err = r.LockItem(e.ctx, item)
		return err
	})
	if err != nil || st.AttachmentID == nil || *st.AttachmentID != att.ID || st.URL != nil {
		t.Errorf("attachment state = %+v (%v), want attachment %s", st, err, att.ID)
	}

	e.tx(func(r images.Repo) error {
		_, err = r.LockItem(e.ctx, uuid.New())
		return nil
	})
	if !isNotFound(err) {
		t.Errorf("missing item err = %v, want NotFound", err)
	}
	trashed := e.insertItem("Trashed")
	var batch uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `INSERT INTO trash_batches (workspace_id, root_type, root_id, root_name, former_path) VALUES ($1, 'item', $2, 'x', '[]') RETURNING id`,
		e.ws, trashed).Scan(&batch); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE items SET deleted_at = now(), trash_batch_id = $2 WHERE id = $1`, trashed, batch); err != nil {
		t.Fatal(err)
	}
	e.tx(func(r images.Repo) error {
		_, err = r.LockItem(e.ctx, trashed)
		return nil
	})
	if !isNotFound(err) {
		t.Errorf("trashed item err = %v, want NotFound", err)
	}
}

func TestRepoSetItemImageAttachmentBumpsAndClearsURL(t *testing.T) {
	e := newEnv(t)
	item := e.insertItem("Attach")
	if _, err := e.pool.Exec(e.ctx, `UPDATE items SET image_url = 'https://example.com/a.png' WHERE id = $1`, item); err != nil {
		t.Fatal(err)
	}
	att := e.insertAttachment(now)

	e.tx(func(r images.Repo) error { return r.SetItemImageAttachment(e.ctx, item, att.ID, later) })
	version, updated, attachment, url := e.itemRow(item)
	if version != 2 || !updated.Equal(later) || attachment == nil || *attachment != att.ID || url != nil {
		t.Errorf("after attach: version %d updated %s attachment %v url %v", version, updated, attachment, url)
	}
}

func TestRepoSetItemImageURLBumpsAndClearsAttachment(t *testing.T) {
	e := newEnv(t)
	item := e.insertItem("Link")
	e.point(item, e.insertAttachment(now).ID)

	e.tx(func(r images.Repo) error { return r.SetItemImageURL(e.ctx, item, "https://example.com/b.png", later) })
	version, updated, attachment, url := e.itemRow(item)
	if version != 2 || !updated.Equal(later) || attachment != nil || url == nil || *url != "https://example.com/b.png" {
		t.Errorf("after url: version %d updated %s attachment %v url %v", version, updated, attachment, url)
	}
}

func TestRepoClearItemImageBumpsAndClearsBoth(t *testing.T) {
	e := newEnv(t)
	item := e.insertItem("Clear")
	e.point(item, e.insertAttachment(now).ID)

	e.tx(func(r images.Repo) error { return r.ClearItemImage(e.ctx, item, later) })
	version, updated, attachment, url := e.itemRow(item)
	if version != 2 || !updated.Equal(later) || attachment != nil || url != nil {
		t.Errorf("after clear: version %d updated %s attachment %v url %v", version, updated, attachment, url)
	}
}

func TestRepoRollbackUndoesImageWrite(t *testing.T) {
	e := newEnv(t)
	item := e.insertItem("Rollback")
	att := e.insertAttachment(now)

	if err := e.repo.InTx(e.ctx, func(r images.Repo) error {
		if err := r.SetItemImageAttachment(e.ctx, item, att.ID, later); err != nil {
			return err
		}
		return errors.New("stop")
	}); err == nil {
		t.Fatal("InTx returned nil, want the callback error")
	}
	version, _, attachment, _ := e.itemRow(item)
	if version != 1 || attachment != nil {
		t.Errorf("after rollback: version %d attachment %v, want version 1 and no image", version, attachment)
	}
}
