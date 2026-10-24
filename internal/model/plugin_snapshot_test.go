package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSnapshotRelationsStableAndLegacyCompatible(t *testing.T) {
	r := PluginRelation{ID: "relation-1", SourcePluginID: "expert-1", TargetPluginID: "skill-1", Type: "expert_skill", SortOrder: 2, Data: json.RawMessage(`{"role":"reviewer"}`), Status: 1}
	stable, err := EncodeSnapshotRelations([]PluginRelation{r})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stable), `"TargetPluginID"`) || !strings.Contains(string(stable), `"target_plugin_id":"skill-1"`) {
		t.Fatalf("unstable encoding: %s", stable)
	}
	legacy, err := json.Marshal([]PluginRelation{r})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []json.RawMessage{stable, legacy} {
		got, err := DecodeSnapshotRelations(raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != r.ID || got[0].TargetPluginID != r.TargetPluginID || got[0].Type != r.Type || got[0].SortOrder != r.SortOrder || string(got[0].Data) != string(r.Data) {
			t.Fatalf("lost relation fields: %+v", got)
		}
	}
	empty, err := EncodeSnapshotRelations(nil)
	if err != nil || string(empty) != "[]" {
		t.Fatalf("empty=%s err=%v", empty, err)
	}
}

func TestSnapshotRelationsRejectAmbiguousOrMalformedHistory(t *testing.T) {
	for _, raw := range []string{`[{"TargetPluginID":"a","target_plugin_id":"b"}]`, `{}`, `[null]`, `[{"sort_order":"wrong"}]`} {
		if _, err := DecodeSnapshotRelations(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted malformed relation %s", raw)
		}
	}
}
