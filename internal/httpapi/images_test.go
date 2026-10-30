package httpapi_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/httpapi"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
	"github.com/0xfederama/collections-app-backend/internal/search"
	"github.com/0xfederama/collections-app-backend/internal/seed"
	"github.com/0xfederama/collections-app-backend/internal/storage"
	"github.com/0xfederama/collections-app-backend/internal/trash"
)

// testClock is the images service clock: tests move it past the sweeper's 1 h
// threshold instead of waiting.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// flakyStore fails every Delete while failDelete is set (a disk error after a
// replacement); everything else goes to the real local store.
type flakyStore struct {
	*storage.Local
	mu         sync.Mutex
	failDelete bool
}

func (f *flakyStore) setFail(v bool) { f.mu.Lock(); f.failDelete = v; f.mu.Unlock() }
func (f *flakyStore) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDelete {
		return errors.New("disk on fire")
	}
	return f.Local.Delete(key)
}

type imageEnv struct {
	*treeEnv
	dir   string
	clk   *testClock
	store *flakyStore
	svc   *images.Service
}

func newImageEnv(t *testing.T) *imageEnv {
	t.Helper()
	pool := testdb.NewMigrated(t, sharedPostgres(t))
	ws := uuid.New()
	if _, err := pool.Exec(t.Context(), "INSERT INTO workspaces (id, name) VALUES ($1, 'W')", ws); err != nil {
		t.Fatal(err)
	}
	if err := seed.BuiltinTypes(t.Context(), sqlc.New(pool), ws); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	local, err := storage.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	store := &flakyStore{Local: local}
	clk := &testClock{t: time.Now().UTC()}
	itemSvc := newItems(pool, clk)
	svc := images.NewService(images.NewPostgres(pool), store, itemSvc, clk, quietLogger())
	h := newRouter(t, httpapi.Deps{
		DB: pool, DataDir: dir, Logger: quietLogger(), WorkspaceID: ws,
		Items: itemSvc, Images: svc,
		Search: search.New(pool, ws), Trash: trash.New(pool, store, clk, quietLogger(), ws),
	})
	return &imageEnv{treeEnv: &treeEnv{t: t, h: h, pool: pool, ws: ws}, dir: dir, clk: clk, store: store, svc: svc}
}

// ---- fixtures ----

func pngFixture(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, 0, color.RGBA{R: 200, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegFixture(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// webpFixture is a 1x1 lossless WebP (only decoders exist for WebP in Go).
var webpFixture = []byte{
	'R', 'I', 'F', 'F', 0x1a, 0, 0, 0, 'W', 'E', 'B', 'P', 'V', 'P', '8', 'L', 0x0d, 0, 0, 0,
	0x2f, 0, 0, 0, 0x10, 0x07, 0x10, 0x11, 0x11, 0x88, 0x88, 0xfe, 0x07, 0x00,
}

// ---- helpers ----

func multipartBody(t *testing.T, field, filename string, data []byte) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, filename))
	h.Set("Content-Type", "application/octet-stream")
	part, err := w.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(data)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String(), w.FormDataContentType()
}

// upload sends a multipart PUT with the item's current ETag.
func (e *imageEnv) upload(it obj, filename string, data []byte) obj {
	e.t.Helper()
	rec := e.uploadRaw(it, "file", filename, data, etagOf("i", it))
	wantStatus(e.t, rec, 200)
	out := decode(e.t, rec)
	if rec.Header().Get("ETag") != etagOf("i", out) {
		e.t.Fatalf("ETag %q does not match version %v", rec.Header().Get("ETag"), out["version"])
	}
	return out
}

func (e *imageEnv) uploadRaw(it obj, field, filename string, data []byte, ifMatch string) *httptest.ResponseRecorder {
	e.t.Helper()
	body, ct := multipartBody(e.t, field, filename, data)
	r := req{method: "PUT", path: "/api/v1/items/" + it["id"].(string) + "/image", body: body, contentType: ct}
	if ifMatch != "" {
		r.headers = map[string]string{"If-Match": ifMatch}
	}
	return do(e.t, e.h, r)
}

func (e *imageEnv) setURL(it obj, url string) *httptest.ResponseRecorder {
	e.t.Helper()
	b := fmt.Sprintf(`{"url":%q}`, url)
	return e.call("PUT", "/api/v1/items/"+it["id"].(string)+"/image", b, etagOf("i", it))
}

