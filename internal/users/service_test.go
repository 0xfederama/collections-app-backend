package users

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

type fakeStore struct {
	rows              map[string]User
	byEmail, upserted int
}

func (f *fakeStore) ByEmail(_ context.Context, email string) (User, error) {
	f.byEmail++
	if u, ok := f.rows[email]; ok {
		return u, nil
	}
	return User{}, apperr.NotFound("user")
}

func (f *fakeStore) Upsert(_ context.Context, email string) (User, error) {
	f.upserted++
	u := User{ID: uuid.New(), Email: email}
	f.rows[email] = u
	return u, nil
}

func (f *fakeStore) Get(_ context.Context, id uuid.UUID) (User, error) {
	for _, u := range f.rows {
		if u.ID == id {
			return u, nil
		}
	}
	return User{}, apperr.NotFound("user")
}

func (f *fakeStore) List(context.Context) ([]User, error) {
	out := make([]User, 0, len(f.rows))
	for _, u := range f.rows {
		out = append(out, u)
	}
	return out, nil
}

func (f *fakeStore) UpdateProfile(_ context.Context, id uuid.UUID, p Profile, at time.Time) (User, error) {
	for e, u := range f.rows {
		if u.ID == id {
			u.Name, u.AvatarURL, u.ProfileFetchedAt = p.Name, p.AvatarURL, &at
			f.rows[e] = u
			return u, nil
		}
	}
	return User{}, apperr.NotFound("user")
}

type clk struct{ t time.Time }

func (c *clk) Now() time.Time { return c.t }

func newSvc() (*Service, *fakeStore, *clk) {
	st := &fakeStore{rows: map[string]User{}}
	c := &clk{t: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	return NewService(st, c), st, c
}

func TestEnsureByEmail(t *testing.T) {
	ctx := scope.With(context.Background(), uuid.New())
	s, st, c := newSvc()

	u, err := s.EnsureByEmail(ctx, "  Alice@Example.TEST ")
	if err != nil || u.Email != "alice@example.test" {
		t.Fatalf("EnsureByEmail = %+v, %v", u, err)
	}
	again, _ := s.EnsureByEmail(ctx, "ALICE@example.test")
	if again.ID != u.ID || st.upserted != 1 {
		t.Fatalf("same person got a second row (upserts %d)", st.upserted)
	}
	if st.byEmail != 1 {
		t.Fatalf("second call missed the cache (%d reads)", st.byEmail)
	}
	c.t = c.t.Add(cacheTTL + time.Second)
	_, _ = s.EnsureByEmail(ctx, "alice@example.test")
	if st.byEmail != 2 || st.upserted != 1 {
		t.Fatalf("after the TTL: reads %d upserts %d", st.byEmail, st.upserted)
	}
}

func TestEnsureByEmailRejectsBadLengths(t *testing.T) {
	s, _, _ := newSvc()
	for _, e := range []string{"", "a@", strings.Repeat("a", 321)} {
		if _, err := s.EnsureByEmail(context.Background(), e); err == nil {
			t.Errorf("%q accepted", e)
		}
	}
}

func TestCacheIsBounded(t *testing.T) {
	ctx := scope.With(context.Background(), uuid.New())
	s, _, _ := newSvc()
	for i := range cacheMax * 3 {
		_, _ = s.EnsureByEmail(ctx, strings.Repeat("x", 3)+uuid.NewString()+"@t.test"+string(rune('a'+i%20)))
	}
	if len(s.cache) > cacheMax {
		t.Fatalf("cache holds %d entries, max %d", len(s.cache), cacheMax)
	}
}

func TestProfileDue(t *testing.T) {
	s, _, c := newSvc()
	id := uuid.New()
	old := c.t.Add(-25 * time.Hour)
	fresh := c.t.Add(-time.Hour)
	if s.ProfileDue(User{ID: uuid.New(), ProfileFetchedAt: &fresh}) {
		t.Error("a fresh profile is not due")
	}
	if !s.ProfileDue(User{ID: id}) {
		t.Fatal("a never fetched profile is due")
	}
	if s.ProfileDue(User{ID: id}) {
		t.Error("a second attempt within minutes must be refused")
	}
	c.t = c.t.Add(profileRetry + time.Second)
	if !s.ProfileDue(User{ID: id, ProfileFetchedAt: &old}) {
		t.Error("a day-old profile is due again after the retry gap")
	}
}

func TestSaveProfileUpdatesCache(t *testing.T) {
	ctx := scope.With(context.Background(), uuid.New())
	s, _, _ := newSvc()
	u, _ := s.EnsureByEmail(ctx, "a@example.test")
	name := "Alice"
	if err := s.SaveProfile(ctx, u.ID, Profile{Name: &name}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.EnsureByEmail(ctx, "a@example.test")
	if got.Name == nil || *got.Name != "Alice" || got.ProfileFetchedAt == nil {
		t.Fatalf("cached user = %+v", got)
	}
}
