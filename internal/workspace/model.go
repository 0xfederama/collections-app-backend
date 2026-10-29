package workspace

// Settings is the workspace name plus the workspace_settings row. Version is
// workspace_settings.version, the single ETag stream "s-N" (API.md §3).
// TrashRetentionDays nil means off: the trash is never purged automatically (F012-B3).
// PreferredStore is the one default store key (F-016, 0.13.0); never empty.
type Settings struct {
	WorkspaceName        string
	StreamingCountryCode string
	MetadataLanguage     string
	MarkdownHistoryLimit int
	TrashRetentionDays   *int
	PreferredStore       string
	Version              int
}

// SettingsPatch holds the fields a PATCH supplied; nil means "leave as is".
// TrashRetentionDays is an Optional: absent leaves it, an explicit null turns it off.
type SettingsPatch struct {
	WorkspaceName        *string
	StreamingCountryCode *string
	MetadataLanguage     *string
	MarkdownHistoryLimit *int
	TrashRetentionDays   Optional[int]
	PreferredStore       *string
}

// Optional is a PATCH field that can be cleared: Set tells absent from present,
// and a nil Value means an explicit null.
type Optional[T any] struct {
	Set   bool
	Value *T
}
