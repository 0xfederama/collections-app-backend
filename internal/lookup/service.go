package lookup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/clock"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

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

// Service resolves provider data (the provider is picked from the list's type)
// before delegating one atomic item/provenance mutation to Store.
type Service struct {
	providers   Providers
	store       Store
	items       ItemService
	settings    SettingsReader
	clock       clock.Clock
	workspaceID uuid.UUID
}

// New returns a lookup service bound to one workspace.
func New(providers Providers, store Store, itemSvc ItemService, settings SettingsReader, clk clock.Clock, workspaceID uuid.UUID) *Service {
	return &Service{providers: providers, store: store, items: itemSvc, settings: settings, clock: clk, workspaceID: workspaceID}
}

// CreateOptions preserves whether lookup=true was explicit: an omitted default
// degrades to a normal typed create when lookup is unavailable, while an
// explicit request returns a 503 domain error. TMDBID applies to movie and TV
// lists, SourceID to book lists; the other is ignored.
type CreateOptions struct {
	Lookup   *bool
	TMDBID   *int
	SourceID *string
}

// Selection is the match the caller picked from the suggestions: a tmdb_id for
// movie and TV items, a source_id (Open Library work id) for books. The one that
// does not fit the item's provider is ignored.
type Selection struct {
	TMDBID   *int
	SourceID *string
}

// forProvider is the source id picked for this provider, if any.
func (s Selection) forProvider(provider string) *string {
	if provider == ProviderTMDB {
		if s.TMDBID == nil {
			return nil
		}
		id := strconv.Itoa(*s.TMDBID)
		return &id
	}
	return s.SourceID
}

// Create creates an item, enriching it from the list type's provider when requested.
func (s *Service) Create(ctx context.Context, listID uuid.UUID, in items.CreateInput, opts CreateOptions) (items.Item, error) {
	typ, err := s.store.ListType(ctx, listID)
	if err != nil {
		return items.Item{}, err
	}
	provider := s.providers[typ]
	if provider == nil {
		return s.items.Create(ctx, listID, in)
	}
	if opts.Lookup != nil && !*opts.Lookup {
		in.Metadata = cloneInputMap(in.Metadata)
		in.Metadata["lookup_disabled"] = true
		return s.items.Create(ctx, listID, in)
	}

	selectedID := Selection{TMDBID: opts.TMDBID, SourceID: opts.SourceID}.forProvider(provider.Name())
	explicit := (opts.Lookup != nil && *opts.Lookup) || selectedID != nil
	if disabled, _ := in.Metadata["lookup_disabled"].(bool); disabled && !explicit {
		return s.items.Create(ctx, listID, in)
	}
	if explicit {
		in.Metadata = cloneInputMap(in.Metadata)
		delete(in.Metadata, "lookup_disabled")
	}
	if !provider.Enabled() {
		if explicit {
			return items.Item{}, apperr.LookupDisabled()
		}
		return s.items.Create(ctx, listID, in)
	}
	settings, err := s.settings.Get(ctx, s.workspaceID)
	if err != nil {
		return items.Item{}, err
	}
	candidate, err := s.resolve(ctx, provider, typ, in.Title, selectedID, settings)
	if err != nil {
		if explicit {
			return items.Item{}, err
		}
		return s.items.Create(ctx, listID, in)
	}
	if candidate == nil {
		if selectedID != nil {
			return items.Item{}, apperr.UpstreamUnavailable()
		}
		return s.items.Create(ctx, listID, in)
	}
	poster, err := s.poster(ctx, provider, candidate.details.ImagePath)
	if err != nil {
		if explicit {
			return items.Item{}, err
		}
		return s.items.Create(ctx, listID, in)
	}
	return s.store.Apply(ctx, ApplyParams{
		Mode: ModeCreate, ListID: listID, Type: typ, CreateInput: &in,
		Provider: provider.Name(), SourceID: candidate.details.SourceID,
		Title: candidate.details.Title, OriginalTitle: in.Title,
		Fields: candidate.details.Fields, FillOnly: createFillOnly(candidate.details.FillOnly, settings.PreferredStores),
		PosterPath: candidate.details.ImagePath, Poster: poster,
		RefreshFields: true, RefreshPoster: true,
	})
}

