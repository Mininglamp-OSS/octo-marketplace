package artifacts

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func pkg(atts ...map[string]any) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"$schema": "https://schemas.octo.app/cowork-plugin-package-2.0.json", "attachments": atts})
	return b
}
func raw(p, s string) map[string]any {
	return map[string]any{"path": p, "content_type": "raw", "raw_content": s}
}

func TestResolvePlatformArtifactUsesResourceOwnership(t *testing.T) {
	data := []byte{0, 255, 1}
	sum := sha256.Sum256(data)
	key := fmt.Sprintf("platform/plugins/platform-skill/attachments/versions/%x", sum)
	p := model.Plugin{ID: "platform-skill", Visibility: model.PluginVisibilitySystem, Type: model.PluginTypeSkill, Package: pkg(raw("SKILL.md", "body"), map[string]any{"path": "asset.bin", "content_type": "storage", "content_size": len(data), "content_hash": fmt.Sprintf("sha256:%x", sum)})}
	p.AttachmentKeys, _ = json.Marshal(map[string]string{"asset.bin": key})
	read := func(context.Context, string) ([]byte, error) { return data, nil }
	files, err := Resolve(context.Background(), &p, read, DefaultLimits())
	if err != nil || len(files) != 2 {
		t.Fatalf("platform resolve: %v", err)
	}
	p.ID = "other"
	if _, err := Resolve(context.Background(), &p, read, DefaultLimits()); err == nil {
		t.Fatal("foreign platform resource accepted")
	}
}
func TestResolveKeeps74FilesAndBinary(t *testing.T) {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for i := 0; i < 74; i++ {
		name := fmt.Sprintf("file-%03d.txt", i)
		if i == 0 {
			name = "SKILL.md"
		}
		f, _ := w.Create("skill/" + name)
		if i == 73 {
			f.Write([]byte{0, 255, 1})
		} else {
			f.Write([]byte("body"))
		}
	}
	w.Close()
	p := model.Plugin{Type: model.PluginTypeSkill, Package: pkg(raw("SKILL.md", "stub"), raw("skill/ref.json", `{"zip_object_key":"experts/e/skill.zip"}`))}
	fs, err := Resolve(context.Background(), &p, func(_ context.Context, key string) ([]byte, error) {
		if key != "experts/e/skill.zip" {
			t.Fatal("unexpected read")
		}
		return b.Bytes(), nil
	}, DefaultLimits())
	if err != nil || len(fs) != 74 {
		t.Fatalf("files=%d err=%v", len(fs), err)
	}
	found := false
	for _, f := range fs {
		if bytes.Equal(f.Data, []byte{0, 255, 1}) {
			found = true
		}
		if f.Path == "SKILL.md" && string(f.Data) != "body" {
			t.Fatal("kept stub")
		}
	}
	if !found {
		t.Fatal("binary lost")
	}
}
func TestResolveRejectsCrossSpaceAndMissingArchive(t *testing.T) {
	sp := "a"
	p := model.Plugin{Type: model.PluginTypeSkill, SpaceID: &sp, Package: pkg(map[string]any{"path": "SKILL.md", "content_type": "storage"}), AttachmentKeys: json.RawMessage(`{"SKILL.md":"plugins/b/attachments/key"}`)}
	called := false
	_, err := Resolve(context.Background(), &p, func(context.Context, string) ([]byte, error) { called = true; return nil, nil }, DefaultLimits())
	if err == nil || called {
		t.Fatal("cross space read")
	}
	p.Package = pkg(raw("SKILL.md", "stub"), raw("skill/ref.json", `{"zip_object_key":"experts/e/missing.zip"}`))
	p.AttachmentKeys = nil
	_, err = Resolve(context.Background(), &p, func(context.Context, string) ([]byte, error) { return nil, errors.New("secret internal detail") }, DefaultLimits())
	if err == nil || err.Error() == "secret internal detail" {
		t.Fatal("missing archive not safely rejected")
	}
}
func TestResolveRejectsTraversalDuplicateAndBudget(t *testing.T) {
	for _, atts := range [][]map[string]any{{raw("../SKILL.md", "x")}, {raw("SKILL.md", "x"), raw("SKILL.md", "y")}, {raw("SKILL.md", "12345")}} {
		p := model.Plugin{Type: model.PluginTypeSkill, Package: pkg(atts...)}
		l := DefaultLimits()
		l.MaxFileBytes = 4
		if _, err := Resolve(context.Background(), &p, nil, l); err == nil {
			t.Fatal("unsafe package accepted")
		}
	}
}
func TestReferencedKeysFindsNestedRefAndSidecars(t *testing.T) {
	keys, err := ReferencedKeys("plugin_json", []byte(`{"attachments":[{"path":"skill/ref.json","content_type":"raw","raw_content":"{\"object_key\":\"experts/e/skill.md\",\"zip_object_key\":\"experts/e/skill.zip\"}"}]}`))
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
	keys, err = ReferencedKeys("attachment_keys_json", []byte(`{"image.png":"plugins/a/attachments/image"}`))
	if err != nil || len(keys) != 1 {
		t.Fatal(keys, err)
	}
	if _, err = ReferencedKeys("attachment_keys_json", []byte(`{"x":"../escape"}`)); err == nil {
		t.Fatal("unsafe key")
	}
}

