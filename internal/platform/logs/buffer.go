// Package logs keeps the newest log records of the process in memory and serves
// them through a small viewer (LOGS_UI). The records are copies made by Handler
// (slog.go), with secret attribute values redacted. There is no auth: the
// listener is the boundary (docs/modules/logs.md).
package logs

import (
	"log/slog"
	"sync"
	"time"
)

// DefaultSize is how many records the buffer keeps when NewBuffer gets no size.
const DefaultSize = 2000

// Record is one buffered log record. Attrs holds the record's attributes; a group
// is a nested map, and a value under a secret key (or inside a secret group) is
// replaced by "[redacted]".
type Record struct {
	Time  time.Time
	Level slog.Level
	Msg   string
	Attrs map[string]any
}

// Buffer is a ring of the newest records. It is safe for concurrent use.
type Buffer struct {
	mu     sync.Mutex
	max    int
	recs   []Record
	oldest int // index of the oldest record, moves only once the ring is full
}

// NewBuffer returns a buffer that keeps the newest size records (DefaultSize when size <= 0).
func NewBuffer(size int) *Buffer {
	if size <= 0 {
		size = DefaultSize
	}
	return &Buffer{max: size}
}

func (b *Buffer) add(r Record) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.recs) < b.max {
		b.recs = append(b.recs, r)
		return
	}
	b.recs[b.oldest] = r
	b.oldest = (b.oldest + 1) % b.max
}

// Query returns the records at level or above, logged at or after since (a zero
// since matches any time), oldest first. limit keeps the newest matches (limit <= 0
// keeps all). total counts every match before the limit is applied.
// The returned maps are never modified after they are recorded, so callers may read them freely.
func (b *Buffer) Query(level slog.Level, since time.Time, limit int) (out []Record, total int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(b.recs)
	matches := make([]Record, 0)
	for i := range n {
		r := b.recs[(b.oldest+i)%n]
		if r.Level < level || r.Time.Before(since) {
			continue
		}
		matches = append(matches, r)
	}
	total = len(matches)
	if limit > 0 && total > limit {
		matches = matches[total-limit:]
	}
	return matches, total
}
