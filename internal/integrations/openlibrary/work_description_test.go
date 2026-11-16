package openlibrary

import (
	"net/http"
	"testing"
)

// F-028 (D-162): the work description, a string or an object {type, value}, from /works/{id}.json.

func TestF028B2_WorkDescriptionStringAndObject(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"string":     {`{"description":"A plain summary."}`, "A plain summary."},
		"object":     {`{"description":{"type":"/type/text","value":"An object summary."}}`, "An object summary."},
		"absent":     {`{"title":"No summary"}`, ""},
		"null":       {`{"description":null}`, ""},
		"other type": {`{"description":42}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			s, count := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/works/OL27482W.json" {
					t.Errorf("path = %s", r.URL.Path)
				}
				_, _ = w.Write([]byte(tc.body))
			})
			got, err := testClient(s, Options{}).WorkDescription(t.Context(), "OL27482W")
			if err != nil || got != tc.want || count.Load() != 1 {
				t.Fatalf("description = %q err = %v requests = %d, want %q", got, err, count.Load(), tc.want)
			}
		})
	}
}

func TestF028B2_WorkDescriptionRejectsBadIDAndMocksWithoutRequests(t *testing.T) {
	s, count := fakeServer(t, func(http.ResponseWriter, *http.Request) {
		t.Error("unexpected request")
	})
	c := testClient(s, Options{})
	if _, err := c.WorkDescription(t.Context(), "../etc"); err != ErrInvalid {
		t.Errorf("bad id error = %v, want ErrInvalid", err)
	}
	mock := testClient(s, Options{MockFixture: true})
	if got, err := mock.WorkDescription(t.Context(), mockWorkID); err != nil || got != mockSynopsis {
		t.Errorf("mock description = %q err = %v", got, err)
	}
	if count.Load() != 0 {
		t.Errorf("requests = %d, want none", count.Load())
	}
}

func TestF028B2_WorkDescriptionFailureIsAnError(t *testing.T) {
	s, _ := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})
	if _, err := testClient(s, Options{}).WorkDescription(t.Context(), "OL27482W"); err == nil {
		t.Fatal("a 404 work record was not an error")
	}
}
