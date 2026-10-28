package plugin

import (
	"context"
	"encoding/json"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

type fakeTracker struct {
	typ, id string
}

func (f *fakeTracker) TrackInstall(_ context.Context, resourceType, resourceID string) error {
	f.typ, f.id = resourceType, resourceID
	return nil
}
func (f *fakeTracker) TrackDownload(_ context.Context, resourceType, resourceID string) error {
	f.typ, f.id = resourceType, resourceID
	return nil
}

func packageWith(attachments ...string) json.RawMessage {
	return json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[` + joinComma(attachments) + `]}`)
}
func joinComma(items []string) string {
	out := ""
	for i, item := range items {
		if i > 0 {
			out += ","
		}
		out += item
	}
	return out
}
func rawAtt(path, content string) string {
	quotedContent, _ := json.Marshal(content)
	return `{"path":"` + path + `","content_type":"raw","mime_type":"text/plain","raw_content":` + string(quotedContent) + `}`
}

func installFixture() *fakeStore {
	space := "space-a"
	skillPkg := packageWith(rawAtt("skill/ref.json", `{"file_name":"pack.zip","file_size":9,"files":["a.md"],"object_key":"plugins/space-a/attachments/skill.md","zip_object_key":"plugins/space-a/attachments/pack.zip"}`))
	expertPkg := packageWith(rawAtt("AGENTS.md", "do the work"), rawAtt("mcp.json", `{"mcpServers":{}}`))
	teamPkg := packageWith(rawAtt("AGENTS.md", "# Team\n\n## 协作方式\n1. first\n2. second"))
	return &fakeStore{
		plugins: map[string]*model.Plugin{
			"expert-1": {ID: "expert-1", Name: "Alice", Type: model.PluginTypeExpert, OwnerUID: "user-1", SpaceID: &space, Visibility: model.PluginVisibilitySpace, Manifest: json.RawMessage(`{"description":"expert summary"}`), Package: expertPkg},
			"team-1":   {ID: "team-1", Name: "Team", Type: model.PluginTypeExpertTeam, OwnerUID: "user-1", SpaceID: &space, Visibility: model.PluginVisibilitySpace, Manifest: json.RawMessage(`{"description":"team summary"}`), Package: teamPkg},
			"skill-1":  {ID: "skill-1", Name: "Deploy", Type: model.PluginTypeSkill, OwnerUID: "user-1", SpaceID: &space, Visibility: model.PluginVisibilitySpace, Manifest: json.RawMessage(`{}`), Package: skillPkg},
		},
		relations: map[string][]model.PluginRelation{
			"expert-1": {{ID: "r1", SourcePluginID: "expert-1", TargetPluginID: "skill-1", TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", SortOrder: 0, Status: 1}},
			"team-1":   {{ID: "r2", SourcePluginID: "team-1", TargetPluginID: "expert-1", TargetPluginType: model.PluginTypeExpert, Type: "expert_team_expert", SortOrder: 0, Status: 1, Data: json.RawMessage(`{"member_key":"m1","role":"leader","is_leader":true}`)}},
		},
	}
}
