package content_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/content"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
)

// The body rules run before any query, so a nil database is enough: reaching it would panic.
func TestAT08_SaveRejectsBadBodiesBeforeTheDatabase(t *testing.T) {
	svc := content.New(nil, clock.System{}, uuid.New())

	var tooLarge *apperr.PayloadTooLargeError
	if _, err := svc.Save(t.Context(), uuid.New(), 0, strings.Repeat("a", content.MaxBodyBytes+1)); !errors.As(err, &tooLarge) {
		t.Errorf("1 MiB + 1 byte: err = %v, want PayloadTooLarge", err)
	}

	for name, body := range map[string]string{"invalid UTF-8": "a\xffb", "NUL": "a\x00b"} {
		var invalid *apperr.ValidationError
		if _, err := svc.Save(t.Context(), uuid.New(), 0, body); !errors.As(err, &invalid) || invalid.Errors[0].Field != "body" {
			t.Errorf("%s: err = %v, want a ValidationError on body", name, err)
		}
	}
}
