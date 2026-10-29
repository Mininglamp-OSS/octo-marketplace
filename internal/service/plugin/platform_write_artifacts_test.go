package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

func TestPlatformArtifactsFreezeUsesAuthorizedResourceIdentity(t *testing.T) {
	ctx := context.Background()
	blobs := storage.NewLocal(t.TempDir(), "http://localhost")
	svc := New(&fakeStore{}, blobs)
	p := &model.Plugin{ID: "system-skill", Visibility: model.PluginVisibilitySystem}
	for _, tc := range []struct {
		key     string
		allowed bool
	}{
		{"platform/plugins/system-skill/attachments/migration/data.bin", true},
		{"plugins/caller-space/attachments/upload.bin", true},
		{"platform/plugins/other-skill/attachments/data.bin", false},
		{"plugins/other-space/attachments/upload.bin", false},
	} {
		t.Run(tc.key, func(t *testing.T) {
			data := []byte("platform attachment")
			if err := blobs.PutObject(ctx, tc.key, strings.NewReader(string(data)), int64(len(data)), ""); err != nil {
				t.Fatal(err)
			}
			docs := artifactDocuments(t, tc.key, data)
			err := svc.freezeVersionArtifacts(ctx, model.PluginTypeSkill, docs, "caller-space", p)
			if !tc.allowed {
				if !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("foreign source allowed: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			key := attachmentKeyMap(docs.AttachmentKeys)["assets/data.bin"]
			if !strings.HasPrefix(key, "platform/plugins/system-skill/attachments/versions/") || !storage.IsImmutableArtifactKey(key) {
				t.Fatalf("not platform immutable: %s", key)
			}
			if err := blobs.DeleteObject(ctx, key); !errors.Is(err, storage.ErrImmutableObject) {
				t.Fatalf("immutable deletion: %v", err)
			}
		})
	}
}

func TestPlatformAttachmentAdminRoundTripAndReviewFreeze(t *testing.T) {
	ctx := context.Background()
	blobs := storage.NewLocal(t.TempDir(), "http://localhost")
	data := []byte("platform binary")
	key := "platform/plugins/skill-1/attachments/migration/data.bin"
	if err := blobs.PutObject(ctx, key, strings.NewReader(string(data)), int64(len(data)), ""); err != nil {
		t.Fatal(err)
	}
	req := adminSkillRequest()
	var pkg map[string]any
	if err := json.Unmarshal(req.Package, &pkg); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	pkg["attachments"] = append(pkg["attachments"].([]any), map[string]any{"path": "assets/data.bin", "content_type": "storage", "mime_type": "application/octet-stream", "content_hash": "sha256:" + hex.EncodeToString(hash[:]), "content_size": len(data), "storage_uri": key})
	req.Package, _ = json.Marshal(pkg)
	resource := &model.Plugin{ID: "skill-1", Name: req.Name, Type: req.Type, Visibility: model.PluginVisibilitySystem, Tags: req.Tags, ListingState: model.PluginListingStateDraft}
	docs, err := canonicalizePluginDocuments(resource, req.Name, req.Type, req.Tags, req.Manifest, req.Package, "")
	if err != nil {
		t.Fatal(err)
	}
	resource.Manifest, resource.Package, resource.AttachmentKeys = docs.Manifest, docs.Package, docs.AttachmentKeys
	resource.ManifestHash, resource.PluginHash = docs.ManifestHash, docs.PluginHash
	f := &fakeStore{plugins: map[string]*model.Plugin{resource.ID: resource}}
	svc := New(f, blobs)
	req.Package = docs.Package // normal GET omits host-private object keys
	detail, err := svc.AdminUpdate(ctx, adminCaller, resource.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Plugin.SpaceID != nil {
		t.Fatal("platform resource acquired a Space")
	}
	frozen := attachmentKeyMap(detail.Plugin.AttachmentKeys)["assets/data.bin"]
	if !strings.HasPrefix(frozen, "platform/plugins/skill-1/attachments/versions/") {
		t.Fatalf("wrong freeze namespace: %s", frozen)
	}
	var uploaded []string
	snap, err := svc.freezeSubmission(ctx, Caller{UID: "admin", SpaceID: "caller-space"}, detail, ReviewSubmitParams{Manifest: detail.Plugin.Manifest, Package: detail.Plugin.Package}, nil, &uploaded)
	if err != nil {
		t.Fatal(err)
	}
	if attachmentKeyMap(snap.AttachmentKeys)["assets/data.bin"] != frozen {
		t.Fatal("review changed frozen platform object")
	}
}

func TestPlatformDocumentScopeCannotBeClaimedByPublicCanonicalizer(t *testing.T) {
	req := adminSkillRequest()
	var pkg map[string]any
	if err := json.Unmarshal(req.Package, &pkg); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("x"))
	pkg["attachments"] = append(pkg["attachments"].([]any), map[string]any{"path": "assets/data.bin", "content_type": "storage", "mime_type": "application/octet-stream", "content_hash": "sha256:" + hex.EncodeToString(hash[:]), "content_size": 1, "storage_uri": "platform/plugins/skill-1/attachments/migration/data.bin"})
	req.Package, _ = json.Marshal(pkg)
	if _, err := CanonicalizeDocuments(req.Name, req.Type, req.Tags, req.Manifest, req.Package, "caller-space"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("caller claimed platform scope: %v", err)
	}
	for _, p := range []*model.Plugin{{ID: "other-skill", Visibility: model.PluginVisibilitySystem}, {ID: "skill-1", Visibility: model.PluginVisibilitySpace}} {
		if _, err := canonicalizePluginDocuments(p, req.Name, req.Type, req.Tags, req.Manifest, req.Package, "caller-space"); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("foreign resource scope accepted: %v", err)
		}
	}
}