func (e *imageEnv) deleteImage(it obj) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.call("DELETE", "/api/v1/items/"+it["id"].(string)+"/image", "", etagOf("i", it))
}

func (e *imageEnv) newItemInNewList(title string) obj {
	e.t.Helper()
	l := e.list("L", nil)
	return e.titled(l["id"].(string), title)
}

func imageOf(t *testing.T, it obj) obj {
	t.Helper()
	img, ok := it["image"].(map[string]any)
	if !ok {
		t.Fatalf("item has no image: %v", it["image"])
	}
	return img
}

func attachmentIDOf(t *testing.T, it obj) string { return imageOf(t, it)["attachment_id"].(string) }

func (e *imageEnv) attachmentCount() int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(e.t.Context(), "SELECT count(*) FROM attachments").Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *imageEnv) fileKeys() []string {
	e.t.Helper()
	var keys []string
	_ = filepath.WalkDir(filepath.Join(e.dir, "images"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(e.dir, p)
			keys = append(keys, filepath.ToSlash(rel))
		}
		return nil
	})
	return keys
}

func (e *imageEnv) wantNoFiles() {
	e.t.Helper()
	if k := e.fileKeys(); len(k) != 0 {
		e.t.Fatalf("expected no stored files, found %d", len(k))
	}
}

// wantConsistent fails if an item points to a missing attachment row or a row's files are missing.
func (e *imageEnv) wantConsistent() {
	e.t.Helper()
	rows, err := e.pool.Query(e.t.Context(), `SELECT storage_key, thumbnail_storage_key FROM attachments`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k1, k2 string
		if err := rows.Scan(&k1, &k2); err != nil {
			e.t.Fatal(err)
		}
		for _, k := range []string{k1, k2} {
			if _, err := os.Stat(filepath.Join(e.dir, k)); err != nil {
				e.t.Fatalf("attachment row without its file: %v", err)
			}
		}
	}
	var dangling int
	if err := e.pool.QueryRow(e.t.Context(), `SELECT count(*) FROM items i WHERE i.image_attachment_id IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM attachments a WHERE a.id = i.image_attachment_id)`).Scan(&dangling); err != nil {
		e.t.Fatal(err)
	}
	if dangling != 0 {
		e.t.Fatal("item points to a missing attachment")
	}
}

var generatedKey = regexp.MustCompile(`^images/[0-9a-f]{2}/[0-9a-f]{2}/[0-9a-f-]{36}\.(png|jpg|webp|thumb\.jpg)$`)

// ---- AT-06 ----

func TestAT06_UploadFormats(t *testing.T) {
	e := newImageEnv(t)
	tests := []struct {
		name, filename, mediaType string
		data                      []byte
		w, h                      int
	}{
		{"jpeg", "cover.jpg", "image/jpeg", jpegFixture(t, 80, 40), 80, 40},
		{"png", "cover.png", "image/png", pngFixture(t, 30, 30), 30, 30},
		{"webp", "cover.webp", "image/webp", webpFixture, 1, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			it := e.newItemInNewList(tc.name)
			got := e.upload(it, tc.filename, tc.data)
			if got["version"].(float64) != it["version"].(float64)+1 {
				t.Fatalf("version %v -> %v", it["version"], got["version"])
			}
			img := imageOf(t, got)
			id := img["attachment_id"].(string)
			if img["source"] != "upload" || img["url"] != "/api/v1/attachments/"+id+"/file" ||
				img["thumbnail_url"] != "/api/v1/attachments/"+id+"/thumbnail" ||
				img["width"] != float64(tc.w) || img["height"] != float64(tc.h) || img["original_filename"] != tc.filename {
				t.Fatalf("image = %v", img)
			}

			rec := e.call("GET", "/api/v1/attachments/"+id+"/file", "", "")
			wantStatus(t, rec, 200)
			if !bytes.Equal(rec.Body.Bytes(), tc.data) || rec.Header().Get("Content-Type") != tc.mediaType {
				t.Fatalf("file: type %q, %d bytes", rec.Header().Get("Content-Type"), rec.Body.Len())
			}
			rec = e.call("GET", "/api/v1/attachments/"+id+"/thumbnail", "", "")
			wantStatus(t, rec, 200)
			if _, err := jpeg.Decode(rec.Body); err != nil || rec.Header().Get("Content-Type") != "image/jpeg" {
				t.Fatalf("thumbnail: %v, type %q", err, rec.Header().Get("Content-Type"))
			}
			// The same image shows up on a fresh GET of the item and in the list summary.
			if again := e.fetchItem(it["id"]); imageOf(t, again)["attachment_id"] != id {
				t.Fatalf("GET item image = %v", again["image"])
			}
		})
	}
	e.wantConsistent()
}

