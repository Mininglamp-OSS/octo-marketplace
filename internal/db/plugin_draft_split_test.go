package db

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"testing"
	"time"
)

func TestMigratedDraftsSaveIndependentlyAndCannotReferenceUnreleasedSkills(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	const id = "draft-split"
	seedMigrationRoot(t, database, id, "private", "draft")
	if _, err := database.Exec(`UPDATE plugin_relations SET relation_json=JSON_OBJECT('member_key','worker','role','member','is_leader',false) WHERE source_plugin_id=?`, id); err != nil {
		t.Fatal(err)
	}
	applyCurrentMigration(t, database)
	var edges, releases int
	database.QueryRow(`SELECT COUNT(*) FROM plugin_relations r JOIN plugins p ON p.plugin_id=r.source_plugin_id WHERE p.is_embedded=0 AND r.status=1 AND r.deleted_at IS NULL`).Scan(&edges)
	database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases`).Scan(&releases)
	if edges != 0 || releases != 0 {
		t.Fatalf("drafts have dependencies=%d releases=%d", edges, releases)
	}
	svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(true)
	detail, err := svc.CurrentDetail(ctx, managedOwner(), id)
	if err != nil {
		t.Fatal(err)
	}
	p := detail.Plugin
	updated, err := svc.Update(ctx, managedOwner(), id, pluginsvc.WriteRequest{Name: p.Name, Type: p.Type, Visibility: p.Visibility, Version: "1.2.5", Tags: p.Tags, Manifest: p.Manifest, Package: p.Package})
	if err != nil {
		t.Fatalf("edit independent draft: %v", err)
	}
	if len(updated.Relations) != 0 {
		t.Fatal("draft relationships recreated")
	}
	if _, err = svc.Publish(ctx, managedOwner(), explicitTestPublication(t, database, id)); err == nil {
		t.Fatal("incomplete team published")
	}
	var expertID, skillID, skillVersion string
	if err = database.QueryRow(`SELECT p.plugin_id FROM plugins p JOIN marketplace_conversion_receipt r ON r.target_id=p.plugin_id WHERE p.plugin_type='expert'`).Scan(&expertID); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow(`SELECT p.plugin_id,p.current_version_id FROM plugins p JOIN marketplace_conversion_receipt r ON r.target_id=p.plugin_id WHERE p.plugin_type='skill'`).Scan(&skillID, &skillVersion); err != nil {
		t.Fatal(err)
	}
	expert, err := svc.CurrentDetail(ctx, managedOwner(), expertID)
	if err != nil {
		t.Fatal(err)
	}
	p = expert.Plugin
	_, err = svc.Update(ctx, managedOwner(), expertID, pluginsvc.WriteRequest{Name: p.Name, Type: p.Type, Visibility: p.Visibility, Version: "1.0.1", Tags: p.Tags, Manifest: p.Manifest, Package: p.Package, Relations: []pluginsvc.RelationRequest{{Type: "expert_skill", TargetPluginID: skillID, TargetVersionID: &skillVersion, Data: json.RawMessage(`{}`)}}})
	if err == nil {
		t.Fatal("draft skill accepted as formal reference")
	}
}

func TestEmptyDraftTeamCannotBeApprovedFromLegacyPendingReview(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	write := managedWrite("1.0.0", "Draft team instructions", model.PluginVisibilitySpace)
	write.Type = model.PluginTypeExpertTeam
	var manifest map[string]any
	if err := json.Unmarshal(write.Manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["plugin_type"] = "expert_team"
	write.Manifest, _ = json.Marshal(manifest)
	created, err := svc.Create(ctx, managedOwner(), write)
	if err != nil {
		t.Fatalf("save independent draft: %v", err)
	}
	p := created.Plugin
	now := time.Now().UTC()
	req := model.PluginReviewRequest{ID: "empty-team-review", PluginID: p.ID, SpaceID: "space-a", Version: "1.0.0", Status: model.ReviewStatusPending, ApplicantUID: "user-1", ApplicantName: "Owner", SubmittedAt: now, CreatedAt: now, UpdatedAt: now}
	snap := pluginrepo.FrozenSnapshot{Manifest: p.Manifest, Package: p.Package, ManifestHash: p.ManifestHash, PluginHash: p.PluginHash}
	// New reference-enabled submissions already reject incomplete teams.
	if err = repo.InsertReviewRequest(ctx, tenantScope(), &req, snap); err == nil {
		t.Fatal("incomplete team submission accepted")
	}
	// A pending request may predate enabling fixed references. Approval must
	// independently enforce release invariants rather than editable-draft rules.
	legacy := pluginrepo.New(database).WithVersionManagement(true)
	if err = legacy.InsertReviewRequest(ctx, tenantScope(), &req, snap); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApproveReview(ctx, managedReviewer(), req.ID); err == nil {
		t.Fatal("empty draft team was approved and published")
	}
	_, err = repo.ApproveReview(ctx, pluginrepo.Scope{CallerUID: "reviewer", SpaceID: "space-a"}, pluginrepo.ApproveReviewParams{ReviewID: req.ID, ReviewerUID: "reviewer", ReviewerName: "Reviewer"})
	if !errors.Is(err, pluginrepo.ErrInvalidRelation) {
		t.Fatalf("approval did not enforce release relation contract: %v", err)
	}
	var releases int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id=?`, p.ID).Scan(&releases); err != nil {
		t.Fatal(err)
	}
	var listing, status string
	if err = database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id=?`, p.ID).Scan(&listing); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow(`SELECT status FROM plugin_review_requests WHERE review_id=?`, req.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if releases != 0 || listing != "draft" || status != "pending" {
		t.Fatalf("approval changed state: releases=%d listing=%s review=%s", releases, listing, status)
	}
}
