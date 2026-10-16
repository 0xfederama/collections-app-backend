package lookup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/clock"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/tmdb"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

// Client is the provider surface used by lookup orchestration.
type Client interface {
	Enabled() bool
	Search(ctx context.Context, kind, query, language string) ([]tmdb.Match, error)
	Details(ctx context.Context, kind string, id int, language, country string) (tmdb.Details, error)
	DownloadPoster(ctx context.Context, path string) ([]byte, error)
}

// ItemService is the item surface needed outside Store's atomic import writes.
type ItemService interface {
	Get(ctx context.Context, id uuid.UUID) (items.Item, error)
	Create(ctx context.Context, listID uuid.UUID, in items.CreateInput) (items.Item, error)
	Update(ctx context.Context, id uuid.UUID, version int, p items.Patch) (items.Item, error)
}

// SettingsReader supplies localization and streaming-region preferences.
type SettingsReader interface {
	Get(ctx context.Context, workspaceID uuid.UUID) (workspace.Settings, error)
}

// Service resolves provider data before delegating one atomic item/provenance
// mutation to Store.
type Service struct {
	client      Client
	store       Store
	items       ItemService
	settings    SettingsReader
	clock       clock.Clock
	workspaceID uuid.UUID
}

// New returns a lookup service bound to one workspace.
func New(client Client, store Store, itemSvc ItemService, settings SettingsReader, clk clock.Clock, workspaceID uuid.UUID) *Service {
	return &Service{client: client, store: store, items: itemSvc, settings: settings, clock: clk, workspaceID: workspaceID}
}

// CreateOptions preserves whether lookup=true was explicit: an omitted default
// degrades to a normal typed create when lookup is unavailable, while an
// explicit request returns a 503 domain error.
type CreateOptions struct {
	Lookup *bool
	TMDBID *int
}

// Create creates an item, enriching movie and TV items when requested.
func (s *Service) Create(ctx context.Context, listID uuid.UUID, in items.CreateInput, opts CreateOptions) (items.Item, error) {
	kind, err := s.store.ListKind(ctx, listID)
	if err != nil {
		return items.Item{}, err
	}
	if kind != "movie" && kind != "tv_show" {
		return s.items.Create(ctx, listID, in)
	}
	if opts.Lookup != nil && !*opts.Lookup {
		in.Metadata = cloneMap(in.Metadata)
		in.Metadata["lookup_disabled"] = true
		return s.items.Create(ctx, listID, in)
	}

	explicit := (opts.Lookup != nil && *opts.Lookup) || opts.TMDBID != nil
	if s.client == nil || !s.client.Enabled() {
		if explicit {
			return items.Item{}, apperr.LookupDisabled()
		}
		return s.items.Create(ctx, listID, in)
	}
	settings, err := s.settings.Get(ctx, s.workspaceID)
	if err != nil {
		return items.Item{}, err
	}
	candidate, err := s.resolve(ctx, kind, in.Title, opts.TMDBID, settings)
	if err != nil {
		if explicit {
			return items.Item{}, err
		}
		return s.items.Create(ctx, listID, in)
	}
	if candidate == nil {
		return s.items.Create(ctx, listID, in)
	}
	poster, err := s.poster(ctx, candidate.details.PosterPath)
	if err != nil {
		if explicit {
			return items.Item{}, err
		}
		return s.items.Create(ctx, listID, in)
	}
	return s.store.Apply(ctx, ApplyParams{
		Mode: ModeCreate, ListID: listID, Kind: kind, CreateInput: &in,
		TMDBID: candidate.details.ID, Title: candidate.details.Title, OriginalTitle: in.Title,
		Fields: candidate.details.Metadata, PosterPath: candidate.details.PosterPath, Poster: poster,
		RefreshFields: true, RefreshPoster: true,
	})
}

