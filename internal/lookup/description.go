package lookup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// The description of an item is its Markdown content. A provider's summary (F-028, D-162) fills it
// when it is empty, and while it is still the text the lookup wrote it replaces it on a re-match or
// a refresh. Everything here runs inside the Apply transaction, through the lookup's own queries.

// currentImportGeneration is the import generation every successful apply writes (D-164). A feature
// that needs existing imports read again bumps it; the backfill refreshes the rows below it.
const currentImportGeneration = 1

// maxSynopsisRunes caps the description a provider summary writes (D-162).
const maxSynopsisRunes = 5000

// markdownLeading are the characters that start a Markdown block or inline construct when they open a
// paragraph. An escape keeps them as plain text.
const markdownLeading = "\\`*_[]#+-!|>~<"

// orderedMarker is a line that opens an ordered list: "2001. " renders as a list, not as text.
var orderedMarker = regexp.MustCompile(`^([0-9]{1,9})([.)])`)

// normalizeSynopsis turns a provider summary into the item's description: plain text as one Markdown
// paragraph, whitespace collapsed, the Markdown that would start a block escaped, at most
// maxSynopsisRunes characters. It returns "" when there is no text.
func normalizeSynopsis(raw string) string {
	text := strings.Join(strings.Fields(raw), " ")
	if text == "" {
		return ""
	}
	text = escapeLeadingMarkdown(text)
	// Cut after escaping: the escapes only touch the first characters, so the cut never splits one.
	if utf8.RuneCountInString(text) > maxSynopsisRunes {
		text = strings.TrimSpace(string([]rune(text)[:maxSynopsisRunes]))
	}
	return text
}

// escapeLeadingMarkdown backslash-escapes the first character when it would start a Markdown block,
// and the marker of a numbered line.
func escapeLeadingMarkdown(s string) string {
	if m := orderedMarker.FindStringSubmatch(s); m != nil {
		return m[1] + `\` + m[2] + s[len(m[0]):]
	}
	if strings.IndexByte(markdownLeading, s[0]) >= 0 {
		return `\` + s
	}
	return s
}

// descriptionTrack is the provenance the import keeps for the item's description after an apply: the
// content version the lookup wrote and its SHA-256. Both are nil when the text is not the import's.
type descriptionTrack struct {
	version *int
	sha256  []byte
}

// writeDescription applies the summary p.Synopsis to the item's description inside the Apply
// transaction and returns the tracking the import records. old is the import before this apply.
//
// The text is fetched and written only when:
//   - the current content is empty (version 0 or an empty body), for every mode;
//   - or the text is still the import's (tracked: the current version and its SHA-256 match what the
//     lookup wrote) and the mode is a re-match or a refresh, and the summary changed.
//
// A tracked text that a fill leaves alone stays tracked. Once the user saves over it, the text is the
// user's: the tracking ends and nothing is written. A missing summary changes nothing. An untracked
// Hardcover create writes the text as the user's own (origin NULL, the caller as author), since
// nothing is imported from it (D-162).
func (s *DBStore) writeDescription(ctx context.Context, r Repo, itemID uuid.UUID, p ApplyParams, old Record) (descriptionTrack, error) {
	var prev descriptionTrack
	if old.DescriptionVersion != nil {
		prev = descriptionTrack{version: old.DescriptionVersion, sha256: old.DescriptionSHA256}
	}
	head, err := r.ContentHead(ctx, itemID)
	if err != nil {
		return descriptionTrack{}, err
	}
	tracked := prev.version != nil && head.Version == *prev.version && bytes.Equal(head.SHA256, prev.sha256)
	keep := descriptionTrack{}
	if tracked {
		keep = prev
	}
	if p.Synopsis == "" {
		return keep, nil
	}
	sum := sha256.Sum256([]byte(p.Synopsis))
	empty := head.Version == 0 || head.Size == 0
	replace := tracked && (p.Mode == ModeRematch || p.Mode == ModeScheduled)
	if !empty && !replace {
		return keep, nil
	}
	if replace && bytes.Equal(sum[:], head.SHA256) {
		return keep, nil
	}

	now := s.clock.Now()
	version := head.Version + 1
	w := contentWrite{ItemID: itemID, Version: version, Body: p.Synopsis, SHA256: sum[:], Now: now}
	untracked := p.Mode == ModeCreateUntracked
	if untracked {
		w.Author = callerID(ctx)
	} else {
		origin := "lookup"
		w.Origin = &origin
	}
	if err := r.InsertContent(ctx, w); err != nil {
		return descriptionTrack{}, fmt.Errorf("insert lookup description: %w", err)
	}
	if err := r.PruneContent(ctx, itemID, version-(p.HistoryLimit+1)); err != nil {
		return descriptionTrack{}, fmt.Errorf("prune lookup description history: %w", err)
	}
	if err := r.TouchContent(ctx, itemID, now); err != nil {
		return descriptionTrack{}, fmt.Errorf("touch item after lookup description: %w", err)
	}
	if untracked {
		return descriptionTrack{}, nil
	}
	return descriptionTrack{version: &version, sha256: sum[:]}, nil
}
