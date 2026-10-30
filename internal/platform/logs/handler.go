package logs

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
)

// viewHTML is the viewer page: one file, no external resources.
//
//go:embed view.html
var viewHTML string

// nonceMarker is the placeholder for the per-response CSP nonce in view.html.
const nonceMarker = "__NONCE__"

// viewCSP allows the page's own inline style and script (by nonce) and fetches to
// its own origin, and nothing else. It replaces the API-wide policy for this page only.
const viewCSP = "default-src 'none'; script-src 'nonce-%[1]s'; style-src 'nonce-%[1]s'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// levels maps the level query values to slog levels.
var levels = map[string]slog.Level{
	"debug": slog.LevelDebug,
	"info":  slog.LevelInfo,
	"warn":  slog.LevelWarn,
	"error": slog.LevelError,
}

// Register adds the log operations to the API: GET /admin/logs (the buffered records,
// JSON) and GET /admin/logs/view (the viewer page). Both are hidden from the public
// OpenAPI spec. They have no auth: serve them only on a private listener.
func Register(api huma.API, log *slog.Logger, buf *Buffer) {
	tags := []string{"admin"}
	huma.Register(api, huma.Operation{
		OperationID: "listLogs", Method: http.MethodGet, Path: "/admin/logs", Tags: tags, Hidden: true,
		Summary:     "List buffered log records",
		Description: "The newest records of the in-memory buffer, oldest first.",
	}, httpserver.Op(log, listLogs(buf)))
	huma.Register(api, huma.Operation{
		OperationID: "viewLogs", Method: http.MethodGet, Path: "/admin/logs/view", Tags: tags, Hidden: true,
		Summary:     "Log viewer page",
		Description: "A self-contained HTML page over /admin/logs.",
	}, viewLogs)
}

// ListLogsInput is the listLogs query.
type ListLogsInput struct {
	Level string    `query:"level" enum:"debug,info,warn,error" doc:"Lowest level to return. Default: all."`
	Since time.Time `query:"since" doc:"Only records logged at or after this RFC 3339 time."`
	Limit int       `query:"limit" default:"200" minimum:"1" maximum:"2000" doc:"Keep the newest N matching records."`
}

// ListLogsOutput is the listLogs answer.
type ListLogsOutput struct {
	Body LogsBody
}

// LogsBody is {data, total}: data is the newest matching records (oldest first),
// total is how many records match before the limit.
type LogsBody struct {
	Data  []LogEntry `json:"data"`
	Total int        `json:"total"`
}

// LogEntry is one record as the viewer shows it.
type LogEntry struct {
	Time  time.Time      `json:"time"`
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Attrs map[string]any `json:"attrs"`
}

func listLogs(buf *Buffer) func(context.Context, *ListLogsInput) (*ListLogsOutput, error) {
	return func(_ context.Context, in *ListLogsInput) (*ListLogsOutput, error) {
		level := slog.LevelDebug
		if in.Level != "" {
			level = levels[in.Level]
		}
		recs, total := buf.Query(level, in.Since, in.Limit)
		data := make([]LogEntry, 0, len(recs))
		for _, r := range recs {
			data = append(data, LogEntry{
				Time:  r.Time,
				Level: strings.ToLower(r.Level.String()),
				Msg:   r.Msg,
				Attrs: r.Attrs,
			})
		}
		return &ListLogsOutput{Body: LogsBody{Data: data, Total: total}}, nil
	}
}

// viewLogs serves view.html with a fresh CSP nonce for its inline style and script.
func viewLogs(_ context.Context, _ *struct{}) (*huma.StreamResponse, error) {
	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}
	page := strings.ReplaceAll(viewHTML, nonceMarker, nonce)
	return &huma.StreamResponse{Body: func(ctx huma.Context) {
		ctx.SetHeader("Content-Type", "text/html; charset=utf-8")
		ctx.SetHeader("Content-Security-Policy", fmt.Sprintf(viewCSP, nonce))
		ctx.SetHeader("Cache-Control", "no-store")
		ctx.SetStatus(http.StatusOK)
		_, _ = ctx.BodyWriter().Write([]byte(page))
	}}, nil
}

func newNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
