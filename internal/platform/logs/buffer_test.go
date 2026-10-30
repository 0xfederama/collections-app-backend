package logs

import (
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func rec(i int, level slog.Level) Record {
	return Record{Time: t0.Add(time.Duration(i) * time.Second), Level: level, Msg: fmt.Sprintf("m%d", i)}
}

func msgs(recs []Record) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Msg)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestBufferRingKeepsNewestInOrder: after overflow the buffer holds exactly the
// newest N records, oldest first, at every fill level.
func TestBufferRingKeepsNewestInOrder(t *testing.T) {
	const size = 3
	for _, n := range []int{0, 1, 2, 3, 4, 5, 9} {
		t.Run(fmt.Sprintf("after %d records", n), func(t *testing.T) {
			b := NewBuffer(size)
			for i := range n {
				b.add(rec(i, slog.LevelInfo))
			}
			got, total := b.Query(slog.LevelDebug, time.Time{}, 0)
			var want []string
			for i := max(0, n-size); i < n; i++ {
				want = append(want, fmt.Sprintf("m%d", i))
			}
			if !equal(msgs(got), want) {
				t.Errorf("got %v, want %v", msgs(got), want)
			}
			if total != len(want) {
				t.Errorf("total = %d, want %d", total, len(want))
			}
		})
	}
}

func TestBufferDefaultSize(t *testing.T) {
	b := NewBuffer(0)
	for i := range DefaultSize + 10 {
		b.add(rec(i, slog.LevelInfo))
	}
	got, total := b.Query(slog.LevelDebug, time.Time{}, 0)
	if len(got) != DefaultSize || total != DefaultSize {
		t.Fatalf("kept %d (total %d), want %d", len(got), total, DefaultSize)
	}
	if got[0].Msg != "m10" {
		t.Errorf("oldest kept = %q, want m10", got[0].Msg)
	}
}

// TestBufferConcurrentWrites: many writers and readers at once; run with -race.
func TestBufferConcurrentWrites(t *testing.T) {
	b := NewBuffer(50)
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				b.add(rec(w*1000+i, slog.LevelInfo))
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				got, _ := b.Query(slog.LevelDebug, time.Time{}, 10)
				if len(got) > 10 {
					t.Errorf("limit broken: %d records", len(got))
					return
				}
			}
		}()
	}
	wg.Wait()
	got, total := b.Query(slog.LevelDebug, time.Time{}, 0)
	if total != 50 || len(got) != 50 {
		t.Fatalf("after writes: %d kept, total %d, want 50", len(got), total)
	}
}

// TestBufferQueryFilters covers the level, since and limit filters and their combination.
func TestBufferQueryFilters(t *testing.T) {
	b := NewBuffer(10)
	levels := []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError, slog.LevelInfo, slog.LevelWarn}
	for i, l := range levels {
		b.add(rec(i, l))
	}
	tests := []struct {
		name      string
		level     slog.Level
		since     time.Time
		limit     int
		wantMsgs  []string
		wantTotal int
	}{
		{"all", slog.LevelDebug, time.Time{}, 0, []string{"m0", "m1", "m2", "m3", "m4", "m5"}, 6},
		{"info and up", slog.LevelInfo, time.Time{}, 0, []string{"m1", "m2", "m3", "m4", "m5"}, 5},
		{"warn and up", slog.LevelWarn, time.Time{}, 0, []string{"m2", "m3", "m5"}, 3},
		{"error only", slog.LevelError, time.Time{}, 0, []string{"m3"}, 1},
		{"since drops older", slog.LevelDebug, t0.Add(3 * time.Second), 0, []string{"m3", "m4", "m5"}, 3},
		{"since is inclusive", slog.LevelDebug, t0.Add(4 * time.Second), 0, []string{"m4", "m5"}, 2},
		{"limit keeps newest", slog.LevelDebug, time.Time{}, 2, []string{"m4", "m5"}, 6},
		{"limit after level", slog.LevelWarn, time.Time{}, 2, []string{"m3", "m5"}, 3},
		{"level and since", slog.LevelInfo, t0.Add(2 * time.Second), 0, []string{"m2", "m3", "m4", "m5"}, 4},
		{"nothing matches", slog.LevelError, t0.Add(4 * time.Second), 0, []string{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, total := b.Query(tt.level, tt.since, tt.limit)
			if !equal(msgs(got), tt.wantMsgs) {
				t.Errorf("records = %v, want %v", msgs(got), tt.wantMsgs)
			}
			if total != tt.wantTotal {
				t.Errorf("total = %d, want %d", total, tt.wantTotal)
			}
		})
	}
}