// Apply enriches an existing item from its list type's provider. Network work
// happens against an initial snapshot; Store rechecks ExpectedVersion under lock
// before writing.
func (s *Service) Apply(ctx context.Context, itemID uuid.UUID, expectedVersion int, mode Mode, selection Selection) (items.Item, error) {
	if mode != ModeFill && mode != ModeRematch && mode != ModeRefresh {
		return items.Item{}, apperr.Validation(apperr.FieldError{Field: "mode", Message: "must be fill, rematch, or refresh"})
	}
	if !s.anyEnabled() {
		return items.Item{}, apperr.LookupDisabled()
	}
	it, err := s.items.Get(ctx, itemID)
	if err != nil {
		return items.Item{}, err
	}
	if it.Version != expectedVersion {
		return items.Item{}, apperr.VersionConflict(it.Version)
	}
	itemType := typeKey(it.Type)
	provider := s.providers[itemType]
	if provider == nil {
		return items.Item{}, apperr.Validation(apperr.FieldError{Field: "item", Message: "lookup is available only for movie, TV and book items"})
	}
	if !provider.Enabled() {
		return items.Item{}, apperr.LookupDisabled()
	}
	settings, err := s.settings.Get(ctx, s.workspaceID)
	if err != nil {
		return items.Item{}, err
	}

	selectedID := selection.forProvider(provider.Name())
	lookupID := selectedID
	if mode == ModeRefresh && lookupID == nil {
		name := provider.Name()
		if id := metadataSourceID(name, it.Metadata); id != "" {
			lookupID = &id
		} else if record, ok, rerr := s.store.Read(ctx, itemID); rerr != nil {
			return items.Item{}, rerr
		} else if ok && record.Provider == name && record.SourceID != "" {
			lookupID = &record.SourceID
		} else {
			return items.Item{}, apperr.Validation(apperr.FieldError{
				Field: sourceIDField(name), Message: fmt.Sprintf("is required when the item has no previous %s match", providerLabel(name)),
			})
		}
	}
	candidate, err := s.resolve(ctx, provider, itemType, it.Title, lookupID, settings)
	if err != nil {
		return items.Item{}, err
	}
	if candidate == nil {
		if selectedID != nil {
			return items.Item{}, apperr.UpstreamUnavailable()
		}
		// Recheck the ETag even though no fields matched and no write is needed.
		return s.items.Update(ctx, itemID, expectedVersion, items.Patch{})
	}

	refreshPoster := it.Image == nil
	var poster *images.Processed
	if refreshPoster {
		poster, err = s.poster(ctx, provider, candidate.details.ImagePath)
		if err != nil {
			return items.Item{}, err
		}
	}
	return s.store.Apply(ctx, ApplyParams{
		Mode: mode, ItemID: itemID, ExpectedVersion: expectedVersion, Type: itemType,
		Provider: provider.Name(), SourceID: candidate.details.SourceID,
		Title: candidate.details.Title, OriginalTitle: it.Title,
		Fields: candidate.details.Fields, FillOnly: candidate.details.FillOnly,
		PosterPath: candidate.details.ImagePath, Poster: poster,
		RefreshFields: true, RefreshPoster: refreshPoster,
	})
}

