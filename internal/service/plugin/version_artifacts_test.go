package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

func artifactDocuments(t *testing.T, key string, data []byte) *CanonicalDocuments {
	t.Helper()
	sum := sha256.Sum256(data)
	pkg, _ := json.Marshal(map[string]any{"attachments": []map[string]any{{"path": "assets/data.bin", "content_type": "storage", "content_size": len(data), "content_hash": "sha256:" + hex.EncodeToString(sum[:])}}})
	keys, _ := json.Marshal(map[string]string{"assets/data.bin": key})
	return &CanonicalDocuments{Package: pkg, AttachmentKeys: keys}
}

func TestFreezeVersionArtifactsRetainsOriginalBytesAcrossReplacementRemovalAndFailure(t *testing.T) {
	ctx := context.Background()
	blobs := storage.NewLocal(t.TempDir(), "http://localhost")
	s := New(&fakeStore{}, blobs)
	key := "plugins/space-a/attachments/upload.bin"
	old := []byte("old binary content")
	newContent := []byte("replacement binary content")
	if err := blobs.PutObject(ctx, key, strings.NewReader(string(old)), int64(len(old)), ""); err != nil {
		t.Fatal(err)
	}
	v1 := artifactDocuments(t, key, old)
	if err := s.freezeVersionArtifacts(ctx, model.PluginTypeSkill, v1, "space-a"); err != nil {
		t.Fatal(err)
	}
	oldKey := attachmentKeyMap(v1.AttachmentKeys)["assets/data.bin"]
	if oldKey == key || !storage.IsImmutableArtifactKey(oldKey) {
		t.Fatalf("not frozen: %s", oldKey)
	}
	if err := blobs.PutObject(ctx, key, strings.NewReader(string(newContent)), int64(len(newContent)), ""); err != nil {
		t.Fatal(err)
	}
	v2 := artifactDocuments(t, key, newContent)
	if err := s.freezeVersionArtifacts(ctx, model.PluginTypeExpert, v2, "space-a"); err != nil {
		t.Fatal(err)
	}
	if oldKey == attachmentKeyMap(v2.AttachmentKeys)["assets/data.bin"] {
		t.Fatal("different versions share mutable bytes")
	}
	if err := blobs.DeleteObject(ctx, key); err != nil {
		t.Fatal(err)
	}
	// A failed repository transaction/abandoned review must not delete a shared
	// immutable object, even when it reaches the generic compensation path.
	s.deleteObjects(ctx, oldKey)
	body, err := blobs.GetObject(ctx, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, _ := io.ReadAll(body)
	if string(got) != string(old) {
		t.Fatalf("v1 lost original bytes: %s", got)
	}
	replay := &CanonicalDocuments{Package: v1.Package, AttachmentKeys: v1.AttachmentKeys}
	if err := s.freezeVersionArtifacts(ctx, model.PluginTypeSkill, replay, "space-a"); err != nil {
		t.Fatal(err)
	}
	if string(replay.AttachmentKeys) != string(v1.AttachmentKeys) {
		t.Fatal("repeated version changed object mapping")
	}
}

func TestFreezeArtifactsRejectsCorruptMissingCrossSpaceAndOversizedBytes(t *testing.T) {
	for _, mode := range []string{"corrupt-source", "corrupt-existing", "missing", "cross-space", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			blobs := storage.NewLocal(root, "http://localhost")
			s := New(&fakeStore{}, blobs)
			ctx := context.Background()
			key := "plugins/space-a/attachments/upload.bin"
			data := []byte("expected bytes")
			docs := artifactDocuments(t, key, data)
			if mode != "missing" {
				if err := blobs.PutObject(ctx, key, strings.NewReader(string(data)), int64(len(data)), ""); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "corrupt-source":
				_ = blobs.PutObject(ctx, key, strings.NewReader("different bytes"), 15, "")
			case "corrupt-existing":
				sum := sha256.Sum256(data)
				immutable := "plugins/space-a/attachments/versions/" + hex.EncodeToString(sum[:])
				full := filepath.Join(root, immutable)
				if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte("corruption"), 0644); err != nil {
					t.Fatal(err)
				}
			case "cross-space":
				docs = artifactDocuments(t, "plugins/space-b/attachments/upload.bin", data)
			case "oversize":
				s.SetArtifactLimits(3)
			}
			err := s.freezeVersionArtifacts(ctx, model.PluginTypeSkill, docs, "space-a")
			if err == nil {
				t.Fatalf("accepted %s", mode)
			}
			if mode == "missing" && !errors.Is(err, ErrIntegrity) {
				t.Fatalf("missing source not classified as integrity: %v", err)
			}
		})
	}
}

