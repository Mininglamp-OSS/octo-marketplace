package db

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

// API configuration rejects references without version management, and the
// Service gates that combination off. Direct repository users can still select
// it; every snapshot writer must consistently honor the repository index flag.
// This tests that defensive invariant, not a deployed configuration path.
func TestLegacyApprovalHonorsRepositoryReferenceIndexFlag(t *testing.T) {
	for _, withEdge := range []bool{false, true} {
		t.Run(fmt.Sprintf("with_edge=%t", withEdge), func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			owner := managedOwner()
			modernRepo := pluginrepo.New(database)
			modern := pluginsvc.New(modernRepo).WithVersionManagement(true).WithReferences(true)
			skill, err := modern.Create(ctx, owner, managedSkillWrite("1.0.0", "Indexed Skill", model.PluginVisibilitySpace))
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, modern, owner, skill.Plugin.ID)
			state, err := modernRepo.GetVersionState(ctx, tenantScope(), skill.Plugin.ID)
			if err != nil || state.PublishedVersionID == nil {
				t.Fatalf("missing Skill release: %v", err)
			}
			write := managedWrite("1.0.0", "approval index fixture", model.PluginVisibilitySpace)
			if withEdge {
				write.Relations = []pluginsvc.RelationRequest{{Type: "expert_skill", TargetPluginID: skill.Plugin.ID, TargetVersionID: state.PublishedVersionID}}
			}
			expert, err := modern.Create(ctx, owner, write)
			if err != nil {
				t.Fatal(err)
			}
			// Deliberately construct the repository-only combination without a
			// Service, which would correctly disable independent references here.
			repo := pluginrepo.New(database).WithReferences(true)
			request := newRequest(expert.Plugin.ID, "1.0.0")
			snapshot := pluginrepo.FrozenSnapshot{
				Manifest: expert.Plugin.Manifest, Package: expert.Plugin.Package,
				AttachmentKeys: expert.Plugin.AttachmentKeys, Relations: expert.Relations,
				ManifestHash: expert.Plugin.ManifestHash, PluginHash: expert.Plugin.PluginHash,
				DeclaredContent: true,
			}
			if err := repo.InsertReviewRequest(ctx, tenantScope(), request, snapshot); err != nil {
				t.Fatal(err)
			}
			approved, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
				ReviewID: request.ID, ReviewerUID: "admin-1", ReviewerName: "Reviewer",
			})
			if err != nil || approved.CurrentVersionID == nil {
				t.Fatalf("legacy approval: %v", err)
			}
			dependents, _, err := repo.ReferenceDependents(ctx, tenantScope(), skill.Plugin.ID, "", 100, 0)
			if err != nil {
				t.Fatalf("approval left reference reads incomplete: %v", err)
			}
			wantEdges := 0
			if withEdge {
				wantEdges = 1
				found := false
				for _, dependent := range dependents {
					if dependent.PluginID == expert.Plugin.ID && dependent.VersionID == *approved.CurrentVersionID {
						found = true
					}
				}
				if !found {
					t.Fatal("approved snapshot missing from reverse dependencies")
				}
			} else if len(dependents) != 0 {
				t.Fatal("empty approved snapshot acquired a reference")
			}
			var indexed, edges int
			var digestMatches bool
			if err := database.QueryRow(`SELECT idx.relation_count,idx.snapshot_digest=SHA2(CAST(v.relations_json AS CHAR CHARACTER SET utf8mb4),256),(SELECT COUNT(*) FROM plugin_version_relation_refs edge WHERE edge.source_plugin_id=v.plugin_id AND edge.source_version_id=v.version_id) FROM plugin_versions v JOIN plugin_version_reference_index_state idx ON idx.source_plugin_id=v.plugin_id AND idx.source_version_id=v.version_id WHERE v.plugin_id=? AND v.version_id=?`, expert.Plugin.ID, *approved.CurrentVersionID).Scan(&indexed, &digestMatches, &edges); err != nil || indexed != wantEdges || edges != wantEdges || !digestMatches {
				t.Fatalf("approval index mismatch: indexed=%d edges=%d digest=%t err=%v", indexed, edges, digestMatches, err)
			}
			foreign := tenantScope()
			foreign.SpaceID = "foreign-space"
			if _, _, err := repo.ReferenceDependents(ctx, foreign, skill.Plugin.ID, "", 100, 0); !errors.Is(err, pluginrepo.ErrNotFound) {
				t.Fatalf("foreign Space inspected indexed references: %v", err)
			}
			err = repo.Delete(ctx, tenantScope(), skill.Plugin.ID, owner.UID, owner.Name, "", nil)
			if withEdge {
				if !errors.Is(err, pluginrepo.ErrRelationInUse) {
					t.Fatalf("referenced Skill deletion: %v", err)
				}
			} else if err != nil {
				t.Fatalf("unreferenced Skill deletion: %v", err)
			}
		})
	}
}