func TestPointerInventoryIgnoresOpaqueMCPAndRawJSONContent(t *testing.T) {
	for _, value := range []string{"https://example.test/input", "relative/config.json"} {
		mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"s": map[string]any{"command": "server", "env": map[string]string{"file_url": value, "object_key": value, "storage_uri": value}}}})
		p := pkg(raw("SKILL.md", "entry"), raw("mcp.json", string(mcp)), raw("references/config.json", `{"file_url":"https://example.test/reference","object_key":"arbitrary/config"}`))
		keys, e := ReferencedKeys("plugin_json", p)
		if e != nil || len(keys) != 0 {
			t.Fatalf("opaque payload treated as objects: %v %v", keys, e)
		}
		a, e := ReferenceAssertions("plugin_json", p, nil)
		if e != nil || len(a) != 0 {
			t.Fatalf("opaque payload treated as assertions: %v %v", a, e)
		}
	}
}

func TestPointerInventoryPreservesLegacyStorageAndMemberReferences(t *testing.T) {
	for column, raw := range map[string]string{
		"storage":      `{"zip_object_key":"skills/a/versions/v/skill.zip","skill_md_object_key":"skills/a/versions/v/SKILL.md"}`,
		"members_json": `[{"skills":[{"object_key":"squads/a/SKILL.md","zip_object_key":"squads/a/skill.zip"}],"mcp_config":{"file_url":"https://example.test/mcp"}}]`,
	} {
		keys, e := ReferencedKeys(column, []byte(raw))
		if e != nil || len(keys) != 2 {
			t.Fatalf("%s: %v %v", column, keys, e)
		}
	}
}
func TestFrozenReferenceAssertionsAndLegacyInventory(t *testing.T) {
	for _, bad := range []string{`{"object_key":"skills/x/md","object_key_hash":"bad"}`, `{"object_key":"skills/x/md","object_key_size":0}`, `{"object_key":"skills/x/md","object_key_hash":"sha256:0000000000000000000000000000000000000000000000000000000000000000"}`} {
		a, e := ReferenceAssertions("skill/ref.json", []byte(bad), nil)
		if e == nil {
			e = VerifyReferenceAssertions(context.Background(), a, func(context.Context, string) ([]byte, error) { return []byte("same-size"), nil })
		}
		if e == nil {
			t.Fatal("bad frozen assertion accepted")
		}
	}
	keys, e := ReferencedKeys("snapshot_row", []byte(`{"file_url":"skills/x/archive.zip","icon":"icons/managed.png","icon_url":"https://example.test/icon.png"}`))
	if e != nil || len(keys) != 2 {
		t.Fatal(keys, e)
	}
	if _, e = ReferencedKeys("file_url", []byte("https://example.test/archive.zip")); e == nil {
		t.Fatal("external file pointer omitted")
	}
}
func TestArchiveMergesIndependentAttachments(t *testing.T) {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, _ := w.Create("SKILL.md")
	f.Write([]byte("real"))
	w.Close()
	p := model.Plugin{Type: model.PluginTypeSkill, Package: pkg(raw("SKILL.md", "stub"), raw("skill/ref.json", `{"zip_object_key":"skills/x/archive.zip"}`), raw("LICENSE", "license"), map[string]any{"path": "image.bin", "content_type": "storage", "storage_uri": "skills/x/binary"})}
	fs, e := Resolve(context.Background(), &p, func(_ context.Context, k string) ([]byte, error) {
		if k == "skills/x/binary" {
			return []byte{0, 255}, nil
		}
		return b.Bytes(), nil
	}, DefaultLimits())
	if e != nil || len(fs) != 3 {
		t.Fatalf("independent files lost: %v %v", fs, e)
	}
}
func TestArchiveDuplicateIndependentFilesMustAgree(t *testing.T) {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range map[string]string{"SKILL.md": "real", "LICENSE.txt": "license"} {
		f, _ := w.Create(name)
		f.Write([]byte(body))
	}
	w.Close()
	for _, tt := range []struct {
		body string
		fail bool
	}{{"license", false}, {"different", true}} {
		a := raw("LICENSE.txt", tt.body)
		a["mime_type"] = "text/plain"
		p := model.Plugin{Type: model.PluginTypeSkill, Package: pkg(raw("SKILL.md", "stub"), raw("skill/ref.json", `{"zip_object_key":"skills/x/archive.zip"}`), a)}
		fs, e := Resolve(context.Background(), &p, func(context.Context, string) ([]byte, error) { return b.Bytes(), nil }, DefaultLimits())
		if (e != nil) != tt.fail || (!tt.fail && len(fs) != 2) {
			t.Fatalf("duplicate agreement violated files=%d err=%v", len(fs), e)
		}
	}
}
func TestFrozenRefHashRejectsChangedEqualLengthBytes(t *testing.T) {
	h := sha256.Sum256([]byte("original"))
	raw := []byte(fmt.Sprintf(`{"zip_object_key":"skills/x/archive.zip","zip_object_key_hash":"sha256:%x","zip_object_key_size":8}`, h))
	a, e := ReferenceAssertions("skill/ref.json", raw, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = VerifyReferenceAssertions(context.Background(), a, func(context.Context, string) ([]byte, error) { return []byte("modified"), nil }); e == nil {
		t.Fatal("same-length object replacement accepted")
	}
}