func TestAT06_UploadRejections(t *testing.T) {
	e := newImageEnv(t)
	it := e.newItemInNewList("Rejected")
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"><script>alert(1)</script></svg>`)
	big := append(pngFixture(t, 4, 4), make([]byte, images.MaxUploadBytes)...) // PNG header + 10 MiB of padding
	// 8000x6000 = 48 MP: only a header is needed, DecodeConfig refuses it before any pixel is decoded.
	huge := pngHeader(8000, 6000)

	tests := []struct {
		name, filename string
		data           []byte
		status         int
		code           string
		field          string
	}{
		{"svg", "logo.svg", svg, 415, "unsupported_media_type", ""},
		{"svg named png", "logo.png", svg, 415, "unsupported_media_type", ""},
		{"text named jpg", "photo.jpg", []byte("just some text"), 415, "unsupported_media_type", ""},
		{"gif", "anim.gif", []byte("GIF89a\x01\x00\x01\x00"), 415, "unsupported_media_type", ""},
		{"corrupt png", "bad.png", pngFixture(t, 30, 30)[:50], 422, "validation_failed", "file"},
		{"over 10 MiB", "big.png", big, 413, "payload_too_large", ""},
		{"over 40 MP", "huge.png", huge, 422, "validation_failed", "file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.uploadRaw(it, "file", tc.filename, tc.data, etagOf("i", it))
			p := wantProblem(t, rec, tc.status, tc.code)
			if tc.field != "" && (len(p.Errors) != 1 || p.Errors[0].Field != tc.field) {
				t.Fatalf("errors = %+v", p.Errors)
			}
			if strings.Contains(rec.Body.String(), e.dir) {
				t.Fatal("the answer leaks a filesystem path")
			}
		})
	}

	// A PNG whose filename ends in .svg is a fine PNG: only the bytes count.
	e.upload(it, "sneaky.svg", pngFixture(t, 5, 5))

	t.Run("no file part", func(t *testing.T) {
		cur := e.fetchItem(it["id"])
		p := wantProblem(t, e.uploadRaw(cur, "other", "x.png", pngFixture(t, 5, 5), etagOf("i", cur)), 422, "validation_failed")
		if len(p.Errors) != 1 || p.Errors[0].Field != "file" {
			t.Fatalf("errors = %+v", p.Errors)
		}
	})
	t.Run("body over the route limit", func(t *testing.T) {
		cur := e.fetchItem(it["id"])
		wantProblem(t, e.uploadRaw(cur, "file", "x.png", make([]byte, images.MaxUploadBytes+128<<10), etagOf("i", cur)), 413, "payload_too_large")
	})

	// Only the one valid upload left traces; every rejection removed what it wrote (nothing was written).
	if e.attachmentCount() != 1 || len(e.fileKeys()) != 2 {
		t.Fatalf("attachments=%d files=%d", e.attachmentCount(), len(e.fileKeys()))
	}
	e.wantConsistent()
}

func TestAT06_UploadExactlyTenMiBIsAccepted(t *testing.T) {
	e := newImageEnv(t)
	it := e.newItemInNewList("Max")
	base := pngFixture(t, 4, 4)
	got := e.upload(it, "max.png", append(base, make([]byte, images.MaxUploadBytes-len(base))...))
	rec := e.call("GET", "/api/v1/attachments/"+attachmentIDOf(t, got)+"/file", "", "")
	wantStatus(t, rec, 200)
	if rec.Body.Len() != images.MaxUploadBytes {
		t.Fatalf("stored %d bytes", rec.Body.Len())
	}
}

