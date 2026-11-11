package users

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

const (
	// cacheTTL and cacheMax bound the e-mail to user cache: it only saves a query per request.
	cacheTTL = time.Minute
	cacheMax = 256
	// ProfileMaxAge is how old a profile may be before it is fetched again.
	ProfileMaxAge = 24 * time.Hour
	// profileRetry spaces the attempts of one user, so a failing Access endpoint is not called per request.
	profileRetry = 5 * time.Minute
	maxEmailLen  = 320
)

// Store is the persistence the rules need.
type Store interface {
	ByEmail(ctx context.Context, email string) (User, error)
	Upsert(ctx context.Context, email string) (User, error)
	Get(ctx context.Context, id uuid.UUID) (User, error)
	UpdateProfile(ctx context.Context, id uuid.UUID, p Profile, at time.Time) (User, error)
}

type cacheKey struct {
	ws    uuid.UUID
	email string
}

type cacheEntry struct {
	user    User
	expires time.Time
}

// Service holds the account rules. The cache and the attempt log are instance state, bounded.
type Service struct {
	store Store
	clk   clock.Clock

	mu       sync.Mutex
	cache    map[cacheKey]cacheEntry
	attempts map[uuid.UUID]time.Time
}

func NewService(store Store, clk clock.Clock) *Service {
	return &Service{store: store, clk: clk, cache: map[cacheKey]cacheEntry{}, attempts: map[uuid.UUID]time.Time{}}
}

// EnsureByEmail returns the user of email (lower-cased), creating it on first sight.
func (s *Service) EnsureByEmail(ctx context.Context, email string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if len(email) < 3 || len(email) > maxEmailLen {
		return User{}, apperr.Validation(apperr.FieldError{Field: "email", Message: "must be 3 to 320 characters"})
	}
	key := cacheKey{scope.WorkspaceID(ctx), email}
	now := s.clk.Now()
	s.mu.Lock()
	e, ok := s.cache[key]
	s.mu.Unlock()
	if ok && now.Before(e.expires) {
		return e.user, nil
	}
	u, err := s.store.ByEmail(ctx, email)
	var missing *apperr.NotFoundError
	if errors.As(err, &missing) {
		u, err = s.store.Upsert(ctx, email)
	}
	if err != nil {
		return User{}, err
	}
	s.remember(key, u, now)
	return u, nil
}

// Get returns one user by id.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (User, error) {
	return s.store.Get(ctx, id)
}

// ProfileDue reports whether u's profile should be fetched now: never fetched or older than
// ProfileMaxAge, and not tried in the last few minutes. A true answer counts as an attempt.
func (s *Service) ProfileDue(u User) bool {
	now := s.clk.Now()
	if u.ProfileFetchedAt != nil && now.Sub(*u.ProfileFetchedAt) < ProfileMaxAge {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.attempts[u.ID]; ok && now.Sub(last) < profileRetry {
		return false
	}
	if len(s.attempts) >= cacheMax {
		clear(s.attempts) // bounded: forgetting only allows an earlier retry
	}
	s.attempts[u.ID] = now
	return true
}

// SaveProfile stores a fetched profile and stamps profile_fetched_at.
func (s *Service) SaveProfile(ctx context.Context, id uuid.UUID, p Profile) error {
	u, err := s.store.UpdateProfile(ctx, id, p, s.clk.Now())
	if err != nil {
		return err
	}
	s.remember(cacheKey{scope.WorkspaceID(ctx), u.Email}, u, s.clk.Now())
	return nil
}

func (s *Service) remember(k cacheKey, u User, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.cache[k]; !ok && len(s.cache) >= cacheMax {
		clear(s.cache)
	}
	s.cache[k] = cacheEntry{user: u, expires: now.Add(cacheTTL)}
}
