package workspace

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/0xfederama/collections-app-backend/internal/platform/etag"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

// settingsReader is what GET reads through. The repository satisfies it; the
// router passes the service, which reads through the same store.
type settingsReader interface {
	Get(ctx context.Context) (Settings, error)
}

// Availability tells the settings response which optional lookups this server
// has configured. It is set once, at registration.
type Availability struct {
	// Lookup is true when the optional TMDB key is configured (movies and TV).
	Lookup bool
	// BookLookup is true when book lookup (Open Library) is on.
	BookLookup bool
}

// Register adds the settings operations to the API. The handlers do HTTP only:
// parse the input, call the service or the reader, shape the output.
func Register(api huma.API, log *slog.Logger, svc *Service, reader settingsReader, avail Availability) {
	h := &handler{svc: svc, reader: reader, avail: avail}
	tags := []string{"settings"}

	huma.Register(api, huma.Operation{
		OperationID: "getSettings", Method: http.MethodGet, Path: "/api/v1/settings", Tags: tags,
		Summary:     "Get workspace settings",
		Description: "The settings, including the workspace name.",
		Errors:      []int{http.StatusInternalServerError},
	}, httpserver.Op(log, h.get))
	huma.Register(api, huma.Operation{
		OperationID: "updateSettings", Method: http.MethodPatch, Path: "/api/v1/settings", Tags: tags,
		Summary: "Update workspace settings",
		Description: "Requires `If-Match` (`\"s-N\"`). Lowering `markdown_history_limit` prunes on the next save per item, not immediately. " +
			"`trash_retention_days`: absent = unchanged, `null` = off, otherwise 1 to 365. " +
			"`preferred_store` is the one default store of the workspace, never null. " +
			"`owned_streaming_services`: absent = unchanged, `[]` = show every service, otherwise the provider keys of the services the user owns.",
		Errors: []int{http.StatusBadRequest, http.StatusPreconditionFailed, http.StatusRequestEntityTooLarge,
			http.StatusUnprocessableEntity, http.StatusPreconditionRequired},
	}, httpserver.Op(log, h.update))
}

type handler struct {
	svc    *Service
	reader settingsReader
	avail  Availability
}

// SettingsJSON is the wire shape of the settings. Its schema is "Settings": the
// JSON suffix is stripped by the schema namer (httpserver.NewAPI), and the Go
// name avoids the domain type Settings in this package.
type SettingsJSON struct {
	WorkspaceName        string `json:"workspace_name" minLength:"1" maxLength:"200"`
	StreamingCountryCode string `json:"streaming_country_code" pattern:"^[A-Z]{2}$"`
	MetadataLanguage     string `json:"metadata_language" pattern:"^[a-z]{2}(-[A-Z]{2})?$" example:"it-IT"`
	LookupAvailable      bool   `json:"lookup_available" readOnly:"true" doc:"Whether this server has TMDB lookup (movies and TV) configured."`
	BookLookupAvailable  bool   `json:"book_lookup_available" readOnly:"true" doc:"Whether book lookup (Open Library) is available. It needs no key and is always on."`
	MarkdownHistoryLimit int    `json:"markdown_history_limit" minimum:"0" maximum:"200"`
	// TrashRetentionDays is always present: null when the automatic purge is off.
	TrashRetentionDays *int   `json:"trash_retention_days" nullable:"true" minimum:"1" maximum:"365"`
	PreferredStore     string `json:"preferred_store" enum:"amazon,bookshop,apple_books,google_play_books,kobo"`
	// OwnedStreamingServices is always present: [] means every service is shown.
	OwnedStreamingServices []string `json:"owned_streaming_services" nullable:"false" maxItems:"64" doc:"Provider keys of the streaming services the user owns; [] shows all."`
	Version                int      `json:"version"`
}

// SettingsOutput is the settings with their ETag ("s-N").
type SettingsOutput struct {
	ETag string `header:"ETag"`
	Body SettingsJSON
}

// UpdateSettingsRequest is the body of updateSettings. An absent field is left as it is.
// The value rules (lengths, patterns, ranges, store keys) are not tagged here on
// purpose: Huma reports constraint errors of several fields in map order, so the
// service validates them all, in its fixed order, as before the migration.
type UpdateSettingsRequest struct {
	WorkspaceName        *string `json:"workspace_name,omitempty"`
	StreamingCountryCode *string `json:"streaming_country_code,omitempty"`
	MetadataLanguage     *string `json:"metadata_language,omitempty"`
	MarkdownHistoryLimit *int    `json:"markdown_history_limit,omitempty"`
	// TrashRetentionDays: absent leaves it, null turns it off, otherwise 1 to 365.
	TrashRetentionDays httpserver.Optional[int] `json:"trash_retention_days,omitempty"`
	PreferredStore     *string                  `json:"preferred_store,omitempty"`
	// OwnedStreamingServices: absent leaves it, [] clears it. Values are checked by the service.
	OwnedStreamingServices *[]string `json:"owned_streaming_services,omitempty"`
}

// patch maps the PATCH body to the service input.
func (r UpdateSettingsRequest) patch() SettingsPatch {
	return SettingsPatch{
		WorkspaceName:          r.WorkspaceName,
		StreamingCountryCode:   r.StreamingCountryCode,
		MetadataLanguage:       r.MetadataLanguage,
		MarkdownHistoryLimit:   r.MarkdownHistoryLimit,
		TrashRetentionDays:     Optional[int]{Set: r.TrashRetentionDays.Set, Value: r.TrashRetentionDays.Value},
		PreferredStore:         r.PreferredStore,
		OwnedStreamingServices: r.OwnedStreamingServices,
	}
}

func (h *handler) output(s Settings) *SettingsOutput {
	owned := s.OwnedStreamingServices
	if owned == nil {
		owned = []string{}
	}
	return &SettingsOutput{
		ETag: etag.Format(etag.Settings, s.Version),
		Body: SettingsJSON{
			WorkspaceName:          s.WorkspaceName,
			StreamingCountryCode:   s.StreamingCountryCode,
			MetadataLanguage:       s.MetadataLanguage,
			LookupAvailable:        h.avail.Lookup,
			BookLookupAvailable:    h.avail.BookLookup,
			MarkdownHistoryLimit:   s.MarkdownHistoryLimit,
			TrashRetentionDays:     s.TrashRetentionDays,
			PreferredStore:         s.PreferredStore,
			OwnedStreamingServices: owned,
			Version:                s.Version,
		},
	}
}

func (h *handler) get(ctx context.Context, _ *struct{}) (*SettingsOutput, error) {
	cur, err := h.reader.Get(ctx)
	if err != nil {
		return nil, err
	}
	return h.output(cur), nil
}

type updateInput struct {
	IfMatch string `header:"If-Match" doc:"Settings ETag, \"s-N\"."`
	Body    UpdateSettingsRequest
}

func (h *handler) update(ctx context.Context, in *updateInput) (*SettingsOutput, error) {
	version, ok := etag.Parse(in.IfMatch, etag.Settings)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	updated, err := h.svc.Update(ctx, version, in.Body.patch())
	if err != nil {
		return nil, err
	}
	return h.output(updated), nil
}
