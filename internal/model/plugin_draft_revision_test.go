package model

import "testing"

func TestDraftRevisionIgnoresDatabaseJSONFormatting(t *testing.T) {
	a := Plugin{Tags: []byte(`["one","two"]`), AttachmentKeys: []byte(`{"b":"two","a":"one"}`)}
	b := a
	b.Tags = []byte(`["one", "two"]`)
	b.AttachmentKeys = []byte(`{"a": "one", "b": "two"}`)
	if PluginDraftRevision(&a) != PluginDraftRevision(&b) {
		t.Fatal("database JSON formatting changed revision")
	}
	b.Tags = []byte(`["one","changed"]`)
	if PluginDraftRevision(&a) == PluginDraftRevision(&b) {
		t.Fatal("changed tag did not change revision")
	}
}
