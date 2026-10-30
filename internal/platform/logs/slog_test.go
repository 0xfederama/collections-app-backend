package logs_test

import (
	"bytes"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/0xfederama/collections-app-backend/internal/platform/logs"
)

// capture logs through a Handler over a JSON output and returns the newest buffered record.
func capture(t *testing.T, level slog.Level, log func(*slog.Logger)) (logs.Record, string) {
	t.Helper()
	var out bytes.Buffer
	buf := logs.NewBuffer(10)
	logger := slog.New(logs.NewHandler(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: level}), buf))
	log(logger)
	recs, _ := buf.Query(slog.LevelDebug, time.Time{}, 0)
	if len(recs) == 0 {
		t.Fatal("nothing buffered")
	}
	return recs[len(recs)-1], out.String()
}

// TestRedaction: secret keys (case-insensitive, exact) are replaced in the buffer,
// including inside secret groups and nested groups; other keys are untouched.
func TestRedaction(t *testing.T) {
	tests := []struct {
		name string
		args []any
		want map[string]any
	}{
		{"password", []any{"password", "hunter2"}, map[string]any{"password": "[redacted]"}},
		{"mixed case", []any{"Password", "x"}, map[string]any{"Password": "[redacted]"}},
		{"secret", []any{"client_secret", "x", "secret", "y"}, map[string]any{"client_secret": "x", "secret": "[redacted]"}},
		{"token", []any{"token", "t"}, map[string]any{"token": "[redacted]"}},
		{"api_key", []any{"API_KEY", "k"}, map[string]any{"API_KEY": "[redacted]"}},
		{"apikey", []any{"apikey", "k"}, map[string]any{"apikey": "[redacted]"}},
		{"authorization", []any{"Authorization", "Bearer x"}, map[string]any{"Authorization": "[redacted]"}},
		{"cookie", []any{"cookie", "sid=1"}, map[string]any{"cookie": "[redacted]"}},
		{"key", []any{"key", "v"}, map[string]any{"key": "[redacted]"}},
		{"exact match only", []any{"keys", "a", "token_count", 3, "user", "bob"},
			map[string]any{"keys": "a", "token_count": int64(3), "user": "bob"}},
		{"secret key in a group", []any{slog.Group("auth", slog.String("token", "t"), slog.String("user", "u"))},
			map[string]any{"auth": map[string]any{"token": "[redacted]", "user": "u"}}},
		{"whole group named as a secret", []any{slog.Group("token", slog.String("expires", "soon"))},
			map[string]any{"token": map[string]any{"expires": "[redacted]"}}},
		{"inline group keeps its keys", []any{slog.Group("", slog.String("password", "p"), slog.Int("n", 1))},
			map[string]any{"password": "[redacted]", "n": int64(1)}},
		{"nested secret deep down", []any{slog.Group("req", slog.Group("", slog.String("cookie", "c")))},
			map[string]any{"req": map[string]any{"cookie": "[redacted]"}}},
		{"empty attrs are skipped", []any{slog.Attr{}, "n", 1}, map[string]any{"n": int64(1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, _ := capture(t, slog.LevelDebug, func(l *slog.Logger) { l.Info("m", tt.args...) })
			if !reflect.DeepEqual(rec.Attrs, tt.want) {
				t.Errorf("attrs = %#v\nwant    %#v", rec.Attrs, tt.want)
			}
		})
	}
}

// TestRedactionThroughWithAttrsAndGroups: attributes added with With and WithGroup
// are redacted the same way, and keep their group.
func TestRedactionThroughWithAttrsAndGroups(t *testing.T) {
	rec, _ := capture(t, slog.LevelDebug, func(l *slog.Logger) {
		l.With("cookie", "sid=1").WithGroup("req").With("token", "t").Info("m", "path", "/x", "password", "p")
	})
	want := map[string]any{
		"cookie": "[redacted]",
		"req": map[string]any{
			"token":    "[redacted]",
			"path":     "/x",
			"password": "[redacted]",
		},
	}
	if !reflect.DeepEqual(rec.Attrs, want) {
		t.Errorf("attrs = %#v\nwant    %#v", rec.Attrs, want)
	}
}

// TestValuesAreReadable: errors and durations are text in the buffer, not {} or nanoseconds.
func TestValuesAreReadable(t *testing.T) {
	rec, _ := capture(t, slog.LevelDebug, func(l *slog.Logger) {
		l.Info("m", "err", errors.New("boom"), "took", 1500*time.Millisecond, "ok", true)
	})
	want := map[string]any{"err": "boom", "took": "1.5s", "ok": true}
	if !reflect.DeepEqual(rec.Attrs, want) {
		t.Errorf("attrs = %#v, want %#v", rec.Attrs, want)
	}
}

// TestHandlerWritesAndRecords: the record reaches the output and the buffer, with level, time and message.
func TestHandlerWritesAndRecords(t *testing.T) {
	before := time.Now().Add(-time.Second)
	rec, out := capture(t, slog.LevelDebug, func(l *slog.Logger) { l.Warn("request", "route", "/x") })
	if !strings.Contains(out, `"msg":"request"`) || !strings.Contains(out, `"route":"/x"`) {
		t.Errorf("output lacks the record: %s", out)
	}
	if rec.Level != slog.LevelWarn || rec.Msg != "request" || rec.Attrs["route"] != "/x" {
		t.Errorf("record = %+v", rec)
	}
	if rec.Time.Before(before) {
		t.Errorf("time = %v, want now", rec.Time)
	}
}

// TestHandlerEnabledFollowsOutput: a level the output drops is not buffered either.
func TestHandlerEnabledFollowsOutput(t *testing.T) {
	var out bytes.Buffer
	buf := logs.NewBuffer(10)
	logger := slog.New(logs.NewHandler(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelWarn}), buf))
	logger.Info("dropped")
	logger.Warn("kept")
	recs, total := buf.Query(slog.LevelDebug, time.Time{}, 0)
	if total != 1 || recs[0].Msg != "kept" {
		t.Errorf("buffered %v (total %d), want only the warn record", recs, total)
	}
}
