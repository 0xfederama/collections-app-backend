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
		Body:    settingsBody(cur),
		Headers: oapi.GetSettings200ResponseHeaders{ETag: etag.Format(etag.Settings, cur.Version)},
	}, nil
}

func (s *Server) UpdateSettings(ctx context.Context, req oapi.UpdateSettingsRequestObject) (oapi.UpdateSettingsResponseObject, error) {
	// A missing body cannot reach here: the guard answers 400 for it first.
	updated, err := s.deps.Settings.Update(ctx, s.deps.WorkspaceID, IfMatchVersion(ctx), workspace.SettingsPatch{
		WorkspaceName:        req.Body.WorkspaceName,
		StreamingCountryCode: req.Body.StreamingCountryCode,
		MarkdownHistoryLimit: req.Body.MarkdownHistoryLimit,
		TrashRetentionDays:   optionalRetention(req.Body.TrashRetentionDays),
	})
	if err != nil {
		return nil, err
	}
	return oapi.UpdateSettings200JSONResponse{
		Body:    settingsBody(updated),
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

func settingsBody(s workspace.Settings) oapi.Settings {
	// The retention is always set: an unset Nullable would marshal as 0, not null.
	retention := nullable.NewNullNullable[int]()
	if s.TrashRetentionDays != nil {
		retention = nullable.NewNullableWithValue(*s.TrashRetentionDays)
	}
	return oapi.Settings{
		WorkspaceName:        s.WorkspaceName,
		StreamingCountryCode: s.StreamingCountryCode,
		MarkdownHistoryLimit: s.MarkdownHistoryLimit,
		TrashRetentionDays:   retention,
		Version:              s.Version,
	}
}
