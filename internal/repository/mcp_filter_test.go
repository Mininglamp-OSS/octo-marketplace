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
	for _, field := range []string{"name LIKE", "slogan LIKE", "category LIKE", "creator_name LIKE"} {
		if !strings.Contains(order, field) {
			t.Fatalf("ranking omits %s: %s", field, order)
		}
	}
	// Tags moved to the dedicated tag chip filter — keyword ranking must NOT
	// double-count them. Tool names / descriptions and usage_examples remain
	// excluded (never card-visible free text).
	for _, field := range []string{"tags_json", "tools_json", "usage_examples_json"} {
		if strings.Contains(order, field) {
			t.Fatalf("ranking must not include %s (not part of keyword search): %s", field, order)
		}
	}
	if len(args) != 4 {
		t.Fatalf("ranking args = %d, want 4 (name/slogan/category/creator)", len(args))
	}
}

// TestKeywordSearchIsCaseInsensitive guards the fix for yujiawei P1: mixed-case
// keywords must be lowercased before the LIKE match so results agree between
// SQL and the Go-side enrichListItem (which lowercases both sides). Previously
// this test also asserted case-insensitivity on tags_json — tags are now owned
// by the dedicated tag chip filter and no longer participate in keyword search.
func TestKeywordSearchIsCaseInsensitive(t *testing.T) {
	where, args := (ListFilter{CallerUID: "u1", SpaceID: "s1", Keyword: "GitHub"}).buildWhere()
	for _, needle := range []string{
		"tags_json",
		"JSON_EXTRACT(tools_json",
		"usage_examples_json",
	} {
		if strings.Contains(where, needle) {
			t.Fatalf("keyword clause must not include %s (not part of keyword search): %s", needle, where)
		}
	}
	if strings.Contains(where, "JSON_SEARCH") {
		t.Fatalf("keyword clause must not use case-sensitive JSON_SEARCH: %s", where)
	}
	// keyword args must be lowercased; args = [space_id, caller_uid, 4x like]
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

// TestTagFilterIsAND asserts multi-tag selection intersects rather than unions
// (matches dmworkskillmarket's tag semantics — mcp-v1.md §4.2). A row must
// carry every selected tag to survive the filter.
func TestTagFilterIsAND(t *testing.T) {
	where, args := (ListFilter{CallerUID: "u1", SpaceID: "s1", Tags: []string{"official", "featured"}}).buildWhere()
	if strings.Contains(where, "JSON_CONTAINS(tags_json, JSON_QUOTE(?)) OR JSON_CONTAINS(tags_json, JSON_QUOTE(?))") {
		t.Fatalf("tag filter must AND-combine, not OR-combine: %s", where)
	}
	if !strings.Contains(where, "JSON_CONTAINS(tags_json, JSON_QUOTE(?)) AND JSON_CONTAINS(tags_json, JSON_QUOTE(?))") {
		t.Fatalf("tag filter must AND-combine JSON_CONTAINS clauses: %s", where)
	}
	want := []any{"s1", "u1", "official", "featured"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}
