package images

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

const (
	urlMaxLen  = 2048
	nameMaxLen = 255
)

// attachmentKeys builds the generated keys of an attachment: a two-level fan-out
// from the id keeps directories small. Nothing user-supplied is part of a key.
func attachmentKeys(id uuid.UUID, ext string) (key, thumb string) {
	s := id.String()
	dir := "images/" + s[0:2] + "/" + s[2:4] + "/" + s
	return dir + "." + ext, dir + ".thumb.jpg"
}

// cleanUploadName keeps the display name: base name only, no control
// characters, at most 255 characters. nil when nothing is left.
func cleanUploadName(in *string) *string {
	if in == nil {
		return nil
	}
	name := strings.NewReplacer("\\", "/").Replace(*in)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > nameMaxLen {
		name = string([]rune(name)[:nameMaxLen])
	}
	if name == "" {
		return nil
	}
	return &name
}

// cleanImageURL accepts absolute http and https URLs of at most 2048 characters
// (the same rule as website in the items service).
func cleanImageURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	bad := func(msg string) (string, error) {
		return "", apperr.Validation(apperr.FieldError{Field: "url", Message: msg})
	}
	if utf8.RuneCountInString(s) > urlMaxLen {
		return bad(fmt.Sprintf("must be at most %d characters", urlMaxLen))
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return bad("must be an http or https URL")
	}
	return s, nil
}
