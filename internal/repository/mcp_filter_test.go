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
	for _, field := range []string{"name LIKE", "slogan LIKE", "category LIKE", "tags_json", "creator_name LIKE"} {
		if !strings.Contains(order, field) {
			t.Fatalf("ranking omits %s: %s", field, order)
		}
	}
	// Tool names / descriptions and usage_examples are intentionally excluded
	// from keyword search (search only matches card-visible fields).
	for _, field := range []string{"tools_json", "usage_examples_json"} {
		if strings.Contains(order, field) {
			t.Fatalf("ranking must not include %s (not card-visible): %s", field, order)
		}
	}
	if len(args) != 5 {
		t.Fatalf("ranking args = %d, want 5 (name/slogan/category/tags/creator)", len(args))
	}
}

// TestKeywordSearchIsCaseInsensitiveOnJSONColumns guards the fix for yujiawei P1:
// JSON_SEARCH uses binary collation on JSON columns, so the SQL side was
// case-sensitive while enrichListItem was case-insensitive — mixed-case tags
// were silently dropped from the result set. The keyword clause and relevance
// ranking must both lowercase the JSON side (via LOWER(CAST(... AS CHAR))) and
// match against a lowercased keyword. tools_json / usage_examples_json used to
// be part of this contract; both are excluded from keyword search now (search
// only matches card-visible fields) so the test asserts only the tags side.
func TestKeywordSearchIsCaseInsensitiveOnJSONColumns(t *testing.T) {
	where, args := (ListFilter{CallerUID: "u1", SpaceID: "s1", Keyword: "GitHub"}).buildWhere()
	if !strings.Contains(where, "LOWER(CAST(tags_json AS CHAR)) LIKE ?") {
		t.Fatalf("keyword clause missing case-insensitive tags_json match: %s", where)
	}
	for _, needle := range []string{
		"JSON_EXTRACT(tools_json",
		"usage_examples_json",
	} {
		if strings.Contains(where, needle) {
			t.Fatalf("keyword clause must not include %s (not card-visible): %s", needle, where)
		}
	}
	if strings.Contains(where, "JSON_SEARCH") {
		t.Fatalf("keyword clause must not use case-sensitive JSON_SEARCH: %s", where)
	}
	// keyword args must be lowercased; args = [space_id, caller_uid, 5x like]
	if len(args) < 3 {
		t.Fatalf("args too short: %#v", args)
	}
	kwArg, ok := args[2].(string)
	if !ok || !strings.Contains(kwArg, "github") || strings.Contains(kwArg, "GitHub") {
		t.Fatalf("keyword arg must be lowercased, got %q", kwArg)
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
