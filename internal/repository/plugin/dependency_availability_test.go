package plugin

import (
	"strings"
	"testing"
)

func TestAvailabilityAliasRejectsUnknown(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("unknown source alias must panic instead of being concatenated")
		}
	}()
	availabilityAlias("p; DROP TABLE plugins").sql()
}

func TestSnapshotAvailabilityUsesPinnedMemberVersion(t *testing.T) {
	snapshot := unavailableSnapshotSQL("p.plugin_id", "p.published_version_id")
	if !strings.Contains(snapshot, "JSON_TABLE(source_version.relations_json") {
		t.Fatalf("root snapshot not read: %s", snapshot)
	}
	if !strings.Contains(snapshot, "member_version.version_id=COALESCE(") {
		t.Fatalf("team member pin not carried: %s", snapshot)
	}
	if !strings.Contains(snapshot, "COALESCE(member_edge.relation_type,member_edge.relation_type_legacy) IN ('expert_skill','expert_connector')") {
		t.Fatalf("member snapshot edges not checked: %s", snapshot)
	}
}
