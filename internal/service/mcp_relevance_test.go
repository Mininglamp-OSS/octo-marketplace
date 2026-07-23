package service

import (
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestEnrichListItemCoversAllSearchableFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*model.MCP)
		reason string
		score  int
	}{
		{"category", func(m *model.MCP) { m.Category = "issue" }, "category", 3},
		{"creator", func(m *model.MCP) { m.CreatorName = "issue team" }, "creator:issue team", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &model.MCP{}
			tc.mutate(m)
			item := model.ListItem{}
			enrichListItem(&item, m, "issue", "u")
			if item.Relevance != tc.score || len(item.MatchReasons) != 1 || item.MatchReasons[0] != tc.reason {
				t.Fatalf("item=%+v", item)
			}
		})
	}
}

// Tool names / descriptions and usage_examples are no longer part of keyword
// search — product decision to restrict matches to card-visible fields.
// See enrichListItem doc for context.
func TestEnrichListItemIgnoresToolsAndUsageExamples(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*model.MCP)
	}{
		{"tool name", func(m *model.MCP) { m.Tools = []model.Tool{{Name: "issue"}} }},
		{"tool description", func(m *model.MCP) { m.Tools = []model.Tool{{Name: "create", Description: "issue helper"}} }},
		{"usage_example", func(m *model.MCP) { m.UsageExamples = []string{"create an issue"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &model.MCP{}
			tc.mutate(m)
			item := model.ListItem{}
			enrichListItem(&item, m, "issue", "u")
			if item.Relevance != 0 || len(item.MatchReasons) != 0 {
				t.Fatalf("expected no match; item=%+v", item)
			}
		})
	}
}