func TestAT06_ReplaceDeletesOldFilesAfterCommit(t *testing.T) {
	e := newImageEnv(t)
	it := e.newItemInNewList("Replace")
	first := e.upload(it, "a.png", pngFixture(t, 10, 10))
	firstID := attachmentIDOf(t, first)
	if len(e.fileKeys()) != 2 {
		t.Fatalf("files = %v", e.fileKeys())
	}

	second := e.upload(first, "b.jpg", jpegFixture(t, 20, 20))
	secondID := attachmentIDOf(t, second)
	if secondID == firstID {
		t.Fatal("a replacement must get a new attachment id")
	}
	wantStatus(t, e.call("GET", "/api/v1/attachments/"+firstID+"/file", "", ""), 404)
	wantStatus(t, e.call("GET", "/api/v1/attachments/"+firstID+"/thumbnail", "", ""), 404)
	wantStatus(t, e.call("GET", "/api/v1/attachments/"+secondID+"/file", "", ""), 200)
	if e.attachmentCount() != 1 || len(e.fileKeys()) != 2 {
		t.Fatalf("after replace: attachments=%d files=%v", e.attachmentCount(), e.fileKeys())
	}
	for _, k := range e.fileKeys() {
		if !generatedKey.MatchString(k) {
			t.Fatalf("storage key %q is not generated", k)
		}
	}

	// Remove: clears the image, bumps the version, deletes row and files.
	rec := e.deleteImage(second)
	wantStatus(t, rec, 200)
	removed := decode(t, rec)
	if removed["image"] != nil || removed["version"].(float64) != second["version"].(float64)+1 || rec.Header().Get("ETag") != etagOf("i", removed) {
		t.Fatalf("after delete: %v / %q", removed, rec.Header().Get("ETag"))
	}
	if e.attachmentCount() != 0 {
		t.Fatal("attachment row left behind")
	}
	e.wantNoFiles()
	wantStatus(t, e.call("GET", "/api/v1/attachments/"+secondID+"/file", "", ""), 404)
}

func TestDeleteImageWithoutImageIsANoop(t *testing.T) {
	e := newImageEnv(t)
	it := e.newItemInNewList("Plain")
	rec := e.deleteImage(it)
	wantStatus(t, rec, 200)
	if got := decode(t, rec); got["version"] != it["version"] || got["image"] != nil {
		t.Fatalf("got %v", got)
	}
	wantProblem(t, e.call("DELETE", "/api/v1/items/"+it["id"].(string)+"/image", "", `"i-99"`), 412, "version_conflict")
}

// ---- AT-07 ----

func TestAT07_URLAndUploadReplaceEachOther(t *testing.T) {
	e := newImageEnv(t)
	it := e.newItemInNewList("Switch")

	up := e.upload(it, "a.png", pngFixture(t, 10, 10))
	upID := attachmentIDOf(t, up)

	rec := e.setURL(up, "https://image.tmdb.org/t/p/w500/matrix.jpg")
	wantStatus(t, rec, 200)
	viaURL := decode(t, rec)
	img := imageOf(t, viaURL)
	if img["source"] != "url" || img["url"] != "https://image.tmdb.org/t/p/w500/matrix.jpg" || img["attachment_id"] != nil {
		t.Fatalf("image = %v", img)
	}
	if viaURL["version"].(float64) != up["version"].(float64)+1 || rec.Header().Get("ETag") != etagOf("i", viaURL) {
		t.Fatalf("version/ETag: %v %q", viaURL["version"], rec.Header().Get("ETag"))
	}
	wantStatus(t, e.call("GET", "/api/v1/attachments/"+upID+"/file", "", ""), 404)
	if e.attachmentCount() != 0 {
		t.Fatal("upload not removed when the URL was set")
	}
	e.wantNoFiles()

	again := e.upload(viaURL, "b.png", pngFixture(t, 12, 12))
	if imageOf(t, again)["source"] != "upload" {
		t.Fatalf("image = %v", again["image"])
	}
	var url *string
	if err := e.pool.QueryRow(t.Context(), "SELECT image_url FROM items WHERE id = $1", again["id"]).Scan(&url); err != nil {
		t.Fatal(err)
	}
	if url != nil {
		t.Fatalf("image_url must be cleared by an upload, got %q", *url)
	}
	e.wantConsistent()
}

