// Package users owns the accounts of F-027: one row per person who signed in through
// Cloudflare Access, keyed by lower-cased e-mail. The identity middleware creates them on
// first request; GET /me reads the caller's own row.
package users

import (
	"time"

	"github.com/google/uuid"
)

// User is one account. Name and AvatarURL are nil until the profile fetch finds them.
type User struct {
	ID               uuid.UUID
	Email            string
	Name             *string
	AvatarURL        *string
	ProfileFetchedAt *time.Time
}

// Profile is what the Access identity endpoint told about a user. Empty fields are nil.
type Profile struct {
	Name      *string
	AvatarURL *string
}
