package workspace

// Settings is the workspace_settings row of the workspace. Version is
// workspace_settings.version, the single ETag stream "s-N" (API.md §3).
// TrashRetentionDays nil means off: the trash is never purged automatically (F012-B3).
// PreferredStore is the one default store key (F-016, 0.13.0); never empty.
// OwnedStreamingServices are the provider keys the user subscribes to (F-022);
// empty means every service is shown. Never nil.
type Settings struct {
	StreamingCountryCode   string
	MetadataLanguage       string
	MarkdownHistoryLimit   int
	TrashRetentionDays     *int
	PreferredStore         string
	OwnedStreamingServices []string
	Version                int
}

// SettingsPatch holds the fields a PATCH supplied; nil means "leave as is".
// TrashRetentionDays is an Optional: absent leaves it, an explicit null turns it off.
type SettingsPatch struct {
	StreamingCountryCode *string
	MetadataLanguage     *string
	MarkdownHistoryLimit *int
	TrashRetentionDays   Optional[int]
	PreferredStore       *string
	// OwnedStreamingServices replaces the list when set; an empty slice clears it.
	OwnedStreamingServices *[]string
}

// Optional is a PATCH field that can be cleared: Set tells absent from present,
// and a nil Value means an explicit null.
type Optional[T any] struct {
	Set   bool
	Value *T
}
