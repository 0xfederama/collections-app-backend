package httpapi

import (
	"context"

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
		DefaultCurrency:      req.Body.DefaultCurrency,
		MarkdownHistoryLimit: req.Body.MarkdownHistoryLimit,
	})
	if err != nil {
		return nil, err
	}
	return oapi.UpdateSettings200JSONResponse{
		Body:    settingsBody(updated),
		Headers: oapi.UpdateSettings200ResponseHeaders{ETag: etag.Format(etag.Settings, updated.Version)},
	}, nil
}

func settingsBody(s workspace.Settings) oapi.Settings {
	return oapi.Settings{
		WorkspaceName:        s.WorkspaceName,
		StreamingCountryCode: s.StreamingCountryCode,
		DefaultCurrency:      s.DefaultCurrency,
		MarkdownHistoryLimit: s.MarkdownHistoryLimit,
		Version:              s.Version,
	}
}
