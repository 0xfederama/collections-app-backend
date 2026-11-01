package workspace_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

func ptr[T any](v T) *T { return &v }

func TestService_Update(t *testing.T) {
	base := workspace.Settings{WorkspaceName: "Our Collection", StreamingCountryCode: "IT", MetadataLanguage: "it-IT", MarkdownHistoryLimit: 20, PreferredStore: "amazon", OwnedStreamingServices: []string{}, Version: 1}
	tests := []struct {
		name  string
		patch workspace.SettingsPatch
		want  workspace.Settings
	}{
		{"name is trimmed", workspace.SettingsPatch{WorkspaceName: ptr("  Home  ")}, with(base, func(s *workspace.Settings) { s.WorkspaceName = "Home"; s.Version = 2 })},
		{"country", workspace.SettingsPatch{StreamingCountryCode: ptr("US")}, with(base, func(s *workspace.Settings) { s.StreamingCountryCode = "US"; s.Version = 2 })},
		{"metadata language", workspace.SettingsPatch{MetadataLanguage: ptr("en-US")}, with(base, func(s *workspace.Settings) { s.MetadataLanguage = "en-US"; s.Version = 2 })},
		{"limit zero", workspace.SettingsPatch{MarkdownHistoryLimit: ptr(0)}, with(base, func(s *workspace.Settings) { s.MarkdownHistoryLimit = 0; s.Version = 2 })},
		{"limit max", workspace.SettingsPatch{MarkdownHistoryLimit: ptr(200)}, with(base, func(s *workspace.Settings) { s.MarkdownHistoryLimit = 200; s.Version = 2 })},
		{"preferred store", workspace.SettingsPatch{PreferredStore: ptr("kobo")}, with(base, func(s *workspace.Settings) { s.PreferredStore = "kobo"; s.Version = 2 })},
		{"the default preferred store is a no-op", workspace.SettingsPatch{PreferredStore: ptr("amazon")}, base},
		{"all fields bump once", workspace.SettingsPatch{WorkspaceName: ptr("X"), StreamingCountryCode: ptr("DE"), MarkdownHistoryLimit: ptr(5)},
			workspace.Settings{WorkspaceName: "X", StreamingCountryCode: "DE", MetadataLanguage: "it-IT", MarkdownHistoryLimit: 5, PreferredStore: "amazon", OwnedStreamingServices: []string{}, Version: 2}},
		{"empty patch is a no-op", workspace.SettingsPatch{}, base},
		{"same values are a no-op", workspace.SettingsPatch{WorkspaceName: ptr(" Our Collection "), StreamingCountryCode: ptr("IT"), MetadataLanguage: ptr("it-IT")}, base},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := newPool(t)
			id, err := workspace.Bootstrap(t.Context(), pool, base.WorkspaceName)
			if err != nil {
				t.Fatal(err)
			}
			svc := workspace.NewService(workspace.NewPostgres(pool))

			got, err := svc.Update(scope.With(t.Context(), id), 1, tt.patch)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Update = %+v, want %+v", got, tt.want)
			}
			stored, err := svc.Get(scope.With(t.Context(), id))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored, tt.want) {
				t.Errorf("Get = %+v, want %+v", stored, tt.want)
			}
			var wsName string
			if err := pool.QueryRow(t.Context(), "SELECT name FROM workspaces WHERE id = $1", id).Scan(&wsName); err != nil {
				t.Fatal(err)
			}
			if wsName != tt.want.WorkspaceName {
				t.Errorf("workspaces.name = %q, want %q", wsName, tt.want.WorkspaceName)
			}
		})
	}
}

