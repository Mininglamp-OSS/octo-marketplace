package repository

import (
	"reflect"
	"strings"
	"testing"
)

func TestListFilterCategoryORTransportAND(t *testing.T) {
	where, args := (ListFilter{CallerUID: "u1", SpaceID: "s1", Categories: []string{"dev", "search"}, Transports: []string{"stdio"}}).buildWhere()
	if strings.Count(where, "category IN (?,?)") != 1 || strings.Contains(where, "category = ?") {
		t.Fatalf("category predicate must be one OR set: %s", where)
	}
	if !strings.Contains(where, "transport IN (?)") {
		t.Fatalf("transport must be combined with AND: %s", where)
	}
	want := []any{"s1", "u1", "dev", "search", "stdio"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestRelevanceOrderCoversEverySearchableField(t *testing.T) {
	order, args := relevanceOrder("issue")
	for _, field := range []string{"name LIKE", "slogan LIKE", "category LIKE", "tags_json", "tools_json", "usage_examples_json", "creator_name LIKE"} {
		if !strings.Contains(order, field) {
			t.Fatalf("ranking omits %s: %s", field, order)
		}
	}
	if len(args) != 7 {
		t.Fatalf("ranking args = %d, want 7", len(args))
	}
}

// TestSourceMineExcludesSystemRows guards the filter/label consistency:
// source=mine must never surface a system-visibility row (which enrichment
// would relabel as source=system), even when owner_uid matches the caller.
func TestSourceMineExcludesSystemRows(t *testing.T) {
	where, args := (ListFilter{CallerUID: "u1", SpaceID: "s1", Sources: []string{"mine"}}).buildWhere()
	if !strings.Contains(where, "owner_uid = ? AND visibility <> 'system'") {
		t.Fatalf("source=mine must exclude system-visibility rows: %s", where)
	}
	want := []any{"s1", "u1", "u1"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

// TestSourceSpaceExcludesCallerOwnedRows guards the filter/label consistency:
// enrichListItem classifies OwnerUID==callerUID as source=mine (checked before
// space), so source=space must exclude caller-owned rows to partition the set
// the way the projection does.
func TestSourceSpaceExcludesCallerOwnedRows(t *testing.T) {
	where, args := (ListFilter{CallerUID: "u1", SpaceID: "s1", Sources: []string{"space"}}).buildWhere()
	if !strings.Contains(where, "space_id = ? AND owner_uid <> ?") {
		t.Fatalf("source=space must exclude caller-owned rows: %s", where)
	}
	want := []any{"s1", "u1", "s1", "u1"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}