func TestSetURLValidationAndNoop(t *testing.T) {
	e := newImageEnv(t)
	it := e.newItemInNewList("URL")
	for _, bad := range []string{"javascript:alert(1)", "ftp://example.com/a.jpg", "/relative.jpg", "https://", "not a url", "", "https://example.com/" + strings.Repeat("a", 2048)} {
		p := wantProblem(t, e.setURL(it, bad), 422, "validation_failed")
		if len(p.Errors) != 1 || p.Errors[0].Field != "url" {
			t.Fatalf("%.30q: errors = %+v", bad, p.Errors)
		}
	}
	set := decode(t, e.setURL(it, " http://example.com/a.jpg "))
	if imageOf(t, set)["url"] != "http://example.com/a.jpg" {
		t.Fatalf("not trimmed: %v", set["image"])
	}
	rec := e.setURL(set, "http://example.com/a.jpg")
	wantStatus(t, rec, 200)
	if got := decode(t, rec); got["version"] != set["version"] {
		t.Fatalf("the same URL must not bump the version: %v -> %v", set["version"], got["version"])
	}
	wantProblem(t, e.call("PUT", "/api/v1/items/"+it["id"].(string)+"/image", `{"url":"https://a.b/c.jpg","extra":1}`, etagOf("i", set)), 400, "unknown_field")
}

// ---- served files, preconditions, trash ----

func TestServedFileHeaders(t *testing.T) {
	e := newImageEnv(t)
	it := e.newItemInNewList("Headers")
	id := attachmentIDOf(t, e.upload(it, "a.png", pngFixture(t, 900, 300)))
	for _, tc := range []struct{ path, mediaType string }{
		{"/api/v1/attachments/" + id + "/file", "image/png"},
		{"/api/v1/attachments/" + id + "/thumbnail", "image/jpeg"},
	} {
		rec := e.call("GET", tc.path, "", "")
		wantStatus(t, rec, 200)
		want := map[string]string{
			"Content-Type":            tc.mediaType,
			"X-Content-Type-Options":  "nosniff",
			"Content-Disposition":     "inline",
			"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
			"Cache-Control":           "private, max-age=31536000, immutable",
		}
		for k, v := range want {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s %s = %q, want %q", tc.path, k, got, v)
			}
		}
		if rec.Header().Get("Content-Length") != fmt.Sprint(rec.Body.Len()) {
			t.Errorf("Content-Length %q for %d bytes", rec.Header().Get("Content-Length"), rec.Body.Len())
		}
	}
	cfg, _ := jpeg.DecodeConfig(bytes.NewReader(e.call("GET", "/api/v1/attachments/"+id+"/thumbnail", "", "").Body.Bytes()))
	if cfg.Width != 640 || cfg.Height != 213 {
		t.Fatalf("thumbnail %dx%d", cfg.Width, cfg.Height)
	}
	wantProblem(t, e.call("GET", "/api/v1/attachments/"+uuid.NewString()+"/file", "", ""), 404, "not_found")
	wantProblem(t, e.call("GET", "/api/v1/attachments/nope/thumbnail", "", ""), 400, "invalid_parameter")
}

func TestImageOpsNeedMatchingIfMatch(t *testing.T) {
	e := newImageEnv(t)
	it := e.newItemInNewList("Match")
	data := pngFixture(t, 6, 6)

	wantProblem(t, e.uploadRaw(it, "file", "a.png", data, ""), 428, "precondition_required")
	wantProblem(t, e.uploadRaw(it, "file", "a.png", data, `"c-1"`), 428, "precondition_required") // another resource's ETag
	p := wantProblem(t, e.uploadRaw(it, "file", "a.png", data, `"i-99"`), 412, "version_conflict")
	if p.CurrentVersion == nil || *p.CurrentVersion != int(it["version"].(float64)) {
		t.Fatalf("current_version = %v", p.CurrentVersion)
	}
	wantProblem(t, e.call("PUT", "/api/v1/items/"+it["id"].(string)+"/image", `{"url":"https://a.b/c.jpg"}`, ""), 428, "precondition_required")
	wantProblem(t, e.call("PUT", "/api/v1/items/"+it["id"].(string)+"/image", `{"url":"https://a.b/c.jpg"}`, `"i-99"`), 412, "version_conflict")
	wantProblem(t, e.call("DELETE", "/api/v1/items/"+it["id"].(string)+"/image", "", ""), 428, "precondition_required")

	// A stale upload leaves nothing behind: the files written before the transaction are removed.
	e.wantNoFiles()
	if e.attachmentCount() != 0 {
		t.Fatal("attachment row created by a rejected upload")
	}
}

