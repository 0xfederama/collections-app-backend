package lookup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/database"
	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/tmdb"
)

const (
	// backfillMaxCalls bounds the TMDB calls of one pass; the rest waits for the next startup.
	backfillMaxCalls = 500
	backfillBatch    = 50
	// backfillMaxConsecutiveErrors ends the pass early when TMDB looks down.
	backfillMaxConsecutiveErrors = 5
	// noLinkMarkerPrefix prefixes the imported-fields value that records "checked, TMDB
	// has no JustWatch link for this country".
	noLinkMarkerPrefix = "none:"
)

// LinkFetcher is the slice of the TMDB client the backfill needs (*tmdb.Client).
type LinkFetcher interface {
	Enabled() bool
	WatchLink(ctx context.Context, typ string, id int, country string) (string, error)
}

// BackfillResult counts one streaming link backfill pass.
type BackfillResult struct {
	Scanned int // candidates for which a TMDB call was made
	Updated int // links written
	NoLink  int // checked, no link for the country (marker recorded)
	Skipped int // candidate changed or disappeared before the write
	Errors  int // transient failures, left unmarked for the next startup
}

// RunStreamingLinkBackfill runs one backfill pass and logs it. It never panics
// and never returns an error: it is meant to run in a goroutine started at boot.
// It does nothing without a TMDB key or a streaming country.
func (s *DBStore) RunStreamingLinkBackfill(ctx context.Context, fetcher LinkFetcher, settings SettingsReader) {
	defer func() {
		if r := recover(); r != nil {
			s.log.WarnContext(ctx, "streaming link backfill panicked; it will retry at next startup", "panic", fmt.Sprint(r))
		}
	}()
	if fetcher == nil || !fetcher.Enabled() {
		s.log.DebugContext(ctx, "streaming link backfill skipped: TMDB lookup is disabled")
		return
	}
	cfg, err := settings.Get(ctx, s.workspaceID)
	if err != nil {
		if ctx.Err() == nil {
			s.log.WarnContext(ctx, "streaming link backfill: reading settings failed; it will retry at next startup")
		}
		return
	}
	if cfg.StreamingCountryCode == "" {
		s.log.DebugContext(ctx, "streaming link backfill skipped: no streaming country")
		return
	}
	res, err := s.BackfillStreamingLinks(ctx, fetcher, cfg.StreamingCountryCode)
	if err != nil && ctx.Err() == nil {
		s.log.WarnContext(ctx, "streaming link backfill stopped by an error; it will retry at next startup", "err", err)
	}
	if res.Scanned == 0 && res.Skipped == 0 && res.Errors == 0 {
		s.log.DebugContext(ctx, "streaming link backfill: nothing to do")
		return
	}
	s.log.InfoContext(ctx, "streaming link backfill", "scanned", res.Scanned, "updated", res.Updated,
		"no_link", res.NoLink, "skipped", res.Skipped, "errors", res.Errors)
}

// BackfillStreamingLinks fills `streaming.link` for movie/TV items looked up
// before api 0.14.0 (one TMDB watch/providers call each, at most backfillMaxCalls
// per pass). It writes only that key and its imported-fields entry, with no
// item version/updated_at change (derived data, like the country-change clear).
// "No link" and a missing title (404) are recorded as a `none:<COUNTRY>`
// snapshot marker so they are not asked again; other failures leave the item
// untouched. A 429 or a disabled client ends the pass. Trashed items are not
// candidates (like refresh in Maintain): they are picked up after a restore.
func (s *DBStore) BackfillStreamingLinks(ctx context.Context, fetcher LinkFetcher, country string) (BackfillResult, error) {
	var res BackfillResult
	country = strings.ToUpper(strings.TrimSpace(country))
	after := uuid.Nil
	consecutive := 0
	for res.Scanned+res.Errors < backfillMaxCalls {
		rows, err := sqlc.New(s.db).ListStreamingLinkBackfillCandidates(ctx, sqlc.ListStreamingLinkBackfillCandidatesParams{
			WorkspaceID: s.workspaceID, AfterID: after, Country: country, BatchLimit: backfillBatch,
		})
		if err != nil {
			return res, fmt.Errorf("list streaming link backfill candidates: %w", err)
		}
		if len(rows) == 0 {
			return res, nil
		}
		for _, row := range rows {
			after = row.ItemID
			if res.Scanned+res.Errors >= backfillMaxCalls {
				return res, nil
			}
			id, convErr := strconv.Atoi(row.SourceID)
			if convErr != nil {
				res.Skipped++
				continue
			}
			link, err := fetcher.WatchLink(ctx, row.Type, id, country)
			switch {
			case err == nil, errors.Is(err, tmdb.ErrNotFound):
				if errors.Is(err, tmdb.ErrNotFound) {
					link = ""
				}
				consecutive = 0
				res.Scanned++
				outcome, werr := s.recordStreamingLink(ctx, row.ItemID, row.SourceID, country, link)
				if werr != nil {
					return res, werr
				}
				switch outcome {
				case backfillUpdated:
					res.Updated++
				case backfillNoLink:
					res.NoLink++
				default:
					res.Skipped++
				}
			case ctx.Err() != nil:
				return res, ctx.Err()
			case errors.Is(err, tmdb.ErrRateLimited), errors.Is(err, tmdb.ErrDisabled):
				return res, nil
			default:
				res.Errors++
				consecutive++
				if consecutive >= backfillMaxConsecutiveErrors {
					return res, nil
				}
			}
		}
	}
	return res, nil
}

