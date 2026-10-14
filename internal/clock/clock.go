// Package clock provides the injectable time source (ARCHITECTURE §2 Rules).
// All timestamps are UTC.
package clock

import "time"

// Clock tells the current time. Services take a Clock so tests can fix it.
type Clock interface {
	Now() time.Time
}

// System is the real clock.
type System struct{}

// Now returns the current UTC time.
func (System) Now() time.Time { return time.Now().UTC() }