func TestImageOnMissingOrTrashedItem(t *testing.T) {
	e := newImageEnv(t)
	it := e.newItemInNewList("Trashed")
	withImage := e.upload(it, "a.png", pngFixture(t, 6, 6))
	id := attachmentIDOf(t, withImage)

	e.exec(`UPDATE items SET deleted_at = now(), trash_batch_id = $2 WHERE id = $1`, it["id"], e.trashBatch("item", uuid.MustParse(it["id"].(string))))

	wantProblem(t, e.uploadRaw(withImage, "file", "b.png", pngFixture(t, 7, 7), etagOf("i", withImage)), 404, "not_found")
	wantProblem(t, e.setURL(withImage, "https://a.b/c.jpg"), 404, "not_found")
	wantProblem(t, e.deleteImage(withImage), 404, "not_found")
	ghost := obj{"id": uuid.NewString(), "version": float64(1)}
	wantProblem(t, e.uploadRaw(ghost, "file", "b.png", pngFixture(t, 7, 7), `"i-1"`), 404, "not_found")

	// Trash keeps files: the image of a trashed item is still served, and the rejected upload left nothing.
	wantStatus(t, e.call("GET", "/api/v1/attachments/"+id+"/file", "", ""), 200)
	wantStatus(t, e.call("GET", "/api/v1/attachments/"+id+"/thumbnail", "", ""), 200)
	if e.attachmentCount() != 1 || len(e.fileKeys()) != 2 {
		t.Fatalf("attachments=%d files=%v", e.attachmentCount(), e.fileKeys())
	}
}

// ---- AT-20: orphans and the sweeper ----

func TestAT20_SweepsOrphanFilesAfterACrash(t *testing.T) {
	e := newImageEnv(t)
	it := e.newItemInNewList("Crash")
	kept := e.upload(it, "ok.png", pngFixture(t, 10, 10))

	// A crash between "files written" and "transaction committed": files with no row.
	for _, k := range []string{"images/ab/cd/" + uuid.NewString() + ".png", "images/ab/cd/" + uuid.NewString() + ".thumb.jpg", "images/ab/cd/.tmp-" + uuid.NewString()} {
		if _, err := e.store.Put(k, strings.NewReader("half an upload")); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.fileKeys()) != 5 {
		t.Fatalf("files = %v", e.fileKeys())
	}

	// Recent files may belong to an upload still in flight: untouched.
	res, err := e.svc.Sweep(scope.With(t.Context(), e.ws))
	if err != nil || res.Files != 0 || res.Attachments != 0 || len(e.fileKeys()) != 5 {
		t.Fatalf("early sweep = %+v, %v, files=%d", res, err, len(e.fileKeys()))
	}

	e.clk.Advance(2 * time.Hour)
	res, err = e.svc.Sweep(scope.With(t.Context(), e.ws))
	if err != nil || res.Files != 3 || res.Attachments != 0 {
		t.Fatalf("sweep = %+v, %v", res, err)
	}
	if len(e.fileKeys()) != 2 {
		t.Fatalf("files = %v", e.fileKeys())
	}
	wantStatus(t, e.call("GET", "/api/v1/attachments/"+attachmentIDOf(t, kept)+"/file", "", ""), 200)
	e.wantConsistent()

	res, err = e.svc.Sweep(scope.With(t.Context(), e.ws))
	if err != nil || res != (images.SweepResult{}) {
		t.Fatalf("second sweep = %+v, %v", res, err)
	}
}

func TestAT20_FailedDeleteAfterReplaceIsRetriedBySweeper(t *testing.T) {
	e := newImageEnv(t)
	it := e.newItemInNewList("Retry")
	first := e.upload(it, "a.png", pngFixture(t, 10, 10))

	e.store.setFail(true)
	second := e.upload(first, "b.png", pngFixture(t, 12, 12)) // succeeds even though the old files cannot be deleted
	if e.attachmentCount() != 1 || len(e.fileKeys()) != 4 {
		t.Fatalf("attachments=%d files=%d", e.attachmentCount(), len(e.fileKeys()))
	}
	e.wantConsistent() // never a dangling reference, only orphan files

	e.clk.Advance(2 * time.Hour)
	if res, err := e.svc.Sweep(scope.With(t.Context(), e.ws)); err == nil || res.Files != 0 {
		t.Fatalf("sweep with a broken disk = %+v, %v", res, err)
	}
	if len(e.fileKeys()) != 4 {
		t.Fatal("files must stay until the delete works")
	}

	e.store.setFail(false)
	res, err := e.svc.Sweep(scope.With(t.Context(), e.ws))
	if err != nil || res.Files != 2 {
		t.Fatalf("retry sweep = %+v, %v", res, err)
	}
	if len(e.fileKeys()) != 2 {
		t.Fatalf("files = %v", e.fileKeys())
	}
	wantStatus(t, e.call("GET", "/api/v1/attachments/"+attachmentIDOf(t, second)+"/file", "", ""), 200)
	e.wantConsistent()
}

