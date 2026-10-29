package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestPlatformArtifactsDownloadHistoryAndInstall(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"valid", "foreign-resource", "space-resource", "tampered"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte("original reference")
			sum := sha256.Sum256(data)
			key := "platform/plugins/skill-a/attachments/versions/" + hex.EncodeToString(sum[:])
			p := &model.Plugin{ID: "skill-a", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySystem}
			p.Package = packageWith(rawAtt("SKILL.md", "# entry"), fmt.Sprintf(`{"path":"references/note.txt","content_type":"storage","mime_type":"text/plain","content_size":%d,"content_hash":%q}`, len(data), "sha256:"+hex.EncodeToString(sum[:])))
			p.AttachmentKeys, _ = json.Marshal(map[string]string{"references/note.txt": key})
			stored := append([]byte(nil), data...)
			switch mode {
			case "foreign-resource":
				p.ID = "skill-b"
			case "space-resource":
				space := "space-a"
				p.SpaceID = &space
				p.Visibility = model.PluginVisibilitySpace
			case "tampered":
				stored[0] = 'X'
			}
			s := New(&fakeStore{}, &importStorage{objects: map[string][]byte{key: stored}})
			v := &model.PluginVersion{Package: p.Package, AttachmentKeys: p.AttachmentKeys}
			got, historyErr := s.versionFileBytes(ctx, p, v, "references/note.txt")
			builder := capabilityBuilder{service: s, remaining: maxCapabilityBytes}
			files, installErr := builder.skillFiles(ctx, p)
			var out bytes.Buffer
			zipErr := s.writeSkillZip(ctx, p, decodePackageAttachments(p.Package), &out)
			if mode != "valid" {
				if historyErr == nil || installErr == nil || zipErr == nil {
					t.Fatalf("invalid platform object accepted: history=%v install=%v zip=%v", historyErr, installErr, zipErr)
				}
				if mode == "tampered" && (!errors.Is(historyErr, ErrIntegrity) || !errors.Is(installErr, ErrIntegrity) || !errors.Is(zipErr, ErrIntegrity)) {
					t.Fatal("missing integrity failure")
				}
				return
			}
			if historyErr != nil || installErr != nil || zipErr != nil || !bytes.Equal(got, data) || files["references/note.txt"] != string(data) {
				t.Fatalf("platform read failed: %v %v %v", historyErr, installErr, zipErr)
			}
			zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, f := range zr.File {
				if f.Name == "references/note.txt" {
					r, _ := f.Open()
					b, e := io.ReadAll(r)
					r.Close()
					if e != nil || !bytes.Equal(b, data) {
						t.Fatal("download changed bytes")
					}
					found = true
				}
			}
			if !found {
				t.Fatal("platform download omitted attachment")
			}
			if p.SpaceID != nil || p.Visibility != model.PluginVisibilitySystem {
				t.Fatal("platform ownership changed")
			}
		})
	}
}

func TestPlatformLegacyPointersRequireOriginalDigestOnAdminReads(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"valid", "tampered-markdown", "tampered-archive", "foreign-resource"} {
		t.Run(mode, func(t *testing.T) {
			md := []byte("# original platform skill")
			archive := zipWith(t, map[string][]byte{"SKILL.md": md})
			mh, zh := sha256.Sum256(md), sha256.Sum256(archive)
			mdKey := fmt.Sprintf("platform/plugins/skill-a/attachments/versions/%x", mh)
			zipKey := fmt.Sprintf("platform/plugins/skill-a/attachments/versions/%x", zh)
			ref, _ := json.Marshal(map[string]any{"object_key": mdKey, "object_key_size": len(md), "object_key_hash": fmt.Sprintf("sha256:%x", mh), "zip_object_key": zipKey, "zip_object_key_size": len(archive), "zip_object_key_hash": fmt.Sprintf("sha256:%x", zh)})
			p := &model.Plugin{ID: "skill-a", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySystem, Package: packageWith(rawAtt("SKILL.md", "stub"), rawAtt("skill/ref.json", string(ref)))}
			objects := map[string][]byte{mdKey: append([]byte(nil), md...), zipKey: append([]byte(nil), archive...)}
			if mode == "tampered-markdown" {
				objects[mdKey][0] = 'X'
			}
			if mode == "tampered-archive" {
				objects[zipKey][0] = 'X'
			}
			if mode == "foreign-resource" {
				p.ID = "skill-b"
			}
			s := New(&fakeStore{plugins: map[string]*model.Plugin{p.ID: p}}, &importStorage{objects: objects})
			body, mdErr := s.AdminSkillMarkdown(ctx, adminCaller, p.ID)
			stream, zipErr := s.AdminOpenSkillPackage(ctx, adminCaller, p.ID)
			if mode == "valid" {
				if mdErr != nil || zipErr != nil || body != string(md) {
					t.Fatalf("valid platform refs: %v %v", mdErr, zipErr)
				}
				var out bytes.Buffer
				if e := stream.Write(&out); e != nil || !bytes.Equal(out.Bytes(), archive) {
					t.Fatal("original archive changed", e)
				}
				return
			}
			if zipErr == nil {
				t.Fatal("invalid platform archive accepted")
			}
			if mode != "tampered-archive" && mdErr == nil {
				t.Fatal("invalid platform markdown accepted")
			}
		})
	}
}

func TestPlatformScopeCannotBeDowngradedAndBreakHistory(t *testing.T) {
	req := adminSkillRequest()
	req.Visibility = model.PluginVisibilityPrivate
	space := testCaller.SpaceID
	p := &model.Plugin{ID: "skill-1", Name: req.Name, Type: req.Type, OwnerUID: testCaller.UID, SpaceID: &space, Visibility: model.PluginVisibilitySystem, ListingState: model.PluginListingStateDraft, Manifest: req.Manifest, Package: req.Package, Tags: req.Tags, Status: 1}
	f := &fakeStore{plugins: map[string]*model.Plugin{p.ID: p}}
	s := New(f)
	caller := testCaller
	caller.IsSystemAdmin = true
	_, err := s.Update(context.Background(), caller, p.ID, req)
	if !errors.Is(err, ErrInvalidRequest) || f.update != nil {
		t.Fatalf("platform downgrade must fail before mutation: %v", err)
	}
}
