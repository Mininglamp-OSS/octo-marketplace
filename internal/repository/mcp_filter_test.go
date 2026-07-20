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
