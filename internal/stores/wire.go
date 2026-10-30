package stores

// StoreProvidersJSON is the listStoreProviders answer (published as "StoreProviders").
type StoreProvidersJSON struct {
	Providers []StoreProviderJSON `json:"providers" doc:"Every store, in the fixed display order."`
}

// StoreProviderJSON is one store (published as "StoreProvider"). Key is what a
// `stores` field or the `preferred_store` setting names.
type StoreProviderJSON struct {
	Key  string `json:"key" doc:"A store key, as a stores field or the preferred_store setting names it."`
	Name string `json:"name" doc:"Display name."`
}