// RefreshOwned is the maintenance callback for one imported item. Store owns
// expiry; this method only attempts a safe refresh of still-owned values. Only
// providers with a retention are ever scheduled (D-095: Open Library has none).
func (s *Service) RefreshOwned(ctx context.Context, itemID uuid.UUID, expectedVersion int) error {
	record, ok, err := s.store.Read(ctx, itemID)
	if err != nil || !ok {
		return err
	}
	retention := retentionFor(record.Provider)
	if retention == nil {
		return nil
	}
	provider := s.providerByName(record.Provider)
	if provider == nil || !provider.Enabled() {
		return apperr.LookupDisabled()
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
	candidate, err := s.resolve(ctx, provider, record.Type, it.Title, &record.SourceID, settings)
	if err != nil {
		return err
	}
	if candidate == nil {
		return fmt.Errorf("refresh imported item: %s details missing", providerLabel(record.Provider))
	}

	now := s.clock.Now()
	refreshFields := !now.Before(addMonthsClamped(record.FetchedAt, retention.RefreshMonths))
	refreshPoster := record.AttachmentID == nil || record.PosterFetchedAt == nil ||
		!now.Before(addMonthsClamped(*record.PosterFetchedAt, retention.RefreshMonths))
	var poster *images.Processed
	if refreshPoster && candidate.details.ImagePath != "" {
		poster, err = s.poster(ctx, provider, candidate.details.ImagePath)
		if err != nil {
			return err
		}
	}
	_, err = s.store.Apply(ctx, ApplyParams{
		Mode: ModeOwned, ItemID: itemID, ExpectedVersion: expectedVersion, Type: record.Type,
		Provider: record.Provider, SourceID: candidate.details.SourceID,
		Title: candidate.details.Title, OriginalTitle: record.OriginalTitle,
		Fields: candidate.details.Fields, PosterPath: candidate.details.ImagePath, Poster: poster,
		RefreshFields: refreshFields, RefreshPoster: refreshPoster,
	})
	return err
}

type candidate struct{ details Details }

func (s *Service) resolve(ctx context.Context, provider Provider, typ, title string, selectedID *string, settings workspace.Settings) (*candidate, error) {
	id := ""
	if selectedID != nil {
		id = *selectedID
	} else {
		matches, err := provider.Search(ctx, typ, title, settings.MetadataLanguage)
		if err != nil {
			return nil, ProviderError(err)
		}
		if len(matches) == 0 {
			return nil, nil
		}
		id = matches[0].SourceID
		if provider.ValidateSourceID(id) != nil {
			return nil, apperr.UpstreamUnavailable()
		}
	}
	if err := provider.ValidateSourceID(id); err != nil {
		return nil, err
	}
	details, err := provider.Details(ctx, typ, id, settings.MetadataLanguage, settings.StreamingCountryCode)
	if err != nil {
		return nil, ProviderError(err)
	}
	if details.SourceID != id {
		return nil, apperr.UpstreamUnavailable()
	}
	return &candidate{details: details}, nil
}

func (s *Service) poster(ctx context.Context, provider Provider, path string) (*images.Processed, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := provider.DownloadImage(ctx, path)
	if errors.Is(err, ErrNoImage) {
		return nil, nil
	}
	if err != nil {
		return nil, ProviderError(err)
	}
	processed, err := images.Process(bytes.NewReader(raw))
	if err != nil {
		return nil, apperr.UpstreamUnavailable()
	}
	return processed, nil
}

func (s *Service) anyEnabled() bool {
	for _, p := range s.providers {
		if p != nil && p.Enabled() {
			return true
		}
	}
	return false
}

func (s *Service) providerByName(name string) Provider {
	for _, p := range s.providers {
		if p != nil && p.Name() == name {
			return p
		}
	}
	return nil
}

// createFillOnly adds the workspace's preferred stores to a store link the
// provider proposes (Open Library first), as a normal item create would seed
// them (D-096). The result is applied only when the create request left
// `stores` unset.
func createFillOnly(fill map[string]any, preferred []string) map[string]any {
	proposed, ok := fill["stores"].([]string)
	if !ok {
		return fill
	}
	merged := slices.Clone(proposed)
	for _, key := range preferred {
		if !slices.Contains(merged, key) {
			merged = append(merged, key)
		}
	}
	out := make(map[string]any, len(fill))
	for k, v := range fill {
		out[k] = v
	}
	out["stores"] = merged
	return out
}

// metadataSourceID reads the provider's id from item metadata ("" when absent or invalid).
func metadataSourceID(provider string, raw json.RawMessage) string {
	var metadata map[string]any
	if json.Unmarshal(raw, &metadata) != nil {
		return ""
	}
	key, _ := identity(provider, "")
	switch v := metadata[key].(type) {
	case float64:
		if v <= 0 || v != float64(int(v)) {
			return ""
		}
		id := strconv.Itoa(int(v))
		if ValidSourceID(provider, id) != nil {
			return ""
		}
		return id
	case string:
		if ValidSourceID(provider, v) != nil {
			return ""
		}
		return v
	}
	return ""
}

func cloneInputMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}
