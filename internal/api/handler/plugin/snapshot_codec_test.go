package plugin

import (
	"encoding/json"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestVersionAndReviewDTOsAcceptBothPersistedRelationFormats(t *testing.T) {
	relations := []model.PluginRelation{{ID: "relation", SourcePluginID: "expert", TargetPluginID: "skill", TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", SortOrder: 4, Data: json.RawMessage(`{"role":"reviewer"}`)}}
	legacy, err := json.Marshal(relations)
	if err != nil {
		t.Fatal(err)
	}
	stable, err := model.EncodeSnapshotRelations(relations)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []json.RawMessage{legacy, stable} {
		versions := versionRelationSlice(raw)
		if len(versions) != 1 || versions[0]["target_plugin_id"] != "skill" || versions[0]["relation_id"] != "relation" || versions[0]["relation_type"] != "expert_skill" {
			t.Fatalf("version relation lost: %+v", versions)
		}
		review := decodeFrozenRelations(raw)
		if len(review) != 1 || review[0].TargetPluginID != "skill" || review[0].RelationID != "relation" || review[0].RelationType != "expert_skill" || review[0].SortOrder != 4 {
			t.Fatalf("review relation lost: %+v", review)
		}
	}
}
