package lookup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
)

const (
	expiryBatchSize  = 100
	refreshBatchSize = 25
	refreshPace      = 100 * time.Millisecond
)

// RefreshFunc refreshes one still-eligible imported item at the version read
// by the maintenance pass.
type RefreshFunc func(ctx context.Context, itemID uuid.UUID, version int) error

// MaintenanceResult counts one retention pass.
type MaintenanceResult struct {
	Expired   int
	Refreshed int
	Failed    int
}

// Maintain applies the TMDB retention (D-089); no other provider has one (D-095),
// and its queries only see provider 'tmdb' rows. It first removes every expired owned value, then attempts one bounded
// batch of due refreshes. Expiry never waits behind provider calls.
func (s *DBStore) Maintain(ctx context.Context, refresh RefreshFunc) (MaintenanceResult, error) {
	var result MaintenanceResult
	for {
		now := s.clock.Now()
		ids, err := sqlc.New(s.db).ListExpiredTMDBImportIDs(ctx, sqlc.ListExpiredTMDBImportIDsParams{
			WorkspaceID: s.workspaceID, Now: &now, BatchLimit: expiryBatchSize,
		})
		if err != nil {
			return result, fmt.Errorf("list expired TMDB imports: %w", err)
		}
		if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			changed, err := s.expireOne(ctx, id)
			if err != nil {
				return result, err
			}
			if changed {
				result.Expired++
			}
		}
	}

	now := s.clock.Now()
	due, err := sqlc.New(s.db).ListDueTMDBImports(ctx, sqlc.ListDueTMDBImportsParams{
		WorkspaceID: s.workspaceID, Now: &now, BatchLimit: refreshBatchSize,
	})
	if err != nil {
		return result, fmt.Errorf("list due TMDB imports: %w", err)
	}
	for i, row := range due {
		if !eligibleForRefresh(row) {
			continue
		}
		if i > 0 {
			timer := time.NewTimer(refreshPace)
			select {
			case <-ctx.Done():
				timer.Stop()
				return result, ctx.Err()
			case <-timer.C:
			}
		}
		if err := refresh(ctx, row.ItemID, int(row.ItemVersion)); err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			result.Failed++
			s.log.WarnContext(ctx, "TMDB maintenance refresh failed; will retry", "item_id", row.ItemID)
			continue
		}
		result.Refreshed++
	}
	return result, nil
}

// Run waits for each interval and runs maintenance until ctx is cancelled.
// Startup maintenance is deliberately separate so cmd/api can finish it
// before accepting requests.
func (s *DBStore) Run(ctx context.Context, interval time.Duration, refresh RefreshFunc) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := s.Maintain(ctx, refresh)
			if err != nil {
				if ctx.Err() == nil {
					s.log.WarnContext(ctx, "TMDB maintenance failed; will retry")
				}
				continue
			}
			if result.Expired > 0 || result.Refreshed > 0 || result.Failed > 0 {
				s.log.InfoContext(ctx, "TMDB maintenance finished",
					"expired", result.Expired, "refreshed", result.Refreshed, "failed", result.Failed)
			}
		}
	}
}

func eligibleForRefresh(row sqlc.ListDueTMDBImportsRow) bool {
	if row.DeletedAt != nil || typeKey(row.CurrentType) != row.Type {
		return false
	}
	metadata := map[string]any{}
	if json.Unmarshal(row.Metadata, &metadata) != nil {
		return false
	}
	if disabled, _ := metadata["lookup_disabled"].(bool); disabled {
		return false
	}
	id, ok := metadata["tmdb_id"].(float64)
	return ok && strconv.FormatFloat(id, 'f', -1, 64) == row.SourceID
}