type backfillOutcome int

const (
	backfillSkipped backfillOutcome = iota
	backfillUpdated
	backfillNoLink
)

// recordStreamingLink re-checks the candidate under the tree and item locks (it may
// have changed during the TMDB call) and writes the link or the "none" marker.
func (s *DBStore) recordStreamingLink(ctx context.Context, itemID uuid.UUID, sourceID, country, link string) (backfillOutcome, error) {
	outcome := backfillSkipped
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := q.LockTree(ctx, s.workspaceID); err != nil {
			return fmt.Errorf("lock tree for streaming link backfill: %w", err)
		}
		cur, err := q.LockLookupItem(ctx, sqlc.LockLookupItemParams{ID: itemID, WorkspaceID: s.workspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // trashed or deleted meanwhile
		}
		if err != nil {
			return fmt.Errorf("lock streaming link backfill item: %w", err)
		}
		imp, err := q.GetLookupImport(ctx, sqlc.GetLookupImportParams{ItemID: itemID, WorkspaceID: s.workspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("get streaming link backfill import: %w", err)
		}
		settings, err := q.GetSettings(ctx, s.workspaceID)
		if err != nil {
			return fmt.Errorf("get settings for streaming link backfill: %w", err)
		}
		if imp.Provider != ProviderTMDB || imp.SourceID != sourceID || !strings.EqualFold(settings.StreamingCountryCode, country) {
			return nil
		}
		if !streamingLinkMissing(cur.Metadata, imp.ImportedFields, country) {
			return nil
		}
		if link == "" {
			outcome = backfillNoLink
		} else {
			if err := q.SetItemStreamingLink(ctx, sqlc.SetItemStreamingLinkParams{Link: link, ID: itemID, WorkspaceID: s.workspaceID}); err != nil {
				return fmt.Errorf("set streaming link: %w", err)
			}
			outcome = backfillUpdated
		}
		snapshot := link
		if link == "" {
			snapshot = noLinkMarkerPrefix + country
		}
		if err := q.SetStreamingLinkSnapshot(ctx, sqlc.SetStreamingLinkSnapshotParams{Snapshot: snapshot, ItemID: itemID, WorkspaceID: s.workspaceID}); err != nil {
			return fmt.Errorf("set streaming link snapshot: %w", err)
		}
		return nil
	})
	if err != nil {
		return backfillSkipped, err
	}
	return outcome, nil
}

// streamingLinkMissing mirrors the candidate query on the locked rows: platforms
// for this country, no link key, no snapshot entry, lookup not disabled.
func streamingLinkMissing(metadataJSON, importedJSON []byte, country string) bool {
	var metadata, imported map[string]any
	if json.Unmarshal(metadataJSON, &metadata) != nil || json.Unmarshal(importedJSON, &imported) != nil {
		return false
	}
	if disabled, _ := metadata["lookup_disabled"].(bool); disabled {
		return false
	}
	if _, has := imported["streaming.link"]; has {
		return false
	}
	streaming, _ := metadata["streaming"].(map[string]any)
	if streaming == nil {
		return false
	}
	if _, has := streaming["link"]; has {
		return false
	}
	if c, _ := streaming["country"].(string); !strings.EqualFold(c, country) {
		return false
	}
	platforms, _ := streaming["platforms"].([]any)
	return len(platforms) > 0
}
