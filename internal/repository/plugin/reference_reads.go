package plugin

import (
	"context"
	"fmt"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// ReferenceDependent reports immutable historical consumers, not installation
// state. It deliberately omits source content, artifact keys and hidden IDs.
type ReferenceDependent struct {
	PluginID     string           `json:"plugin_id"`
	VersionID    string           `json:"version_id"`
	Name         string           `json:"name"`
	Type         model.PluginType `json:"plugin_type"`
	RelationType string           `json:"relation_type"`
	Depth        int              `json:"depth"`
}

func (r *Repo) ReferenceDependents(ctx context.Context, scope Scope, targetID, versionID string, limit, offset int) ([]ReferenceDependent, int, error) {
	if !r.references {
		return nil, 0, ErrNotFound
	}
	if _, err := r.Get(ctx, scope, targetID); err != nil {
		return nil, 0, err
	}
	if err := r.ensureReferenceIndexComplete(ctx, scope); err != nil {
		return nil, 0, err
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	where, args := "1=1", []any{targetID, versionID, versionID}
	if !scope.Admin {
		where = visibilitySQL + ` AND (rel.version_id IS NOT NULL OR (p.owner_uid=? AND p.space_id=?))`
		args = append(args, scope.SpaceID, scope.CallerUID, scope.CallerUID, scope.SpaceID)
	}
	cte := `WITH RECURSIVE dependencies AS (
 SELECT source_plugin_id,source_version_id,relation_type,1 AS depth FROM plugin_version_relation_refs WHERE target_plugin_id=? AND (?='' OR target_version_id=?)
 UNION DISTINCT
 SELECT r.source_plugin_id,r.source_version_id,r.relation_type,d.depth+1 FROM plugin_version_relation_refs r JOIN dependencies d ON r.target_plugin_id=d.source_plugin_id AND r.target_version_id=d.source_version_id WHERE d.depth<2
 ) `
	from := ` FROM dependencies d JOIN plugins p ON p.plugin_id=d.source_plugin_id JOIN plugin_versions snapshot ON snapshot.plugin_id=d.source_plugin_id AND snapshot.version_id=d.source_version_id LEFT JOIN plugin_version_releases rel ON rel.plugin_id=d.source_plugin_id AND rel.version_id=d.source_version_id WHERE p.status=1 AND p.deleted_at IS NULL AND ` + where
	var total int
	if err := r.db.QueryRowContext(ctx, cte+`SELECT COUNT(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, cte+`SELECT p.plugin_id,d.source_version_id,COALESCE(JSON_UNQUOTE(JSON_EXTRACT(snapshot.manifest_json,'$.plugin_name')),''),p.plugin_type,d.relation_type,d.depth`+from+` ORDER BY d.depth,p.plugin_id,d.source_version_id LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ReferenceDependent{}
	for rows.Next() {
		var x ReferenceDependent
		if err := rows.Scan(&x.PluginID, &x.VersionID, &x.Name, &x.Type, &x.RelationType, &x.Depth); err != nil {
			return nil, 0, err
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}

type ReferenceCandidate struct {
	PluginID     string           `json:"plugin_id"`
	Name         string           `json:"name"`
	Type         model.PluginType `json:"plugin_type"`
	VersionID    *string          `json:"version_id,omitempty"`
	AlreadyBound bool             `json:"already_bound"`
	IsSelectable bool             `json:"is_selectable"`
	Reason       string           `json:"reason,omitempty"`
}

func (r *Repo) ReferenceCandidates(ctx context.Context, scope Scope, sourceID, relationType string, limit, offset int) ([]ReferenceCandidate, int, error) {
	if !r.references {
		return nil, 0, ErrNotFound
	}
	source, err := r.Get(ctx, scope, sourceID)
	if err != nil {
		return nil, 0, err
	}
	targetType := model.PluginTypeSkill
	switch relationType {
	case "expert_skill":
	case "expert_connector":
		targetType = model.PluginTypeConnector
	case "expert_team_expert":
		targetType = model.PluginTypeExpert
	default:
		return nil, 0, ErrInvalidRelation
	}
	if !validPersistedRelation(relationType, source.Type, targetType) {
		return nil, 0, ErrInvalidRelation
	}
	// Ordinary readers see released binding state, just like published details.
	// Only draft editors may observe the mutable relation table.
	editable := scope.Admin || (source.OwnerUID == scope.CallerUID && source.SpaceID != nil && *source.SpaceID == scope.SpaceID)
	bound := map[string]bool{}
	if !editable {
		state, err := r.GetVersionState(ctx, scope, sourceID)
		if err != nil {
			return nil, 0, err
		}
		if state.PublishedVersionID == nil {
			return nil, 0, ErrVersionBaselineUnavailable
		}
		version, err := r.GetManagedVersion(ctx, scope, sourceID, *state.PublishedVersionID)
		if err != nil {
			return nil, 0, err
		}
		if version.Release == nil {
			return nil, 0, ErrVersionBaselineUnavailable
		}
		relations, err := model.DecodeSnapshotRelations(version.Relations)
		if err != nil {
			return nil, 0, ErrVersionBaselineUnavailable
		}
		for _, relation := range relations {
			if relation.SourcePluginID == sourceID && relation.Type == relationType && relation.Status == 1 && relation.DeletedAt == nil {
				bound[relation.TargetPluginID] = true
			}
		}
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	// Audience compatibility is narrower than admin visibility. It is evaluated
	// from the source's immutable owner/Space, never admin caller privilege.
	audience := `p.visibility IN ('system','public')`
	args := []any{sourceID, relationType, editable, targetType}
	if referenceVisibility(source.Visibility) != model.PluginVisibilitySystem && source.SpaceID != nil {
		audience += ` OR (p.space_id=? AND p.visibility='space')`
		args = append(args, *source.SpaceID)
		if source.Visibility == model.PluginVisibilityPrivate {
			audience += ` OR (p.space_id=? AND p.owner_uid=? AND p.visibility='private')`
			args = append(args, *source.SpaceID, source.OwnerUID)
		}
	}
	from := ` FROM plugins p LEFT JOIN plugin_version_releases v ON v.plugin_id=p.plugin_id AND v.version_id=p.published_version_id LEFT JOIN plugin_versions snapshot ON snapshot.plugin_id=v.plugin_id AND snapshot.version_id=v.version_id LEFT JOIN plugin_relations edge ON edge.source_plugin_id=? AND edge.relation_type=? AND ? AND edge.target_plugin_id=p.plugin_id AND edge.status=1 AND edge.deleted_at IS NULL WHERE p.plugin_type=? AND p.is_embedded=0 AND p.status=1 AND p.deleted_at IS NULL AND p.listing_state='published' AND (` + audience + `)`
	var total int
	if err = r.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT p.plugin_id)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT p.plugin_id,CASE WHEN p.plugin_type='connector' THEN p.plugin_name ELSE COALESCE(JSON_UNQUOTE(JSON_EXTRACT(snapshot.manifest_json,'$.plugin_name')),'') END AS candidate_name,p.plugin_type,v.version_id,(edge.relation_id IS NOT NULL)`+from+` ORDER BY candidate_name,p.plugin_id LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ReferenceCandidate{}
	for rows.Next() {
		var x ReferenceCandidate
		if err = rows.Scan(&x.PluginID, &x.Name, &x.Type, &x.VersionID, &x.AlreadyBound); err != nil {
			return nil, 0, err
		}
		if !editable {
			x.AlreadyBound = bound[x.PluginID]
		}
		x.IsSelectable = !x.AlreadyBound && (x.Type == model.PluginTypeConnector || x.VersionID != nil)
		if !x.IsSelectable {
			x.Reason = "already_bound"
			if !x.AlreadyBound {
				x.Reason = "release_baseline_required"
			}
		}
		out = append(out, x)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read reference candidates: %w", err)
	}
	return out, total, nil
}