func TestFreezeLegacyRefRetainsActualArchiveAndMarkdown(t *testing.T) {
	ctx := context.Background()
	blobs := storage.NewLocal(t.TempDir(), "http://localhost")
	s := New(&fakeStore{}, blobs)
	zipKey := "plugins/space-a/attachments/legacy.zip"
	mdKey := "plugins/space-a/attachments/legacy.md"
	for key, value := range map[string]string{zipKey: "original zip bytes", mdKey: "original markdown"} {
		if err := blobs.PutObject(ctx, key, strings.NewReader(value), int64(len(value)), ""); err != nil {
			t.Fatal(err)
		}
	}
	docs := &CanonicalDocuments{Manifest: json.RawMessage(`{}`), Package: packageWith(rawAtt("SKILL.md", "stub"), rawAtt("skill/ref.json", `{"object_key":"`+mdKey+`","zip_object_key":"`+zipKey+`"}`))}
	if err := s.freezeVersionArtifacts(ctx, model.PluginTypeSkill, docs, "space-a"); err != nil {
		t.Fatal(err)
	}
	var ref map[string]any
	raw, _ := rawAttachmentContent(docs.Package, "skill/ref.json")
	if err := json.Unmarshal([]byte(raw), &ref); err != nil {
		t.Fatal(err)
	}
	_ = blobs.DeleteObject(ctx, zipKey)
	_ = blobs.DeleteObject(ctx, mdKey)
	for _, field := range []string{"object_key", "zip_object_key"} {
		key, _ := ref[field].(string)
		if !storage.IsImmutableArtifactKey(key) {
			t.Fatalf("mutable legacy pointer: %s", raw)
		}
		body, err := blobs.GetObject(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(body)
		body.Close()
		if !strings.HasPrefix(string(b), "original") {
			t.Fatalf("lost actual bytes: %q", b)
		}
		if ref[field+"_hash"] == nil || ref[field+"_size"] == nil {
			t.Fatalf("missing inventory: %s", raw)
		}
	}
}

func TestFreezeUsesInlineStorageURIWhenSidecarIsMissing(t *testing.T) {
	ctx := context.Background()
	blobs := storage.NewLocal(t.TempDir(), "http://localhost")
	s := New(&fakeStore{}, blobs)
	key := "plugins/space-a/attachments/legacy.bin"
	data := []byte("legacy bytes")
	if err := blobs.PutObject(ctx, key, strings.NewReader(string(data)), int64(len(data)), ""); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	pkg, _ := json.Marshal(map[string]any{"attachments": []map[string]any{{
		"path":         "assets/legacy.bin",
		"content_type": "storage",
		"content_size": len(data),
		"content_hash": "sha256:" + hex.EncodeToString(sum[:]),
		"storage_uri":  key,
	}}})
	docs := &CanonicalDocuments{Package: pkg}
	if err := s.freezeVersionArtifacts(ctx, model.PluginTypeSkill, docs, "space-a"); err != nil {
		t.Fatal(err)
	}
	frozen := attachmentKeyMap(docs.AttachmentKeys)["assets/legacy.bin"]
	if !storage.IsImmutableArtifactKey(frozen) {
		t.Fatalf("not frozen: %q (sidecar=%s)", frozen, docs.AttachmentKeys)
	}
	body, err := blobs.GetObject(ctx, frozen)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, _ := io.ReadAll(body)
	if string(got) != string(data) {
		t.Fatalf("bytes=%q", got)
	}
}

func TestFreezeConnectorDoesNotReadOrVersionItsConfiguration(t *testing.T) {
	docs := &CanonicalDocuments{Package: json.RawMessage(`{}`), AttachmentKeys: json.RawMessage(`{"configuration":"mutable"}`)}
	if err := New(&fakeStore{}).freezeVersionArtifacts(context.Background(), model.PluginTypeConnector, docs, "space-a"); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptedContentAddressedImportIsNeverOverwritten(t *testing.T) {
	blobs := &importStorage{objects: map[string][]byte{}}
	s := New(&fakeStore{}, blobs)
	archive := zipWith(t, map[string][]byte{"SKILL.md": []byte("# Skill"), "binary.bin": {0, 255}})
	key := deterministicSkillObjectKey("space-a", "plugin", "binary.bin", []byte{0, 255})
	blobs.objects[key] = []byte("corrupt historical content")
	if _, _, _, err := s.buildSkillAttachmentTree(context.Background(), "space-a", "plugin", archive, nil, nil); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("err=%v", err)
	}
	if string(blobs.objects[key]) != "corrupt historical content" {
		t.Fatal("rewrote an existing historical object")
	}
}
