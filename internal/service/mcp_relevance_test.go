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
		{"tool description", func(m *model.MCP) { m.Tools = []model.Tool{{Name: "create", Description: "issue helper"}} }, "tool:create", 7},
		{"usage", func(m *model.MCP) { m.UsageExamples = []string{"create an issue"} }, "usage_example", 1},
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