func (s *DBStore) expireOne(ctx context.Context, itemID uuid.UUID) (bool, error) {
	now := s.clock.Now()
	var oldFiles []string
	changed := false
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := q.LockTree(ctx, s.workspaceID); err != nil {
			return fmt.Errorf("lock tree for TMDB expiry: %w", err)
		}
		cur, err := q.LockTMDBImportItem(ctx, sqlc.LockTMDBImportItemParams{ID: itemID, WorkspaceID: s.workspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock expired TMDB import: %w", err)
		}
		record, err := q.GetLookupImport(ctx, sqlc.GetLookupImportParams{ItemID: itemID, WorkspaceID: s.workspaceID})
		if err != nil {
			return fmt.Errorf("get expired TMDB import: %w", err)
		}
		if record.ExpiresAt == nil {
			return nil // no retention deadline: not a TMDB import
		}
		metadataExpired := !record.ExpiresAt.After(now)
		posterExpired := record.PosterExpiresAt != nil && !record.PosterExpiresAt.After(now)
		if !metadataExpired && !posterExpired {
			return nil
		}

		metadata := map[string]any{}
		if err := json.Unmarshal(cur.Metadata, &metadata); err != nil {
			return fmt.Errorf("decode expired item metadata: %w", err)
		}
		if metadataExpired {
			snapshots := map[string]any{}
			if err := json.Unmarshal(cur.ImportedFields, &snapshots); err != nil {
				return fmt.Errorf("decode expired imported fields: %w", err)
			}
			removeUnchangedOwned(metadata, snapshots)
		}
		metaJSON, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("encode expired item metadata: %w", err)
		}
		title := cur.Title
		if metadataExpired && cur.ImportedTitle != nil && title == *cur.ImportedTitle {
			title = cur.OriginalTitle
		}
		attachmentID := cur.ImageAttachmentID
		imageURL := cur.ImageUrl
		removePoster := posterExpired && cur.ImportedAttachmentID.Valid &&
			attachmentID.Valid && attachmentID.UUID == cur.ImportedAttachmentID.UUID
		if removePoster {
			attachmentID = uuid.NullUUID{}
			imageURL = nil
		}
		visible := title != cur.Title || !sameJSON(metaJSON, cur.Metadata) ||
			attachmentID != cur.ImageAttachmentID || imageURLChanged(imageURL, cur.ImageUrl)
		if visible {
			if err := q.UpdateLookupItem(ctx, sqlc.UpdateLookupItemParams{
				Title: title, Metadata: metaJSON, ImageUrl: imageURL, ImageAttachmentID: attachmentID,
				Now: now, ID: itemID, WorkspaceID: s.workspaceID,
			}); err != nil {
				return fmt.Errorf("expire TMDB item values: %w", err)
			}
		}
		if removePoster {
			old, err := q.DeleteAttachment(ctx, sqlc.DeleteAttachmentParams{ID: cur.ImportedAttachmentID.UUID, WorkspaceID: s.workspaceID})
			if err != nil {
				return fmt.Errorf("delete expired TMDB attachment: %w", err)
			}
			oldFiles = []string{old.StorageKey, old.ThumbnailStorageKey}
		}
		if metadataExpired && (posterExpired || record.PosterExpiresAt == nil) {
			if _, err := q.DeleteLookupImport(ctx, sqlc.DeleteLookupImportParams{ItemID: itemID, WorkspaceID: s.workspaceID}); err != nil {
				return fmt.Errorf("delete expired TMDB provenance: %w", err)
			}
		} else if metadataExpired {
			if err := q.RetainTMDBPosterAfterMetadataExpiry(ctx, sqlc.RetainTMDBPosterAfterMetadataExpiryParams{ItemID: itemID, WorkspaceID: s.workspaceID}); err != nil {
				return fmt.Errorf("retain TMDB poster provenance after metadata expiry: %w", err)
			}
		} else if posterExpired {
			if err := q.ClearTMDBPosterProvenance(ctx, sqlc.ClearTMDBPosterProvenanceParams{ItemID: itemID, WorkspaceID: s.workspaceID}); err != nil {
				return fmt.Errorf("clear expired TMDB poster provenance: %w", err)
			}
		}
		changed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	s.deleteFiles(context.WithoutCancel(ctx), oldFiles...)
	return changed, nil
}
