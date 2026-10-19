package httpapi

import (
	"context"

	"github.com/oapi-codegen/nullable"

	"github.com/0xfederama/collections-app-backend/internal/httpapi/etag"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

func (s *Server) GetSettings(ctx context.Context, _ oapi.GetSettingsRequestObject) (oapi.GetSettingsResponseObject, error) {
	cur, err := s.deps.Settings.Get(ctx, s.deps.WorkspaceID)
	if err != nil {
		return nil, err
	}
	return oapi.GetSettings200JSONResponse{
		Body:    settingsBody(cur, s.deps.LookupAvailable, s.deps.BookLookupAvailable),
		Headers: oapi.GetSettings200ResponseHeaders{ETag: etag.Format(etag.Settings, cur.Version)},
	}, nil
}

func (s *Server) UpdateSettings(ctx context.Context, req oapi.UpdateSettingsRequestObject) (oapi.UpdateSettingsResponseObject, error) {
	// A missing body cannot reach here: the guard answers 400 for it first.
	updated, err := s.deps.Settings.Update(ctx, s.deps.WorkspaceID, IfMatchVersion(ctx), workspace.SettingsPatch{
		WorkspaceName:        req.Body.WorkspaceName,
		StreamingCountryCode: req.Body.StreamingCountryCode,
		MetadataLanguage:     req.Body.MetadataLanguage,
		MarkdownHistoryLimit: req.Body.MarkdownHistoryLimit,
		TrashRetentionDays:   optionalRetention(req.Body.TrashRetentionDays),
		PreferredStore:       optionalStore(req.Body.PreferredStore),
	})
	if err != nil {
		return nil, err
	}
	return oapi.UpdateSettings200JSONResponse{
		Body:    settingsBody(updated, s.deps.LookupAvailable, s.deps.BookLookupAvailable),
		Headers: oapi.UpdateSettings200ResponseHeaders{ETag: etag.Format(etag.Settings, updated.Version)},
	}, nil
}

// optionalRetention turns the nullable PATCH field into a workspace.Optional:
// absent leaves the setting, null turns it off.
func optionalRetention(n nullable.Nullable[int]) workspace.Optional[int] {
	if !n.IsSpecified() {
		return workspace.Optional[int]{}
	}
	if v, err := n.Get(); err == nil {
		return workspace.Optional[int]{Set: true, Value: &v}
	}
	return workspace.Optional[int]{Set: true}
}

// optionalStore copies the PATCH store key (absent = nil).
func optionalStore(in *oapi.StoreKey) *string {
	if in == nil {
		return nil
	}
	v := string(*in)
	return &v
}

func settingsBody(s workspace.Settings, lookupAvailable, bookLookupAvailable bool) oapi.Settings {
	// The retention is always set: an unset Nullable would marshal as 0, not null.
	retention := nullable.NewNullNullable[int]()
	if s.TrashRetentionDays != nil {
		retention = nullable.NewNullableWithValue(*s.TrashRetentionDays)
	}
	return oapi.Settings{
		WorkspaceName:        s.WorkspaceName,
		StreamingCountryCode: s.StreamingCountryCode,
		MetadataLanguage:     s.MetadataLanguage,
		LookupAvailable:      &lookupAvailable,
		BookLookupAvailable:  &bookLookupAvailable,
		MarkdownHistoryLimit: s.MarkdownHistoryLimit,
		TrashRetentionDays:   retention,
		PreferredStore:       oapi.StoreKey(s.PreferredStore),
		Version:              s.Version,
	}
}