func TestAT20_SweepsUnreferencedAttachmentRows(t *testing.T) {
	e := newImageEnv(t)
	it := e.newItemInNewList("Rows")
	kept := e.upload(it, "ok.png", pngFixture(t, 10, 10))

	// An attachment no item points to (say its item was purged), with real files.
	orphan := uuid.New()
	key, thumb := fmt.Sprintf("images/%s/%s/%s.png", orphan.String()[:2], orphan.String()[2:4], orphan), fmt.Sprintf("images/%s/%s/%s.thumb.jpg", orphan.String()[:2], orphan.String()[2:4], orphan)
	for _, k := range []string{key, thumb} {
		if _, err := e.store.Put(k, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	e.exec(`INSERT INTO attachments (id, workspace_id, storage_key, thumbnail_storage_key, media_type, size_bytes, width, height, sha256, created_at)
		VALUES ($1, $2, $3, $4, 'image/png', 1, 1, 1, '\x00', $5)`, orphan, e.ws, key, thumb, e.clk.Now())

	// Too young: it may belong to an upload whose item write is about to commit.
	if res, err := e.svc.Sweep(scope.With(t.Context(), e.ws)); err != nil || res != (images.SweepResult{}) {
		t.Fatalf("early sweep = %+v, %v", res, err)
	}
	e.clk.Advance(2 * time.Hour)
	res, err := e.svc.Sweep(scope.With(t.Context(), e.ws))
	if err != nil || res.Attachments != 1 || res.Files != 0 {
		t.Fatalf("sweep = %+v, %v", res, err)
	}
	if e.attachmentCount() != 1 || len(e.fileKeys()) != 2 {
		t.Fatalf("attachments=%d files=%v", e.attachmentCount(), e.fileKeys())
	}
	wantStatus(t, e.call("GET", "/api/v1/attachments/"+attachmentIDOf(t, kept)+"/file", "", ""), 200)
	wantStatus(t, e.call("GET", "/api/v1/attachments/"+orphan.String()+"/file", "", ""), 404)
	e.wantConsistent()
}

func TestAT20_ServedRowWithMissingFileIs404(t *testing.T) {
	e := newImageEnv(t)
	it := e.newItemInNewList("Gone")
	id := attachmentIDOf(t, e.upload(it, "a.png", pngFixture(t, 10, 10)))
	for _, k := range e.fileKeys() {
		_ = os.Remove(filepath.Join(e.dir, k))
	}
	wantProblem(t, e.call("GET", "/api/v1/attachments/"+id+"/file", "", ""), 404, "not_found")
}

func TestRunSweeperStopsWhenContextIsCancelled(t *testing.T) {
	e := newImageEnv(t)
	ctx, cancel := context.WithCancel(scope.With(t.Context(), e.ws))
	done := make(chan struct{})
	go func() { e.svc.RunSweeper(ctx, 5*time.Millisecond); close(done) }()
	time.Sleep(30 * time.Millisecond) // a few ticks
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sweeper did not stop")
	}
}

// pngHeader is a header-only PNG that claims w x h pixels.
func pngHeader(w, h uint32) []byte {
	var buf bytes.Buffer
	_, _ = io.WriteString(&buf, "\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		_ = binary.Write(&buf, binary.BigEndian, uint32(len(data)))
		_, _ = buf.WriteString(typ)
		_, _ = buf.Write(data)
		_ = binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	}
	ihdr := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, w), h)
	chunk("IHDR", append(ihdr, 8, 2, 0, 0, 0))
	chunk("IDAT", nil)
	return buf.Bytes()
}
