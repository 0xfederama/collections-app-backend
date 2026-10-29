package lists

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

// List icons (F012-B1). An icon is one emoji (one grapheme) or `i:<name>` for a built-in icon key.
// icon_color is one of ten palette names and only applies to a built-in icon.
//
// No grapheme library is a dependency (go.mod has none, and no new dependency is added), so an emoji is checked by a
// small grammar instead: one element (a base symbol with its variation selectors, skin tone, tags or keycap, or
// a regional indicator pair) optionally followed by ZWJ and another element, at most 32 bytes.
// Anything else (letters, digits outside a keycap, spaces, control characters, two emoji side by side) is rejected.

const (
	maxEmojiIconBytes = 32
	iconPrefix        = "i:"
	zwj               = '‍'
	vs15              = '︎'
	vs16              = '️'
	keycap            = '⃣'
)

var (
	builtinIconRe = regexp.MustCompile(`^i:[a-z0-9][a-z0-9-]{0,39}$`)
	iconColors    = map[string]bool{
		"red": true, "orange": true, "amber": true, "green": true, "teal": true,
		"blue": true, "indigo": true, "purple": true, "pink": true, "gray": true,
	}
)

// isBuiltinIcon reports whether a stored or supplied icon is a built-in icon key (the only type that takes a colour).
func isBuiltinIcon(icon *string) bool {
	return icon != nil && strings.HasPrefix(*icon, iconPrefix)
}

// validIcon reports whether icon is a built-in icon key or one emoji.
func validIcon(icon string) bool {
	if strings.HasPrefix(icon, iconPrefix) {
		return builtinIconRe.MatchString(icon)
	}
	return validEmoji(icon)
}

// ValidIcon reports whether icon is a built-in icon key or one emoji. It is
// shared by list icons and type default icons.
func ValidIcon(icon string) bool { return validIcon(icon) }

// iconErrors checks a supplied icon and icon_color against each other (the colour needs a built-in icon).
// icon is the icon that applies after the request (nil = none).
func iconErrors(icon, iconColor *string) []apperr.FieldError {
	var errs []apperr.FieldError
	if icon != nil && !validIcon(*icon) {
		errs = append(errs, apperr.FieldError{Field: "icon", Message: "must be one emoji or i: followed by a lowercase name of 1 to 40 letters, digits or hyphens"})
	}
	if iconColor != nil {
		if !iconColors[*iconColor] {
			errs = append(errs, apperr.FieldError{Field: "icon_color", Message: "must be one of red, orange, amber, green, teal, blue, indigo, purple, pink, gray"})
		} else if !isBuiltinIcon(icon) {
			errs = append(errs, apperr.FieldError{Field: "icon_color", Message: "is only allowed on a built-in icon (i:<name>)"})
		}
	}
	return errs
}

// validEmoji reports whether s is exactly one emoji grapheme (approximated, see the file comment).
func validEmoji(s string) bool {
	if s == "" || len(s) > maxEmojiIconBytes || !utf8.ValidString(s) {
		return false
	}
	rs := []rune(s)
	i := 0
	for {
		next, ok := emojiElement(rs, i)
		if !ok {
			return false
		}
		i = next
		if i == len(rs) {
			return true
		}
		if rs[i] != zwj {
			return false
		}
		i++
		if i == len(rs) {
			return false
		}
	}
}

// emojiElement consumes one element starting at rs[i] and returns the index after it.
func emojiElement(rs []rune, i int) (int, bool) {
	r := rs[i]
	switch {
	case isRegionalIndicator(r):
		i++
		if i < len(rs) && isRegionalIndicator(rs[i]) {
			i++
		}
		return i, true
	case isKeycapBase(r):
		i++
		if i < len(rs) && rs[i] == vs16 {
			i++
		}
		if i < len(rs) && rs[i] == keycap {
			return i + 1, true
		}
		return 0, false
	case isEmojiSymbol(r):
		i++
	default:
		return 0, false
	}
	for i < len(rs) && (rs[i] == vs15 || rs[i] == vs16 || isSkinTone(rs[i]) || isTag(rs[i])) {
		i++
	}
	return i, true
}

func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }
func isSkinTone(r rune) bool          { return r >= 0x1F3FB && r <= 0x1F3FF }
func isTag(r rune) bool               { return r >= 0xE0020 && r <= 0xE007F }

// isKeycapBase is a character that only forms an emoji with the keycap mark U+20E3.
func isKeycapBase(r rune) bool { return r == '#' || r == '*' || (r >= '0' && r <= '9') }

// isEmojiSymbol covers the pictographic and symbol blocks that emoji use (the BMP ones are listed, the rest is 1F000-1FAFF).
func isEmojiSymbol(r rune) bool {
	switch {
	case r == 0x00A9 || r == 0x00AE, r == 0x203C || r == 0x2049 || r == 0x2122 || r == 0x2139:
		return true
	case r >= 0x2194 && r <= 0x2199, r >= 0x21A9 && r <= 0x21AA, r >= 0x231A && r <= 0x231B:
		return true
	case r == 0x2328 || r == 0x23CF || r >= 0x23E9 && r <= 0x23F3 || r >= 0x23F8 && r <= 0x23FA:
		return true
	case r == 0x24C2 || r >= 0x25AA && r <= 0x25AB || r == 0x25B6 || r == 0x25C0 || r >= 0x25FB && r <= 0x25FE:
		return true
	case r >= 0x2600 && r <= 0x27BF, r >= 0x2934 && r <= 0x2935, r >= 0x2B05 && r <= 0x2B07:
		return true
	case r >= 0x2B1B && r <= 0x2B1C, r == 0x2B50 || r == 0x2B55, r == 0x3030 || r == 0x303D || r == 0x3297 || r == 0x3299:
		return true
	case r >= 0x1F000 && r <= 0x1FAFF:
		return true
	}
	return false
}
