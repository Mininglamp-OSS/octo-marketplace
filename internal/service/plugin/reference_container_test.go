package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func referenceArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func TestReferenceContainerOnlyAcceptsIndependentReferences(t *testing.T) {
	svc := New(nil)
	valid := `{"schema_version":"2","kind":"agent","name":"reviewer","instruction":"Review","skills":[{"id":"skill","version_id":"v1"}],"connectors":[{"id":"connector"}]}`
	req, err := svc.parseReferenceContainer(ContainerImportParams{SchemaVersion: "2", Archive: referenceArchive(t, map[string]string{"expert.json": valid})})
	if err != nil || len(req.Relations) != 2 || req.Relations[0].TargetVersionID == nil || req.Relations[1].TargetVersionID != nil {
		t.Fatalf("req=%+v err=%v", req, err)
	}
	if err = rejectInlineCapabilities(req.Type, req.Package); err != nil {
		t.Fatal(err)
	}
	for _, files := range []map[string]string{
		{"expert.json": `{"kind":"agent","name":"old","instruction":"x","skills":[{"file":"skills/s.zip","name":"s"}]}`},
		{"expert.json": valid, "skills/hidden.zip": "inline"},
		{"expert.json": `{"schema_version":"2","kind":"agent","name":"override","instruction":"x","mcp_config":"secret"}`},
		{"squad.json": `{"schema_version":"2","kind":"squad","name":"override","instruction":"x","members":[{"id":"expert","version_id":"v1","instruction":"override"}]}`},
	} {
		if _, err := svc.parseReferenceContainer(ContainerImportParams{SchemaVersion: "2", Archive: referenceArchive(t, files)}); err == nil {
			t.Fatalf("accepted legacy/inline archive: %v", files)
		}
	}
}
func TestReferenceInlinePackageRejection(t *testing.T) {
	for _, raw := range []string{`{"skills":["inline"]}`, `{"attachments":[{"path":"mcp.json"}]}`, `{"attachments":[{"path":"skills/a/SKILL.md"}]}`} {
		if rejectInlineCapabilities("expert", json.RawMessage(raw)) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestReferenceWriteRejectsManifestInlineAbilities(t *testing.T) {
	svc := New(nil).WithVersionManagement(true).WithReferences(true)
	// The shared library permits manifest extension fields; prove the document
	// is otherwise valid so rejection cannot be attributed to malformed input.
	manifest := map[string]any{"$schema": manifestSchema, "plugin_name": "Expert", "plugin_type": "expert", "name": "Expert", "description": "fixture", "labels": []string{}, "examples": []any{}}
	raw, _ := json.Marshal(manifest)
	pkg := json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"AGENTS.md","content_type":"raw","mime_type":"text/markdown","raw_content":"Expert instructions"}]}`)
	req := WriteRequest{Relations: []RelationRequest{}, Name: "Expert", Type: "expert", Visibility: "private", Tags: json.RawMessage(`[]`), Manifest: raw, Package: pkg}
	if _, _, err := svc.buildWrite(context.Background(), Caller{UID: "owner", SpaceID: "space"}, "", req, time.Now(), false); err != nil {
		t.Fatalf("valid control rejected: %v", err)
	}
	manifest["skills"] = []map[string]string{{"file": "skills/hidden.zip"}}
	req.Manifest, _ = json.Marshal(manifest)
	if _, _, err := svc.buildWrite(context.Background(), Caller{UID: "owner", SpaceID: "space"}, "", req, time.Now(), false); err == nil {
		t.Fatal("accepted inline skills hidden in manifest extensions")
	}
}

func TestTaggedReferenceContainerPassesCanonicalWriteValidation(t *testing.T) {
	svc := New(nil).WithVersionManagement(true).WithReferences(true)
	for _, filename := range []string{"expert.json", "squad.json"} {
		doc := `{"schema_version":"2","kind":"agent","name":"reviewer","instruction":"Review","tags":["research"]}`
		if filename == "squad.json" {
			doc = `{"schema_version":"2","kind":"squad","name":"team","instruction":"Review","tags":["research"],"members":[{"id":"expert","version_id":"v1","member_key":"reviewer","is_leader":true}]}`
		}
		req, err := svc.parseReferenceContainer(ContainerImportParams{SchemaVersion: "2", Archive: referenceArchive(t, map[string]string{filename: doc})})
		if err != nil {
			t.Fatal(err)
		}
		if string(req.Tags) != `["research"]` {
			t.Fatalf("tags lost: %s", req.Tags)
		}
		// Exercise both public service entry points, including admin writes.
		v1 := "v1"
		store := &fakeStore{plugins: map[string]*model.Plugin{"expert": {ID: "expert", Type: model.PluginTypeExpert, Visibility: model.PluginVisibilitySystem, CurrentVersionID: &v1}}}
		service := fixedService(store).WithVersionManagement(true).WithReferences(true)
		params := ContainerImportParams{SchemaVersion: "2", Archive: referenceArchive(t, map[string]string{filename: doc})}
		imported, err := service.ImportContainer(context.Background(), adminCaller, params)
		if err != nil {
			t.Fatalf("%s import: %v", filename, err)
		}
		if string(imported.Plugin.Tags) != `["research"]` {
			t.Fatalf("import tags: %s", imported.Plugin.Tags)
		}
		if filename == "squad.json" && (len(imported.Resolved) != 1 || imported.Resolved[0].TargetPluginID != "expert" || imported.Resolved[0].TargetVersionID == nil || *imported.Resolved[0].TargetVersionID != "v1") {
			t.Fatalf("resolved observations missing: %+v", imported.Resolved)
		}
		store.plugins[imported.Plugin.ID] = imported.Plugin
		reuploaded, err := service.ReuploadContainer(context.Background(), adminCaller, imported.Plugin.ID, params)
		if err != nil {
			t.Fatalf("%s reupload: %v", filename, err)
		}
		if string(reuploaded.Plugin.Tags) != `["research"]` {
			t.Fatalf("reupload tags: %s", reuploaded.Plugin.Tags)
		}
	}
}
