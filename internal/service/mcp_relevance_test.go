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
		{"name", func(m *model.MCP) { m.Name = "issue tracker" }, "name", 8},
		{"slogan", func(m *model.MCP) { m.Slogan = "manage issue lifecycles" }, "description", 2},
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

// Tags moved to the dedicated tag chip filter — they no longer participate
// in keyword search. Tool names / descriptions and usage_examples remain
// excluded (never rendered as free text on the card). See enrichListItem
// doc for context.
func TestEnrichListItemIgnoresTagsToolsAndUsageExamples(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*model.MCP)
	}{
		{"tag", func(m *model.MCP) { m.Tags = []string{"issue"} }},
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