func TestService_Update_Validation(t *testing.T) {
	long := make([]rune, 201)
	for i := range long {
		long[i] = 'a'
	}
	tests := []struct {
		name       string
		patch      workspace.SettingsPatch
		wantFields []string
	}{
		{"empty name", workspace.SettingsPatch{WorkspaceName: ptr("")}, []string{"workspace_name"}},
		{"blank name", workspace.SettingsPatch{WorkspaceName: ptr("   ")}, []string{"workspace_name"}},
		{"name too long", workspace.SettingsPatch{WorkspaceName: ptr(string(long))}, []string{"workspace_name"}},
		{"country lowercase", workspace.SettingsPatch{StreamingCountryCode: ptr("it")}, []string{"streaming_country_code"}},
		{"country three letters", workspace.SettingsPatch{StreamingCountryCode: ptr("ITA")}, []string{"streaming_country_code"}},
		{"language uppercase", workspace.SettingsPatch{MetadataLanguage: ptr("EN")}, []string{"metadata_language"}},
		{"language lowercase region", workspace.SettingsPatch{MetadataLanguage: ptr("it-it")}, []string{"metadata_language"}},
		{"language malformed", workspace.SettingsPatch{MetadataLanguage: ptr("eng")}, []string{"metadata_language"}},
		{"limit negative", workspace.SettingsPatch{MarkdownHistoryLimit: ptr(-1)}, []string{"markdown_history_limit"}},
		{"limit too high", workspace.SettingsPatch{MarkdownHistoryLimit: ptr(201)}, []string{"markdown_history_limit"}},
		{"unknown preferred store", workspace.SettingsPatch{PreferredStore: ptr("audible")}, []string{"preferred_store"}},
		{"open library is not a store", workspace.SettingsPatch{PreferredStore: ptr("openlibrary")}, []string{"preferred_store"}},
		{"empty preferred store", workspace.SettingsPatch{PreferredStore: ptr("")}, []string{"preferred_store"}},
		{"every field reported", workspace.SettingsPatch{WorkspaceName: ptr(""), StreamingCountryCode: ptr("x"), MetadataLanguage: ptr("EN_us"), MarkdownHistoryLimit: ptr(999), PreferredStore: ptr("nope")},
			[]string{"workspace_name", "streaming_country_code", "metadata_language", "markdown_history_limit", "preferred_store"}},
	}
	pool := newPool(t)
	id, err := workspace.Bootstrap(t.Context(), pool, "Our Collection")
	if err != nil {
		t.Fatal(err)
	}
	svc := workspace.NewService(workspace.NewPostgres(pool))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Update(scope.With(t.Context(), id), 1, tt.patch)
			var ve *apperr.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
			var got []string
			for _, fe := range ve.Errors {
				got = append(got, fe.Field)
			}
			if len(got) != len(tt.wantFields) {
				t.Fatalf("fields = %v, want %v", got, tt.wantFields)
			}
			for i := range got {
				if got[i] != tt.wantFields[i] {
					t.Errorf("fields = %v, want %v", got, tt.wantFields)
				}
			}
		})
	}
	got, err := svc.Get(scope.With(t.Context(), id))
	if err != nil || got.Version != 1 {
		t.Errorf("after rejected patches: %+v, %v; want version 1", got, err)
	}
}

func TestService_Update_StaleVersion(t *testing.T) {
	pool := newPool(t)
	id, err := workspace.Bootstrap(t.Context(), pool, "Our Collection")
	if err != nil {
		t.Fatal(err)
	}
	svc := workspace.NewService(workspace.NewPostgres(pool))
	if _, err := svc.Update(scope.With(t.Context(), id), 1, workspace.SettingsPatch{StreamingCountryCode: ptr("US")}); err != nil {
		t.Fatal(err)
	}

	// A stale version loses even when the patch would change nothing.
	for _, p := range []workspace.SettingsPatch{{StreamingCountryCode: ptr("GB")}, {}} {
		_, err := svc.Update(scope.With(t.Context(), id), 1, p)
		var vc *apperr.VersionConflictError
		if !errors.As(err, &vc) || vc.Current != 2 {
			t.Fatalf("err = %v, want VersionConflict with current 2", err)
		}
	}
	got, _ := svc.Get(scope.With(t.Context(), id))
	if got.StreamingCountryCode != "US" || got.Version != 2 {
		t.Errorf("stored = %+v, want the stale write ignored", got)
	}
}

// Two PATCHes with the same If-Match race: exactly one wins, the other gets a conflict.
func TestService_Update_ConcurrentSameVersion(t *testing.T) {
	pool := newPool(t)
	id, err := workspace.Bootstrap(t.Context(), pool, "Our Collection")
	if err != nil {
		t.Fatal(err)
	}
	svc := workspace.NewService(workspace.NewPostgres(pool))

	const racers = 6
	errs := make([]error, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = svc.Update(scope.With(t.Context(), id), 1, workspace.SettingsPatch{MarkdownHistoryLimit: ptr(i + 1)})
		}()
	}
	close(start)
	wg.Wait()

	wins := 0
	for _, err := range errs {
		var vc *apperr.VersionConflictError
		switch {
		case err == nil:
			wins++
		case errors.As(err, &vc) && vc.Current == 2:
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Errorf("winners = %d, want 1", wins)
	}
	if got, _ := svc.Get(scope.With(t.Context(), id)); got.Version != 2 {
		t.Errorf("version = %d, want 2", got.Version)
	}
}

func with(s workspace.Settings, f func(*workspace.Settings)) workspace.Settings {
	f(&s)
	return s
}

// F-022 B3: the owned streaming services are saved and returned in the order given.
func TestF022B3_OwnedStreamingServicesRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		keys []string
	}{
		{"keeps the order given", []string{"netflix", "primevideo", "appletv_plus2"}},
		{"one service", []string{"netflix"}},
		{"longest key", []string{"a" + strings.Repeat("b", 63)}},
		{"sixty four entries", keysN(64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := newPool(t)
			id, err := workspace.Bootstrap(t.Context(), pool, "Our Collection")
			if err != nil {
				t.Fatal(err)
			}
			svc := workspace.NewService(workspace.NewPostgres(pool))
			ctx := scope.With(t.Context(), id)

			got, err := svc.Update(ctx, 1, workspace.SettingsPatch{OwnedStreamingServices: &tt.keys})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.OwnedStreamingServices, tt.keys) || got.Version != 2 {
				t.Errorf("Update = %v at version %d, want %v at 2", got.OwnedStreamingServices, got.Version, tt.keys)
			}
			stored, err := svc.Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored.OwnedStreamingServices, tt.keys) {
				t.Errorf("Get = %v, want %v", stored.OwnedStreamingServices, tt.keys)
			}
		})
	}
}

