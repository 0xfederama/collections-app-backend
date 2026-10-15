package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/storage"
)

// BackfillResult counts what Backfill did to the stored originals.
type BackfillResult struct {
	Total        int // attachments looked at
	Stripped     int // files rewritten without metadata
	AlreadyClean int // files that had no metadata (their size and hash were still checked)
	Skipped      int // missing, unparseable or oversize files, left untouched
	Failed       int // I/O or database errors
}

// Backfill removes the metadata (see Strip) from the originals stored before
// uploads were stripped. It handles one attachment at a time and goes on after
// an error on a single one. It never deletes a file and never touches one it
// cannot parse. It is idempotent: a second run strips nothing.
//
// A file is replaced atomically (FileStore.Put writes a temporary file and
// renames it), then its size_bytes and sha256 are updated in one statement. A
// crash between the two leaves a stale size and hash, which the next run fixes:
// the file then reads as already clean and the row is brought in line.
// Width and height stay as they are, since Strip keeps the orientation.
func Backfill(ctx context.Context, db sqlc.DBTX, store storage.FileStore, log *slog.Logger, workspaceID uuid.UUID) (BackfillResult, error) {
	var res BackfillResult
	q := sqlc.New(db)
	rows, err := q.ListAttachmentsForStrip(ctx, workspaceID)
	if err != nil {
		return res, fmt.Errorf("list attachments: %w", err)
	}
	for _, a := range rows {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		res.Total++
		switch outcome, err := backfillOne(ctx, q, store, workspaceID, a); outcome {
		case outcomeStripped:
			res.Stripped++
		case outcomeClean:
			res.AlreadyClean++
		case outcomeSkipped:
			res.Skipped++
			log.Warn("strip-images: skipped", "attachment", a.ID, "key", a.StorageKey, "reason", err.Error())
		default:
			res.Failed++
			log.Error("strip-images: failed", "attachment", a.ID, "key", a.StorageKey, "error", err)
		}
	}
	return res, nil
}

type backfillOutcome int

const (
	outcomeFailed backfillOutcome = iota
	outcomeStripped
	outcomeClean
	outcomeSkipped
)

func backfillOne(ctx context.Context, q *sqlc.Queries, store storage.FileStore, workspaceID uuid.UUID, a sqlc.ListAttachmentsForStripRow) (backfillOutcome, error) {
	rc, _, err := store.Open(a.StorageKey)
	if errors.Is(err, storage.ErrNotFound) {
		return outcomeSkipped, errors.New("file is missing")
	}
	if err != nil {
		return outcomeFailed, err
	}
	data, err := io.ReadAll(io.LimitReader(rc, MaxUploadBytes+1))
	_ = rc.Close()
	if err != nil {
		return outcomeFailed, fmt.Errorf("read file: %w", err)
	}
	if len(data) > MaxUploadBytes {
		return outcomeSkipped, errors.New("file is over the upload limit")
	}
	clean, err := Strip(data, a.MediaType)
	if err != nil {
		return outcomeSkipped, err
	}
	// Never replace a file with something that does not look like the same picture.
	before, _, errBefore := image.DecodeConfig(bytes.NewReader(data))
	after, _, errAfter := image.DecodeConfig(bytes.NewReader(clean))
	if errBefore != nil || errAfter != nil || before != after {
		return outcomeSkipped, errors.New("stripped file would not decode like the original")
	}

	outcome := outcomeClean
	if !bytes.Equal(clean, data) {
		if _, err := store.Put(a.StorageKey, bytes.NewReader(clean)); err != nil {
			return outcomeFailed, fmt.Errorf("write file: %w", err)
		}
		outcome = outcomeStripped
	}
	sum := sha256.Sum256(clean)
	if outcome == outcomeClean && a.SizeBytes == int64(len(clean)) && bytes.Equal(a.Sha256, sum[:]) {
		return outcome, nil
	}
	n, err := q.UpdateAttachmentFile(ctx, sqlc.UpdateAttachmentFileParams{
		SizeBytes: int64(len(clean)), Sha256: sum[:], ID: a.ID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return outcomeFailed, fmt.Errorf("update attachment: %w", err)
	}
	if n != 1 {
		return outcomeFailed, errors.New("attachment row is gone")
	}
	return outcome, nil
}
