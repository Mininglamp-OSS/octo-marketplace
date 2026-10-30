package plugin

import (
	"encoding/json"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"testing"
)

func TestOwnerListDTOExposesSeparateReleaseWithoutReplacingDraft(t *testing.T) {
	draft, draftID, release, releaseID := "1.0.0", "draft", "1.0.3", "release"
	p := model.Plugin{CurrentVersion: &draft, CurrentVersionID: &draftID, PublishedVersion: &release, PublishedVersionID: &releaseID}
	data, err := json.Marshal(listItemDTO(&p))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	if out["current_version"] != "1.0.0" || out["published_version"] != "1.0.3" || out["published_version_id"] != "release" {
		t.Fatalf("%s", data)
	}
	data, _ = json.Marshal(listItemDTO(&model.Plugin{}))
	out = map[string]any{}
	_ = json.Unmarshal(data, &out)
	if _, ok := out["published_version"]; ok {
		t.Fatal("unpublished release must be absent")
	}
}
