package plugin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// AudienceResource is metadata only; private foreign names never enter a preview.
type AudienceResource struct {
	PluginID           string                 `json:"plugin_id"`
	Name               string                 `json:"name"`
	Type               model.PluginType       `json:"plugin_type"`
	Visibility         model.PluginVisibility `json:"visibility"`
	PublishedVersionID *string                `json:"published_version_id,omitempty"`
	Revision           string                 `json:"revision"`
}
type VisibilityPlan struct {
	Resource         AudienceResource       `json:"resource"`
	TargetVisibility model.PluginVisibility `json:"target_visibility"`
	PlanRevision     string                 `json:"plan_revision"`
	Dependencies     []AudienceResource     `json:"dependencies"`
	Affected         []AudienceResource     `json:"affected_resources"`
	nodes            map[string]*model.Plugin
	members          []*model.Plugin
	impacts          []*model.Plugin
}

func audienceResource(p *model.Plugin) AudienceResource {
	return AudienceResource{p.ID, p.Name, p.Type, p.Visibility, p.PublishedVersionID, model.PluginDraftRevision(p)}
}
func visibilityPlanRow(ctx context.Context, tx *sql.Tx, space, id string) (*model.Plugin, error) {
	p, err := scanPlugin(tx.QueryRowContext(ctx, `SELECT `+pluginColumns+` FROM plugins p WHERE p.plugin_id=? AND p.status=1 AND p.deleted_at IS NULL AND (p.space_id=? OR p.visibility IN ('system','public'))`, id, space))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err == nil {
		err = tx.QueryRowContext(ctx, `SELECT published_version_id FROM plugins WHERE plugin_id=?`, id).Scan(&p.PublishedVersionID)
	}
	return p, err
}
func (r *Repo) PreviewVisibility(ctx context.Context, scope Scope, id string, target model.PluginVisibility) (*VisibilityPlan, error) {
	if !r.versionManagement {
		return nil, ErrVersionBaselineUnavailable
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, err := getOwnedForUpdate(ctx, tx, scope, id)
	if err != nil {
		return nil, err
	}
	out, err := r.buildVisibilityPlan(ctx, tx, scope, p, target)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
func (r *Repo) buildVisibilityPlan(ctx context.Context, tx *sql.Tx, scope Scope, root *model.Plugin, target model.PluginVisibility) (*VisibilityPlan, error) {
	if (root.Visibility != model.PluginVisibilityPrivate && root.Visibility != model.PluginVisibilitySpace) || root.IsEmbedded || root.SpaceID == nil || *root.SpaceID != scope.SpaceID || root.OwnerUID != scope.CallerUID {
		return nil, ErrNotFound
	}
	if root.ListingState != model.PluginListingStatePublished || (target != model.PluginVisibilityPrivate && target != model.PluginVisibilitySpace) {
		return nil, ErrConflict
	}
	plan := &VisibilityPlan{Resource: audienceResource(root), TargetVisibility: target, Dependencies: []AudienceResource{}, Affected: []AudienceResource{}, nodes: map[string]*model.Plugin{root.ID: root}, members: []*model.Plugin{root}}
	if versioned(root) {
		var id sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT published_version_id FROM plugins WHERE plugin_id=?`, root.ID).Scan(&id); err != nil {
			return nil, err
		}
		if !id.Valid {
			return nil, ErrVersionBaselineUnavailable
		}
		root.PublishedVersionID = &id.String
		plan.Resource = audienceResource(root)
	}
	visited := map[string]bool{}
	var fingerprints []string
	var walk func(*model.Plugin, string) error
	walk = func(p *model.Plugin, vid string) error {
		if len(visited) > maxGraphNodes || len(plan.nodes) > maxGraphNodes {
			return ErrGraphTooLarge
		}
		if !versioned(p) {
			return nil
		}
		if vid == "" {
			var v sql.NullString
			if err := tx.QueryRowContext(ctx, `SELECT published_version_id FROM plugins WHERE plugin_id=?`, p.ID).Scan(&v); err != nil {
				return err
			}
			if !v.Valid && (r.references || !p.IsEmbedded) {
				return ErrVersionBaselineUnavailable
			}
			vid = v.String
			if v.Valid {
				p.PublishedVersionID = &vid
			}
		}
		key := p.ID + ":" + vid
		if visited[key] {
			return nil
		}
		visited[key] = true
		fingerprints = append(fingerprints, key)
		var rels []model.PluginRelation
		if vid == "" {
			// Legacy bundled resources inherit publication from their container.
			var err error
			rels, err = compatibilityLiveRelations(ctx, tx, p.ID)
			if err != nil {
				return err
			}
		} else {
			var raw []byte
			err := tx.QueryRowContext(ctx, `SELECT v.relations_json FROM plugin_versions v JOIN plugin_version_releases rel ON rel.plugin_id=v.plugin_id AND rel.version_id=v.version_id WHERE v.plugin_id=? AND v.version_id=?`, p.ID, vid).Scan(&raw)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrVersionBaselineUnavailable
			}
			if err != nil {
				return err
			}
			rels, err = model.DecodeSnapshotRelations(raw)
			if err != nil {
				return err
			}
		}
		for _, edge := range rels {
			if edge.Status != 0 && edge.Status != 1 || edge.DeletedAt != nil {
				continue
			}
			child, err := visibilityPlanRow(ctx, tx, scope.SpaceID, edge.TargetPluginID)
			if err != nil {
				return ErrInvalidRelation
			}
			if child.ListingState != model.PluginListingStatePublished {
				return ErrInvalidRelation
			}
			if child.Visibility != model.PluginVisibilitySystem && child.Visibility != model.PluginVisibilityPublic && (child.SpaceID == nil || *child.SpaceID != scope.SpaceID) {
				return ErrInvalidRelation
			}
			_, known := plan.nodes[child.ID]
			plan.nodes[child.ID] = child
			if child.Visibility == model.PluginVisibilityPrivate {
				if child.OwnerUID != scope.CallerUID {
					return ErrInvalidRelation
				}
				if !child.IsEmbedded && !known {
					plan.members = append(plan.members, child)
					if err := walk(child, ""); err != nil {
						return err
					}
				}
			}
			pin := ""
			if edge.TargetVersionID != nil {
				pin = *edge.TargetVersionID
			}
			if err := walk(child, pin); err != nil {
				return err
			}
		}
		return nil
	}
	if target == model.PluginVisibilitySpace && root.Visibility != target {
		if err := walk(root, ""); err != nil {
			return nil, err
		}
	}
	if target == model.PluginVisibilityPrivate && root.Visibility != target {
		queue := []string{root.ID}
		impacted := map[string]bool{}
		for len(queue) > 0 {
			targetID := queue[0]
			queue = queue[1:]
			ids, err := r.incomingPublishedSources(ctx, tx, scope.SpaceID, targetID)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				if id == root.ID || impacted[id] {
					continue
				}
				p, err := visibilityPlanRow(ctx, tx, scope.SpaceID, id)
				if err != nil {
					return nil, err
				}
				if targetID == root.ID && p.Visibility == model.PluginVisibilityPrivate && p.OwnerUID == root.OwnerUID {
					continue
				}
				impacted[id] = true
				queue = append(queue, id)
				if len(impacted) > maxGraphNodes {
					return nil, ErrGraphTooLarge
				}
				plan.impacts = append(plan.impacts, p)
				plan.nodes[p.ID] = p
				if p.Visibility == model.PluginVisibilitySpace || p.OwnerUID == scope.CallerUID {
					plan.Affected = append(plan.Affected, audienceResource(p))
				}
			}
		}
	}
	sort.Slice(plan.members, func(i, j int) bool { return plan.members[i].ID < plan.members[j].ID })
	sort.Slice(plan.Affected, func(i, j int) bool { return plan.Affected[i].PluginID < plan.Affected[j].PluginID })
	for _, p := range plan.members {
		if p.ID != root.ID {
			plan.Dependencies = append(plan.Dependencies, audienceResource(p))
		}
	}
	for id, p := range plan.nodes {
		fingerprints = append(fingerprints, id+":"+model.PluginDraftRevision(p)+":"+publishedID(p))
	}
	fingerprints = append(fingerprints, scope.SpaceID, scope.CallerUID, root.ID, string(target))
	sort.Strings(fingerprints)
	raw, _ := json.Marshal(fingerprints)
	sum := sha256.Sum256(raw)
	plan.PlanRevision = hex.EncodeToString(sum[:])
	return plan, nil
}

// Incoming paths follow each team's pinned member snapshot, not the member's
// latest editable/released graph. Legacy roots retain their live-edge fallback.
func usesTargetSnapshotSQL(sourceID, version string) string {
	direct := `EXISTS(SELECT 1 FROM plugin_versions sv JOIN JSON_TABLE(sv.relations_json,'$[*]' COLUMNS (` + snapshotRelationColumns + `)) e WHERE sv.plugin_id=` + sourceID + ` AND sv.version_id=` + version + ` AND ` + snapshotField("e", "target_plugin_id") + `=? AND COALESCE(e.status,e.status_legacy,1)=1 AND COALESCE(e.deleted_at,e.deleted_at_legacy) IS NULL)`
	nested := `EXISTS(SELECT 1 FROM plugin_versions sv JOIN JSON_TABLE(sv.relations_json,'$[*]' COLUMNS (` + snapshotRelationColumns + `)) e JOIN plugins member ON member.plugin_id=` + snapshotField("e", "target_plugin_id") + ` WHERE sv.plugin_id=` + sourceID + ` AND sv.version_id=` + version + ` AND ` + snapshotField("e", "relation_type") + `='expert_team_expert' AND COALESCE(e.status,e.status_legacy,1)=1 AND COALESCE(e.deleted_at,e.deleted_at_legacy) IS NULL AND ` + usesTargetLeafSQL("member.plugin_id", `COALESCE(`+snapshotField("e", "target_version_id")+`,member.published_version_id)`) + `)`
	return `(` + direct + ` OR ` + nested + `)`
}
func usesTargetLeafSQL(id, version string) string {
	return `EXISTS(SELECT 1 FROM plugin_versions mv JOIN JSON_TABLE(mv.relations_json,'$[*]' COLUMNS (` + snapshotRelationColumns + `)) me WHERE mv.plugin_id=` + id + ` AND mv.version_id=` + version + ` AND ` + snapshotField("me", "target_plugin_id") + `=? AND COALESCE(me.status,me.status_legacy,1)=1 AND COALESCE(me.deleted_at,me.deleted_at_legacy) IS NULL)`
}
func (r *Repo) incomingPublishedSources(ctx context.Context, tx *sql.Tx, space, id string) ([]string, error) {
	snapshot := usesTargetSnapshotSQL("p.plugin_id", "p.published_version_id")
	live := `EXISTS(SELECT 1 FROM plugin_relations l WHERE l.source_plugin_id=p.plugin_id AND l.status=1 AND l.deleted_at IS NULL AND (l.target_plugin_id=? OR (l.relation_type='expert_team_expert' AND (` + usesTargetLeafSQL("l.target_plugin_id", `COALESCE(l.target_version_id,(SELECT published_version_id FROM plugins WHERE plugin_id=l.target_plugin_id))`) + ` OR EXISTS(SELECT 1 FROM plugin_relations ml WHERE l.target_version_id IS NULL AND (SELECT published_version_id FROM plugins WHERE plugin_id=l.target_plugin_id) IS NULL AND ml.source_plugin_id=l.target_plugin_id AND ml.target_plugin_id=? AND ml.status=1 AND ml.deleted_at IS NULL)))))`
	rows, err := tx.QueryContext(ctx, `SELECT p.plugin_id FROM plugins p WHERE p.space_id=? AND p.status=1 AND p.deleted_at IS NULL AND p.listing_state='published' AND p.plugin_type IN ('expert','expert_team') AND ((p.published_version_id IS NOT NULL AND `+snapshot+`) OR (p.published_version_id IS NULL AND `+live+`)) ORDER BY p.plugin_id LIMIT ?`, space, id, id, id, id, id, maxGraphNodes+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if len(ids) > maxGraphNodes {
		return nil, ErrGraphTooLarge
	}
	return ids, rows.Err()
}

func publishedID(p *model.Plugin) string {
	if p.PublishedVersionID != nil {
		return *p.PublishedVersionID
	}
	return ""
}
