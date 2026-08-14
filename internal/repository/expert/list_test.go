package expert

import (
	"strings"
	"testing"
)

// orderBy is the only piece of the list SQL that varies by sort mode; assert
// each mode ranks by the right column/expression and that unknown values fall
// back to creation-time ordering.
func TestOrderBy(t *testing.T) {
	cases := []struct {
		sort     string
		contains []string
		absent   []string
	}{
		{sort: "", contains: []string{"created_at DESC, id DESC"}, absent: []string{"resource_metrics"}},
		{sort: SortLatest, contains: []string{"created_at DESC, id DESC"}, absent: []string{"resource_metrics"}},
		{sort: "relevance", contains: []string{"created_at DESC, id DESC"}, absent: []string{"resource_metrics"}},
		{sort: SortUpdated, contains: []string{"updated_at DESC, id DESC"}, absent: []string{"resource_metrics"}},
		{sort: SortViews, contains: []string{"rm.view_count", "resource_type = 'expert'", "experts.id"}},
		{sort: SortInstalls, contains: []string{"rm.install_count", "resource_type = 'expert'"}},
		{sort: SortComprehensive, contains: []string{"rm.install_count", "* 5", "rm.view_count", "TIMESTAMPDIFF"}},
	}
	for _, tc := range cases {
		t.Run("sort="+tc.sort, func(t *testing.T) {
			got := ListFilter{Sort: tc.sort}.orderBy(EntityExpert)
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("orderBy(%q) = %q, missing %q", tc.sort, got, want)
				}
			}
			for _, ban := range tc.absent {
				if strings.Contains(got, ban) {
					t.Errorf("orderBy(%q) = %q, unexpectedly contains %q", tc.sort, got, ban)
				}
			}
		})
	}
}

// The squad table ranks against resource_type='squad' rows.
func TestOrderBySquadEntity(t *testing.T) {
	got := ListFilter{Sort: SortViews}.orderBy(EntitySquad)
	for _, want := range []string{"resource_type = 'squad'", "expert_squads.id"} {
		if !strings.Contains(got, want) {
			t.Errorf("orderBy = %q, missing %q", got, want)
		}
	}
}