// Apply enriches an existing movie or TV item. Network work happens against an
// initial snapshot; Store rechecks ExpectedVersion under lock before writing.
func (s *Service) Apply(ctx context.Context, itemID uuid.UUID, expectedVersion int, mode Mode, selectedID *int) (items.Item, error) {
	if s.client == nil || !s.client.Enabled() {
		return items.Item{}, apperr.LookupDisabled()
	}
	it, err := s.items.Get(ctx, itemID)
	if err != nil {
		return items.Item{}, err
	}
	if it.Version != expectedVersion {
		return items.Item{}, apperr.VersionConflict(it.Version)
	}
	if it.Kind != "movie" && it.Kind != "tv_show" {
		return items.Item{}, apperr.Validation(apperr.FieldError{Field: "item", Message: "TMDB lookup is available only for movie and TV items"})
	}
	settings, err := s.settings.Get(ctx, s.workspaceID)
	if err != nil {
		return items.Item{}, err
	}

	lookupID := selectedID
	if mode == ModeRefresh && lookupID == nil {
		if id := metadataTMDBID(it.Metadata); id > 0 {
			lookupID = &id
		} else if record, ok, rerr := s.store.Read(ctx, itemID); rerr != nil {
			return items.Item{}, rerr
		} else if ok && record.TMDBID > 0 {
			lookupID = &record.TMDBID
		} else {
			return items.Item{}, apperr.Validation(apperr.FieldError{Field: "tmdb_id", Message: "is required when the item has no previous TMDB match"})
		}
	}
	candidate, err := s.resolve(ctx, it.Kind, it.Title, lookupID, settings)
	if err != nil {
		return items.Item{}, err
	}
	if candidate == nil {
		// Recheck the ETag even though no fields matched and no write is needed.
		return s.items.Update(ctx, itemID, expectedVersion, items.Patch{})
	}

	refreshPoster := it.Image == nil
	var poster *images.Processed
	if refreshPoster {
		poster, err = s.poster(ctx, candidate.details.PosterPath)
		if err != nil {
			return items.Item{}, err
		}
	}
	return s.store.Apply(ctx, ApplyParams{
		Mode: mode, ItemID: itemID, ExpectedVersion: expectedVersion, Kind: it.Kind,
		TMDBID: candidate.details.ID, Title: candidate.details.Title, OriginalTitle: it.Title,
		Fields: candidate.details.Metadata, PosterPath: candidate.details.PosterPath, Poster: poster,
		RefreshFields: true, RefreshPoster: refreshPoster,
	})
}

// RefreshOwned is the maintenance callback for one imported item. Store owns
// expiry; this method only attempts a safe refresh of still-owned values.
func (s *Service) RefreshOwned(ctx context.Context, itemID uuid.UUID, expectedVersion int) error {
	if s.client == nil || !s.client.Enabled() {
		return apperr.LookupDisabled()
	}
	record, ok, err := s.store.Read(ctx, itemID)
	if err != nil || !ok {
		return err
	}
	it, err := s.items.Get(ctx, itemID)
	if err != nil {
		return err
	}
	if it.Version != expectedVersion {
		return apperr.VersionConflict(it.Version)
	}
	settings, err := s.settings.Get(ctx, s.workspaceID)
	if err != nil {
		return err
	}
	candidate, err := s.resolve(ctx, record.Kind, it.Title, &record.TMDBID, settings)
	if err != nil {
		return err
	}
	if candidate == nil {
		return fmt.Errorf("refresh imported item: TMDB details missing")
	}

	now := s.clock.Now()
	refreshFields := !now.Before(record.FetchedAt.AddDate(0, 5, 0))
	refreshPoster := record.AttachmentID == nil || record.PosterFetchedAt == nil ||
		!now.Before(record.PosterFetchedAt.AddDate(0, 5, 0))
	var poster *images.Processed
	if refreshPoster && candidate.details.PosterPath != "" {
		poster, err = s.poster(ctx, candidate.details.PosterPath)
		if err != nil {
			return err
		}
	}
	_, err = s.store.Apply(ctx, ApplyParams{
		Mode: ModeOwned, ItemID: itemID, ExpectedVersion: expectedVersion, Kind: record.Kind,
		TMDBID: candidate.details.ID, Title: candidate.details.Title, OriginalTitle: record.OriginalTitle,
		Fields: candidate.details.Metadata, PosterPath: candidate.details.PosterPath, Poster: poster,
		RefreshFields: refreshFields, RefreshPoster: refreshPoster,
	})
	return err
}

type candidate struct{ details tmdb.Details }

func (s *Service) resolve(ctx context.Context, kind, title string, selectedID *int, settings workspace.Settings) (*candidate, error) {
	id := 0
	if selectedID != nil {
		id = *selectedID
	} else {
		matches, err := s.client.Search(ctx, kind, title, settings.MetadataLanguage)
		if err != nil {
			return nil, providerError(err)
		}
		if len(matches) == 0 {
			return nil, nil
		}
		id = matches[0].ID
	}
	details, err := s.client.Details(ctx, kind, id, settings.MetadataLanguage, settings.StreamingCountryCode)
	if err != nil {
		return nil, providerError(err)
	}
	return &candidate{details: details}, nil
}

func (s *Service) poster(ctx context.Context, path string) (*images.Processed, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := s.client.DownloadPoster(ctx, path)
	if err != nil {
		return nil, providerError(err)
	}
	processed, err := images.Process(bytes.NewReader(raw))
	if err != nil {
		return nil, apperr.UpstreamUnavailable()
	}
	return processed, nil
}

func providerError(err error) error {
	if errors.Is(err, tmdb.ErrDisabled) {
		return apperr.LookupDisabled()
	}
	return apperr.UpstreamUnavailable()
}

func metadataTMDBID(raw json.RawMessage) int {
	var metadata map[string]any
	if json.Unmarshal(raw, &metadata) != nil {
		return 0
	}
	v, ok := metadata["tmdb_id"].(float64)
	if !ok || v <= 0 || v != float64(int(v)) {
		return 0
	}
	return int(v)
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}
