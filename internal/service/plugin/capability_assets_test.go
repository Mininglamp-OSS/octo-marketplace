package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestCapabilityLegacyArchivePreservesRootedFilesAndChecksIntegrity(t *testing.T) {
	for _, bad := range []bool{false, true} {
		t.Run(fmt.Sprint(bad), func(t *testing.T) {
			svc, store, installer := capabilityFixture()
			key := "plugins/space-a/attachments/legacy.zip"
			data := zipWith(t, map[string][]byte{"pkg/SKILL.md": []byte("# Real skill"), "pkg/references/note.md": []byte("reference")})
			sum := sha256.Sum256(data)
			digest := "sha256:" + hex.EncodeToString(sum[:])
			if bad {
				digest = "sha256:" + strings.Repeat("0", 64)
			}
			store.plugins["skill-1"].Package = packageWith(rawAtt("SKILL.md", "stub"), fmt.Sprintf(`{"path":"skill/package.zip","content_type":"storage","content_size":%d,"content_hash":%q}`, len(data), digest))
			store.plugins["skill-1"].AttachmentKeys = json.RawMessage(`{"skill/package.zip":"` + key + `"}`)
			svc.storage = &importStorage{objects: map[string][]byte{key: data}}
			_, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams())
			if bad {
				if err == nil || installer.calls != 0 {
					t.Fatal("corrupt archive reached Fleet")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			skill := installer.in.Definition.Skills[0]
			if skill.Content != "# Real skill" || len(skill.Files) != 1 || skill.Files[0].Path != "references/note.md" || skill.Files[0].Source.Content != "reference" {
				t.Fatalf("skill=%+v", skill)
			}
		})
	}
}

func TestCapabilityUnresolvedManagedArchiveDoesNotInstallStub(t *testing.T) {
	svc, store, installer := capabilityFixture()
	store.plugins["skill-1"].Package = packageWith(rawAtt("SKILL.md", "stub"), `{"path":"skill/package.zip","content_type":"storage"}`)
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err == nil || installer.calls != 0 {
		t.Fatal("unresolved archive installed as stub")
	}
}

func TestCapabilityAssetLimits(t *testing.T) {
	for _, tc := range []struct {
		name        string
		attachments []string
		budget      int64
	}{
		{"oversize_text", []string{rawAtt("SKILL.md", strings.Repeat("x", 1<<20+1))}, 0},
		{"aggregate", []string{rawAtt("SKILL.md", strings.Repeat("x", 1024))}, 512},
		{"too_many_files", func() []string {
			out := []string{rawAtt("SKILL.md", "x")}
			for i := range 51 {
				out = append(out, rawAtt(fmt.Sprintf("ref/%d.md", i), "x"))
			}
			return out
		}(), 0},
		{"reserved_git", []string{rawAtt("SKILL.md", "x"), rawAtt(".git/config", "x")}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, installer := capabilityFixture()
			store.plugins["skill-1"].Package = packageWith(tc.attachments...)
			if tc.budget > 0 {
				svc.maxArchiveBytes = tc.budget
			}
			if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err == nil || installer.calls != 0 {
				t.Fatal("invalid assets reached Fleet")
			}
		})
	}
	svc, store, installer := capabilityFixture()
	store.plugins["skill-1"].Package = packageWith(rawAtt("SKILL.md", "x"), rawAtt(".github/note.md", "safe text"))
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err != nil || installer.calls != 1 {
		t.Fatalf("ordinary dot-directory rejected: %v", err)
	}
}

func TestCapabilityTiedMemberOrderKeepsFallbackLeaderStable(t *testing.T) {
	svc, store, installer := capabilityFixture()
	second := *store.plugins["expert-1"]
	second.ID, second.Name = "expert-2", "Bob"
	store.plugins[second.ID] = &second
	store.relations["team-1"] = []model.PluginRelation{
		{Type: "expert_team_expert", TargetPluginID: "expert-2", Status: 1},
		{Type: "expert_team_expert", TargetPluginID: "expert-1", Status: 1},
	}
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "team-1", installationParams()); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(installer.in)
	store.relations["team-1"][0], store.relations["team-1"][1] = store.relations["team-1"][1], store.relations["team-1"][0]
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "team-1", installationParams()); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(installer.in)
	if string(before) != string(after) {
		t.Fatal("fallback leader depends on store ordering")
	}
}