// F-022 B3: a new workspace shows every service, so the list is [] and never nil.
func TestF022B3_OwnedStreamingServicesDefaultEmpty(t *testing.T) {
	pool := newPool(t)
	id, err := workspace.Bootstrap(t.Context(), pool, "Our Collection")
	if err != nil {
		t.Fatal(err)
	}
	got, err := workspace.NewService(workspace.NewPostgres(pool)).Get(scope.With(t.Context(), id))
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnedStreamingServices == nil || len(got.OwnedStreamingServices) != 0 {
		t.Errorf("OwnedStreamingServices = %#v, want an empty non-nil list", got.OwnedStreamingServices)
	}
}

// F-022 B3: an absent list is unchanged, a repeat is a no-op, [] clears it (a version bump).
func TestF022B3_OwnedStreamingServicesUnchangedRepeatClear(t *testing.T) {
	pool := newPool(t)
	id, err := workspace.Bootstrap(t.Context(), pool, "Our Collection")
	if err != nil {
		t.Fatal(err)
	}
	svc := workspace.NewService(workspace.NewPostgres(pool))
	ctx := scope.With(t.Context(), id)
	owned := []string{"netflix"}
	if _, err := svc.Update(ctx, 1, workspace.SettingsPatch{OwnedStreamingServices: &owned}); err != nil {
		t.Fatal(err)
	}

	// Absent: a country change leaves the list alone.
	if got, err := svc.Update(ctx, 2, workspace.SettingsPatch{StreamingCountryCode: ptr("DE")}); err != nil || !reflect.DeepEqual(got.OwnedStreamingServices, owned) {
		t.Fatalf("country only: %v, %v; want the list kept", got.OwnedStreamingServices, err)
	}
	// Repeat: the same list changes nothing, so no bump.
	if got, err := svc.Update(ctx, 3, workspace.SettingsPatch{OwnedStreamingServices: &[]string{"netflix"}}); err != nil || got.Version != 3 {
		t.Fatalf("repeat: version %d, %v; want 3", got.Version, err)
	}
	// Empty clears: back to show all.
	empty := []string{}
	got, err := svc.Update(ctx, 3, workspace.SettingsPatch{OwnedStreamingServices: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.OwnedStreamingServices) != 0 || got.Version != 4 {
		t.Errorf("clear = %v at version %d, want [] at 4", got.OwnedStreamingServices, got.Version)
	}
	stored, _ := svc.Get(ctx)
	if stored.OwnedStreamingServices == nil || len(stored.OwnedStreamingServices) != 0 {
		t.Errorf("stored = %#v, want an empty non-nil list", stored.OwnedStreamingServices)
	}
}

// F-022 B3: bad keys, duplicates and a list over 64 entries are 422 on the field,
// and nothing is stored.
func TestF022B3_OwnedStreamingServicesInvalid(t *testing.T) {
	tests := []struct {
		name string
		keys []string
	}{
		{"uppercase", []string{"Netflix"}},
		{"space", []string{"prime video"}},
		{"hyphen", []string{"prime-video"}},
		{"starts with a digit", []string{"1password"}},
		{"starts with an underscore", []string{"_netflix"}},
		{"empty key", []string{""}},
		{"key too long", []string{"a" + strings.Repeat("b", 64)}},
		{"duplicate", []string{"netflix", "primevideo", "netflix"}},
		{"one bad key among good ones", []string{"netflix", "Hulu"}},
		{"sixty five entries", keysN(65)},
	}
	pool := newPool(t)
	id, err := workspace.Bootstrap(t.Context(), pool, "Our Collection")
	if err != nil {
		t.Fatal(err)
	}
	svc := workspace.NewService(workspace.NewPostgres(pool))
	ctx := scope.With(t.Context(), id)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Update(ctx, 1, workspace.SettingsPatch{OwnedStreamingServices: &tt.keys})
			var ve *apperr.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
			if len(ve.Errors) != 1 || ve.Errors[0].Field != "owned_streaming_services" {
				t.Errorf("errors = %+v, want one on owned_streaming_services", ve.Errors)
			}
		})
	}
	got, err := svc.Get(ctx)
	if err != nil || got.Version != 1 || len(got.OwnedStreamingServices) != 0 {
		t.Errorf("after rejected lists: %+v, %v; want version 1 and no list", got, err)
	}
}

// keysN returns n distinct valid keys: s0, s1, ...
func keysN(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("s%d", i)
	}
	return keys
}
