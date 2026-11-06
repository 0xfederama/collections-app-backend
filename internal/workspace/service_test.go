package workspace_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

// fakeStore is an in-memory Store. Like the SQL, UpdateSettings bumps the
// version and a name change shows up in Get. Every write is counted.
type fakeStore struct {
	cur            workspace.Settings
	nameWrites     int
	settingsWrites int
	linkClears     int
	snapshotClears int
}

func newFakeStore() *fakeStore {
	return &fakeStore{cur: workspace.Settings{
		WorkspaceName: "Collections", StreamingCountryCode: "IT", MetadataLanguage: "it-IT",
		MarkdownHistoryLimit: 20, TrashRetentionDays: ptr(7), PreferredStore: "amazon", Version: 1,
	}}
}

func (f *fakeStore) InTx(_ context.Context, fn func(workspace.Repo) error) error { return fn(f) }

func (f *fakeStore) Get(context.Context) (workspace.Settings, error) { return f.cur, nil }

func (f *fakeStore) LockSettings(context.Context) (workspace.Settings, error) { return f.cur, nil }

func (f *fakeStore) UpdateWorkspaceName(_ context.Context, name string) error {
	f.nameWrites++
	f.cur.WorkspaceName = name
	return nil
}

func (f *fakeStore) UpdateSettings(_ context.Context, s workspace.Settings) error {
	f.settingsWrites++
	f.cur = s
	f.cur.Version++
	return nil
}

func (f *fakeStore) ClearItemStreamingLinks(context.Context) error {
	f.linkClears++
	return nil
}

func (f *fakeStore) ClearStreamingLinkSnapshots(context.Context) error {
	f.snapshotClears++
	return nil
}

// writes reports whether any write reached the store.
func (f *fakeStore) writes() bool {
	return f.nameWrites+f.settingsWrites+f.linkClears+f.snapshotClears > 0
}

func TestService_Update_FakeValidation(t *testing.T) {
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
		{"retention zero", workspace.SettingsPatch{TrashRetentionDays: workspace.Optional[int]{Set: true, Value: ptr(0)}}, []string{"trash_retention_days"}},
		{"retention too high", workspace.SettingsPatch{TrashRetentionDays: workspace.Optional[int]{Set: true, Value: ptr(366)}}, []string{"trash_retention_days"}},
		{"unknown preferred store", workspace.SettingsPatch{PreferredStore: ptr("audible")}, []string{"preferred_store"}},
		{"open library is not a store", workspace.SettingsPatch{PreferredStore: ptr("openlibrary")}, []string{"preferred_store"}},
		{"empty preferred store", workspace.SettingsPatch{PreferredStore: ptr("")}, []string{"preferred_store"}},
		{"every field reported", workspace.SettingsPatch{WorkspaceName: ptr(""), StreamingCountryCode: ptr("x"), MetadataLanguage: ptr("EN_us"), MarkdownHistoryLimit: ptr(999), PreferredStore: ptr("nope")},
			[]string{"workspace_name", "streaming_country_code", "metadata_language", "markdown_history_limit", "preferred_store"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeStore()
			_, err := workspace.NewService(f).Update(t.Context(), 1, tt.patch)
			var ve *apperr.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
			var got []string
			for _, fe := range ve.Errors {
				got = append(got, fe.Field)
			}
			if !reflect.DeepEqual(got, tt.wantFields) {
				t.Errorf("fields = %v, want %v", got, tt.wantFields)
			}
			if f.writes() {
				t.Error("a rejected patch reached the store")
			}
		})
	}
}

func TestService_Update_FakeStaleIfMatch(t *testing.T) {
	tests := []struct {
		name  string
		patch workspace.SettingsPatch
	}{
		{"a change", workspace.SettingsPatch{StreamingCountryCode: ptr("GB")}},
		{"an empty patch", workspace.SettingsPatch{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeStore()
			f.cur.Version = 2
			_, err := workspace.NewService(f).Update(t.Context(), 1, tt.patch)
			var vc *apperr.VersionConflictError
			if !errors.As(err, &vc) || vc.Current != 2 {
				t.Fatalf("err = %v, want VersionConflict with current 2", err)
			}
			if f.writes() {
				t.Error("a stale write reached the store")
			}
		})
	}
}

func TestService_Update_FakeUnchangedPatchIsNoWrite(t *testing.T) {
	tests := []struct {
		name  string
		patch workspace.SettingsPatch
	}{
		{"empty patch", workspace.SettingsPatch{}},
		{"same values", workspace.SettingsPatch{WorkspaceName: ptr(" Collections "), StreamingCountryCode: ptr("IT"), MetadataLanguage: ptr("it-IT")}},
		{"the stored preferred store", workspace.SettingsPatch{PreferredStore: ptr("amazon")}},
		{"the stored retention", workspace.SettingsPatch{TrashRetentionDays: workspace.Optional[int]{Set: true, Value: ptr(7)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeStore()
			want := f.cur
			got, err := workspace.NewService(f).Update(t.Context(), 1, tt.patch)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Update = %+v, want the current %+v", got, want)
			}
			if f.writes() {
				t.Errorf("a no-op patch wrote: name %d, settings %d, links %d, snapshots %d", f.nameWrites, f.settingsWrites, f.linkClears, f.snapshotClears)
			}
		})
	}
}

func TestService_Update_FakeWrites(t *testing.T) {
	tests := []struct {
		name          string
		patch         workspace.SettingsPatch
		wantName      string
		wantNameSaves int
		wantLinks     int // item and snapshot clears, each
	}{
		{"name change updates the workspace name", workspace.SettingsPatch{WorkspaceName: ptr("  Home  ")}, "Home", 1, 0},
		{"country change clears the links", workspace.SettingsPatch{StreamingCountryCode: ptr("US")}, "Collections", 0, 1},
		{"name and country change both apply", workspace.SettingsPatch{WorkspaceName: ptr("Home"), StreamingCountryCode: ptr("US")}, "Home", 1, 1},
		{"other fields keep the links", workspace.SettingsPatch{MarkdownHistoryLimit: ptr(5)}, "Collections", 0, 0},
		{"retention off keeps the links", workspace.SettingsPatch{TrashRetentionDays: workspace.Optional[int]{Set: true}}, "Collections", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeStore()
			got, err := workspace.NewService(f).Update(t.Context(), 1, tt.patch)
			if err != nil {
				t.Fatal(err)
			}
			if f.settingsWrites != 1 {
				t.Errorf("settings writes = %d, want 1", f.settingsWrites)
			}
			if f.nameWrites != tt.wantNameSaves {
				t.Errorf("workspace name writes = %d, want %d", f.nameWrites, tt.wantNameSaves)
			}
			if f.linkClears != tt.wantLinks || f.snapshotClears != tt.wantLinks {
				t.Errorf("link clears = %d items, %d snapshots; want %d each", f.linkClears, f.snapshotClears, tt.wantLinks)
			}
			if f.cur.WorkspaceName != tt.wantName {
				t.Errorf("stored name = %q, want %q", f.cur.WorkspaceName, tt.wantName)
			}
			if got.Version != 2 || !reflect.DeepEqual(got, f.cur) {
				t.Errorf("Update = %+v, stored %+v; want the stored settings at version 2", got, f.cur)
			}
		})
	}
}
