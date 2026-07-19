package skill

import (
	"encoding/json"
	"testing"
)

func TestSkillItem_HidesInternalMetadata(t *testing.T) {
	raw, err := json.Marshal(SkillItem{
		ID:          "skill-1",
		OwnerID:     "user-1",
		CreatorID:   "bot-1",
		CreatorName: "Release Bot",
		SpaceID:     "space-1",
		FileURL:     "skills/skill-1/archive.zip",
		FileSHA256:  "secret-digest",
	})
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"owner_id", "space_id", "file_url", "file_sha256"} {
		if _, ok := got[field]; ok {
			t.Fatalf("internal field %q leaked in response: %s", field, raw)
		}
	}
	if got["skill_id"] != "skill-1" {
		t.Fatalf("skill_id missing from response: %s", raw)
	}
	if got["creator_id"] != "bot-1" || got["creator_name"] != "Release Bot" {
		t.Fatalf("creator fields missing from response: %s", raw)
	}
}
