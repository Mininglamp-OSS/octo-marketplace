package pinbackfill

import (
	"encoding/json"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/jcs"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestPlanSourceScopeIsTheCanonicalCutoverScope(t *testing.T) {
	doc := struct {
		Kind          string `json:"kind"`
		SchemaVersion string `json:"schema_version"`
	}{Kind: "pin-backfill", SchemaVersion: PlanSchemaVersion}
	got, err := jcs.Digest(doc)
	if err != nil {
		t.Fatal(err)
	}
	if got != PlanSourceScope {
		t.Fatalf("PlanSourceScope=%s, want %s", PlanSourceScope, got)
	}
}

func TestPlanDigestIsDeterministicAndBindsPins(t *testing.T) {
	pin := "skill-v1"
	plan := Plan{
		SchemaVersion: PlanSchemaVersion, SourceScope: PlanSourceScope,
		SourceIdentity: PlanSourceIdentity, BatchID: PlanBatchID,
		Sources: []SourcePlan{{
			PluginID: "expert", VersionID: "e1", Source: "expert@e1",
			PluginType: model.PluginTypeExpert, OwnerUID: "owner",
			Visibility: model.PluginVisibilityPrivate, EditRevision: 1,
			SnapshotDigest: "a", ExpectedSnapshotDigest: "b",
			Relations: []RelationChange{{
				RelationID: "edge", RelationType: "expert_skill", TargetPluginID: "skill",
				TargetVersionID: pin, Resolution: ResolutionCurrentRelease,
			}},
		}},
		Blocked: []BlockedSource{},
	}
	first, err := Digest(plan)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Digest(plan)
	if err != nil || first != second || len(first) != 64 {
		t.Fatalf("digest not stable: %s %s %v", first, second, err)
	}
	plan.Sources[0].Relations[0].TargetVersionID = "skill-v2"
	third, err := Digest(plan)
	if err != nil || third == first {
		t.Fatalf("pin change did not move the digest: %s %s %v", first, third, err)
	}
}

func TestEncodeSnapshotWithPinsPreservesFieldsAndLegacySpelling(t *testing.T) {
	raw := json.RawMessage(`[
		{"ID":"rel-old","SourcePluginID":"expert","TargetPluginID":"skill","TargetVersionID":null,"Type":"expert_skill","Status":1,"Extra":{"keep":true}},
		{"relation_id":"rel-new","source_plugin_id":"expert","target_plugin_id":"skill","target_version_id":"old","relation_type":"expert_skill","status":1}
	]`)
	rels, err := model.DecodeSnapshotRelations(raw)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := encodeSnapshotWithPins(raw, rels, map[string]string{"rel-old": "s1", "rel-new": "s2"})
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]json.RawMessage
	if err = json.Unmarshal(updated, &rows); err != nil || len(rows) != 2 {
		t.Fatalf("decode updated: %v rows=%d", err, len(rows))
	}
	if string(rows[0]["TargetVersionID"]) != `"s1"` {
		t.Fatalf("legacy alias pin = %s", rows[0]["TargetVersionID"])
	}
	if _, exists := rows[0]["target_version_id"]; exists {
		t.Fatal("legacy alias row gained a second pin spelling")
	}
	if string(rows[0]["Extra"]) != `{"keep":true}` {
		t.Fatalf("unknown field was rewritten: %s", rows[0]["Extra"])
	}
	if string(rows[0]["ID"]) != `"rel-old"` {
		t.Fatal("legacy relation ID spelling was lost")
	}
	if string(rows[1]["target_version_id"]) != `"s2"` {
		t.Fatalf("canonical pin = %s", rows[1]["target_version_id"])
	}
	if _, exists := rows[1]["TargetVersionID"]; exists {
		t.Fatal("canonical row gained a legacy pin spelling")
	}
}

func TestSnapshotDigestIgnoresWhitespaceAndObjectKeyOrder(t *testing.T) {
	a, err := snapshotDigest(json.RawMessage(`[{"relation_id":"r","target_version_id":"v"}]`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := snapshotDigest(json.RawMessage("[\n {\"target_version_id\":\"v\",\"relation_id\":\"r\"}\n]"))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("canonical digests differ: %s %s", a, b)
	}
}

func TestAudienceAllowsSystemTargetAndRejectsCrossSpacePrivate(t *testing.T) {
	space := "space-a"
	source := model.Plugin{ID: "expert", OwnerUID: "owner", SpaceID: &space, Visibility: model.PluginVisibilityPrivate}
	system := model.Plugin{ID: "skill", OwnerUID: "other", SpaceID: &space, Visibility: model.PluginVisibilitySystem}
	if !audienceAllows(source, system) {
		t.Fatal("system target must be visible to every source")
	}
	otherSpace := "space-b"
	cross := model.Plugin{ID: "skill", OwnerUID: "owner", SpaceID: &otherSpace, Visibility: model.PluginVisibilitySpace}
	if audienceAllows(source, cross) {
		t.Fatal("cross-Space target was allowed")
	}
	privateOther := model.Plugin{ID: "skill", OwnerUID: "other", SpaceID: &space, Visibility: model.PluginVisibilityPrivate}
	if audienceAllows(source, privateOther) {
		t.Fatal("another owner's private target was allowed")
	}
}
