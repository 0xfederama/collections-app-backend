package folders

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

func TestIsSelfOrAncestor(t *testing.T) {
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tree := []Folder{{ID: a}, {ID: b, ParentID: &a}, {ID: c, ParentID: &b}, {ID: d}}
	tests := []struct {
		name           string
		folder, target uuid.UUID
		want           bool
	}{
		{"itself", a, a, true},
		{"child", a, b, true},
		{"grandchild", a, c, true},
		{"sibling tree", a, d, false},
		{"parent as destination", c, a, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSelfOrAncestor(tree, tt.folder, tt.target); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsSelfOrAncestor_CorruptCycleTerminates(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	loop := []Folder{{ID: a, ParentID: &b}, {ID: b, ParentID: &a}}
	if isSelfOrAncestor(loop, uuid.New(), a) {
		t.Fatal("unrelated folder reported as ancestor")
	}
}

// fakeStore runs fn against a fake Repo; only what Rename touches is implemented.
type fakeStore struct {
	Repo
	folder Folder
}

func (s *fakeStore) InTx(_ context.Context, fn func(Repo) error) error { return fn(s) }
func (s *fakeStore) GetForUpdate(context.Context, uuid.UUID) (Folder, error) {
	return s.folder, nil
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Unix(0, 0).UTC() }

func TestRename_NilNameChangesNothing(t *testing.T) {
	st := &fakeStore{folder: Folder{ID: uuid.New(), Name: "A", Version: 3}}
	got, err := NewService(st, fixedClock{}).Rename(t.Context(), st.folder.ID, 3, nil)
	if err != nil || got.Version != 3 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestRename_StaleVersionAndBadName(t *testing.T) {
	st := &fakeStore{folder: Folder{ID: uuid.New(), Name: "A", Version: 3}}
	svc := NewService(st, fixedClock{})
	name := "B"
	_, err := svc.Rename(t.Context(), st.folder.ID, 2, &name)
	var vc *apperr.VersionConflictError
	if !errors.As(err, &vc) || vc.Current != 3 {
		t.Fatalf("err = %v, want version conflict at 3", err)
	}
	blank := "  "
	_, err = svc.Rename(t.Context(), st.folder.ID, 3, &blank)
	var ve *apperr.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want validation error", err)
	}
}
