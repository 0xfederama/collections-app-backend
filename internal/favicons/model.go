// Package favicons serves site favicons for item website links (roadmap 28). The web never
// calls a third-party favicon service: the backend fetches the icon of the link's origin,
// keeps it on disk and serves it. The fetch is an SSRF surface, so the outbound client
// refuses non-public addresses at dial time (see fetch.go).
package favicons

import (
	"errors"
	"time"
)

const (
	// MaxURLLength is the longest accepted url query value.
	MaxURLLength = 2048
	// MaxIconBytes is the largest icon body kept.
	MaxIconBytes = 256 << 10
	// MaxPageBytes is how much of the origin's HTML page is parsed for <link rel=icon>.
	MaxPageBytes = 512 << 10
	// HitTTL and MissTTL are how long a found icon and a "no icon" answer are trusted.
	HitTTL  = 30 * 24 * time.Hour
	MissTTL = 24 * time.Hour
)

// Icon is a favicon ready to serve. ContentType is always sniffed from the bytes.
type Icon struct {
	ContentType string
	Data        []byte
}

var (
	// ErrNoIcon means the origin has no usable icon.
	ErrNoIcon = errors.New("favicons: no icon")
	// ErrBlocked means the origin resolves to an address the server must not call.
	ErrBlocked = errors.New("favicons: address not allowed")
)
