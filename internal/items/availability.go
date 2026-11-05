package items

import "strings"

// Availability filter values of listItems (F-026 B1).
const (
	// AvailabilityAvailable keeps items with at least one streaming platform
	// on an owned service (any service when none is owned).
	AvailabilityAvailable = "available"
	// AvailabilityIncluded is AvailabilityAvailable restricted to platforms
	// that are not paid: subscription, free or ad-supported, no rent or buy.
	AvailabilityIncluded = "included"
)

// streamingServices maps the provider keys of owned_streaming_services to the
// names TMDB stores on items. It mirrors the streaming entries of the web
// registry (frontend-web src/components/serviceIcons/streamingEntries.ts):
// key, label and aliases, all compared through normalizeServiceName. The web
// owns the list; a key added there and not here matches only its own name.
var streamingServices = map[string][]string{
	"netflix":       {"Netflix"},
	"primevideo":    {"Prime Video", "amazonprimevideo", "amazonprimevideowithads", "amazonprimevideofreewithads"},
	"amazonvideo":   {"Amazon Video"},
	"disneyplus":    {"Disney+", "disney", "disney+"},
	"appletv":       {"Apple TV+", "appletv+", "appletvplus", "appletvamazonchannel"},
	"appletvstore":  {"Apple TV Store"},
	"youtube":       {"YouTube"},
	"timvision":     {"TIMvision"},
	"infinity":      {"Infinity", "mediasetinfinity"},
	"paramountplus": {"Paramount+", "paramountplus"},
	"hbomax":        {"HBO Max", "max", "hbomax", "hbomaxamazonchannel"},
	"now":           {"NOW", "nowtv"},
	"sky":           {"Sky", "skygo", "skyitalia"},
	"raiplay":       {"RaiPlay"},
	"rakutentv":     {"Rakuten TV", "rakuten"},
	"hulu":          {"Hulu"},
	"discoveryplus": {"Discovery+", "discoveryplus"},
	"mubi":          {"MUBI"},
	"googleplay":    {"Google Play", "googleplaymovies", "googleplaybooks"},
	"peacock":       {"Peacock", "peacockpremium"},
	"crunchyroll":   {"Crunchyroll"},
	"chili":         {"Chili"},
}

// serviceByName is every normalised key, label and alias mapped to its key.
var serviceByName = func() map[string]string {
	m := make(map[string]string)
	for key, names := range streamingServices {
		m[normalizeServiceName(key)] = key
		for _, n := range names {
			m[normalizeServiceName(n)] = key
		}
	}
	return m
}()

// normalizeServiceName is the web's rule: lower case, keep only a-z, 0-9 and +.
func normalizeServiceName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '+' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// serviceKey is the provider key of a stored platform name; an unknown name is
// its normalised self, which no valid owned key can equal unless it is one.
func serviceKey(name string) string {
	n := normalizeServiceName(name)
	if key, ok := serviceByName[n]; ok {
		return key
	}
	return n
}

// matchesAvailability reports whether the item's stored streaming value has a
// platform that passes the filter. owned empty means every service; otherwise
// a platform counts only when its name resolves to an owned key, as the web
// shows it (filterOwnedPlatforms).
func matchesAvailability(v any, mode string, owned map[string]bool) bool {
	obj, ok := v.(map[string]any)
	if !ok {
		return false
	}
	paid := map[string]bool{}
	if mode == AvailabilityIncluded {
		for _, name := range stringsOf(obj["paid_platforms"]) {
			paid[normalizeServiceName(name)] = true
		}
	}
	for _, name := range stringsOf(obj["platforms"]) {
		if strings.TrimSpace(name) == "" || paid[normalizeServiceName(name)] {
			continue
		}
		if len(owned) == 0 || owned[serviceKey(name)] {
			return true
		}
	}
	return false
}

func stringsOf(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
