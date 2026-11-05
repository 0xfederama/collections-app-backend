package items

import (
	"errors"
	"slices"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

// F026-B1: the availability filters over stored streaming data.
func TestF026B1_QueryAvailability(t *testing.T) {
	all := []Item{
		mkItem(1, "Subscription", `{"streaming":{"country":"IT","platforms":["Netflix"]}}`),
		mkItem(2, "Rent only", `{"streaming":{"country":"IT","platforms":["Apple TV Store"],"paid_platforms":["Apple TV Store"]}}`),
		mkItem(3, "Mixed", `{"streaming":{"country":"IT","platforms":["Amazon Prime Video with Ads","Google Play"],"paid_platforms":["Google Play"]}}`),
		mkItem(4, "Nothing", `{"streaming":{"country":"IT","platforms":[]}}`),
		mkItem(5, "No streaming", ""),
		mkItem(6, "Unknown service", `{"streaming":{"country":"IT","platforms":["Some Local TV"]}}`),
		mkItem(7, "Another Netflix", `{"streaming":{"country":"IT","platforms":["Netflix"]}}`),
	}
	tests := []struct {
		name  string
		owned []string
		p     ListParams
		want  []int
		total int
	}{
		{"available, none owned: any service", nil, ListParams{Availability: AvailabilityAvailable}, []int{1, 2, 3, 6, 7}, 5},
		{"included, none owned", nil, ListParams{Availability: AvailabilityIncluded}, []int{1, 3, 6, 7}, 4},
		{"available, owned via alias", []string{"primevideo"}, ListParams{Availability: AvailabilityAvailable}, []int{3}, 1},
		{"available, owned paid service", []string{"googleplay"}, ListParams{Availability: AvailabilityAvailable}, []int{3}, 1},
		{"included, owned only as paid", []string{"googleplay", "appletvstore"}, ListParams{Availability: AvailabilityIncluded}, []int{}, 0},
		{"unknown names never match owned", []string{"netflix"}, ListParams{Availability: AvailabilityAvailable}, []int{1, 7}, 2},
		{"combines with q", nil, ListParams{Availability: AvailabilityAvailable, Q: "netflix"}, []int{7}, 1},
		{"combines with sort and paging", nil, ListParams{Availability: AvailabilityAvailable, Sort: SortTitle, Limit: 2, Offset: 1}, []int{3, 2}, 5},
		{"combines with a field filter", nil, ListParams{Availability: AvailabilityIncluded, FilterField: "streaming", FilterValue: "ads"}, []int{3}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defs := movieDefs
			defs.Owned = tt.owned
			page, total, err := Query(all, &movie, defs, tt.p)
			if err != nil {
				t.Fatal(err)
			}
			if got := nums(page); !slices.Equal(got, tt.want) || total != tt.total {
				t.Errorf("got %v (total %d), want %v (total %d)", got, total, tt.want, tt.total)
			}
		})
	}
}

func TestF026B1_AvailabilityOnlyOnMovieAndTV(t *testing.T) {
	book := types.Book
	for _, typ := range []*string{nil, &book} {
		if _, _, err := Query(nil, typ, untyped, ListParams{Availability: AvailabilityAvailable}); !errors.Is(err, ErrInvalidAvailability) {
			t.Errorf("type %v: err = %v, want ErrInvalidAvailability", typ, err)
		}
	}
	tv := types.TVShow
	if _, _, err := Query(nil, &tv, movieDefs, ListParams{Availability: AvailabilityIncluded}); err != nil {
		t.Errorf("tv: %v", err)
	}
}

func TestF026B1_ServiceKey(t *testing.T) {
	for name, want := range map[string]string{
		"Netflix": "netflix", "Amazon Prime Video with Ads": "primevideo", "Disney Plus": "disneyplus",
		"Max": "hbomax", "Apple TV+": "appletv", "NOW TV": "now", "Some Local TV": "somelocaltv",
	} {
		if got := serviceKey(name); got != want {
			t.Errorf("serviceKey(%q) = %q, want %q", name, got, want)
		}
	}
}
