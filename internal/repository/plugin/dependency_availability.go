package plugin

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// A released resource uses its immutable reference index. Legacy resources
// without a published version pointer use their current live relations. In
// particular, a later draft edit must not change a listed release's audience.
func unavailableDirectSQL(source string) string {
	// The helper is internal and receives only fixed aliases. Reject anything
	// else so a future caller cannot turn the concatenation into an injection
	// surface; both SQL fragments below are composed from these constants.
	if source != "p" && source != "member" {
		panic("unsupported dependency availability source alias")
	}
	refBad := unavailableTargetSQL("ref", "target", true)
	liveBad := unavailableTargetSQL("live", "target", false)
	return `(EXISTS (SELECT 1 FROM plugin_version_relation_refs ref LEFT JOIN plugins target ON target.plugin_id=ref.target_plugin_id WHERE ref.source_plugin_id=` + source + `.plugin_id AND ref.source_version_id=` + source + `.published_version_id AND ` + refBad + `)
 OR (` + source + `.published_version_id IS NULL AND EXISTS (SELECT 1 FROM plugin_relations live LEFT JOIN plugins target ON target.plugin_id=live.target_plugin_id WHERE live.source_plugin_id=` + source + `.plugin_id AND live.status=1 AND live.deleted_at IS NULL AND live.relation_type IN ('expert_skill','expert_connector','expert_team_expert') AND ` + liveBad + `)))`
}

func unavailableTargetSQL(edge, target string, formal bool) string {
	releaseMissing := edge + `.relation_type IN ('expert_skill','expert_team_expert') AND ` + edge + `.target_version_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM plugin_version_releases dep_release WHERE dep_release.plugin_id=` + edge + `.target_plugin_id AND dep_release.version_id=` + edge + `.target_version_id)`
	if formal {
		// A released reference without a pin is checked against the target's
		// published release, matching dependencyEdges'
		// COALESCE(edge.target_version_id,target.published_version_id) join.
		releaseMissing = edge + `.relation_type IN ('expert_skill','expert_team_expert') AND NOT EXISTS (SELECT 1 FROM plugin_version_releases dep_release WHERE dep_release.plugin_id=` + edge + `.target_plugin_id AND dep_release.version_id=COALESCE(` + edge + `.target_version_id,` + target + `.published_version_id))`
	}
	return `(` + target + `.plugin_id IS NULL OR ` + target + `.status<>1 OR ` + target + `.deleted_at IS NOT NULL OR ` + target + `.listing_state<>'published' OR (` + releaseMissing + `))`
}

// unavailableDependencySQL is used by the catalog list, tag and category
// counts. The fixed relation matrix permits only expert -> skill/connector and
// team -> expert, so two levels cover every published dependency path.
func unavailableDependencySQL(source string) string {
	member := `(EXISTS (SELECT 1 FROM plugin_version_relation_refs member_ref WHERE member_ref.source_plugin_id=` + source + `.plugin_id AND member_ref.source_version_id=` + source + `.published_version_id AND member_ref.relation_type='expert_team_expert' AND member_ref.target_plugin_id=member.plugin_id)
 OR (` + source + `.published_version_id IS NULL AND EXISTS (SELECT 1 FROM plugin_relations member_live WHERE member_live.source_plugin_id=` + source + `.plugin_id AND member_live.relation_type='expert_team_expert' AND member_live.target_plugin_id=member.plugin_id AND member_live.status=1 AND member_live.deleted_at IS NULL)))`
	return `(` + source + `.plugin_type IN ('expert','expert_team') AND (` + unavailableDirectSQL(source) + ` OR (` + source + `.plugin_type='expert_team' AND EXISTS (SELECT 1 FROM plugins member WHERE ` + member + ` AND ` + unavailableDirectSQL("member") + `))))`
}

func dependencyAvailabilitySQL() string { return ` AND NOT ` + unavailableDependencySQL("p") }

// HasUnavailableDependencies is the name-free public detail gate. It checks
// the same predicate as catalog pagination without reading target metadata.
func (r *Repo) HasUnavailableDependencies(ctx context.Context, scope Scope, pluginID string) (bool, error) {
	where, args := `p.plugin_id=? AND p.status=1 AND p.deleted_at IS NULL AND p.listing_state='published'`, []any{pluginID}
	if !scope.Admin {
		where += ` AND ` + visibilitySQL
		args = append(args, scope.SpaceID, scope.CallerUID)
	}
	var blocked bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plugins p WHERE `+where+` AND `+unavailableDependencySQL("p")+`)`, args...).Scan(&blocked)
	return blocked, err
}

type dependencyEdge struct {
	SourceID, TargetID, RelationType, TargetName string
	TargetVersionID                              *string
	TargetExists, TargetActive, TargetListed     bool
	ReleaseExists, Formal                        bool
}

func (e dependencyEdge) reason() string {
	switch {
	case !e.TargetExists || !e.TargetActive:
		return "deleted"
	case !e.TargetListed:
		return "delisted"
	case e.RelationType != "expert_connector" && (e.TargetVersionID != nil || e.Formal) && !e.ReleaseExists:
		return "release_missing"
	default:
		return ""
	}
}

// dependencyEdges reads one bounded wave for a page of already-authorized
// source plugins. A released name is selected from the target's published
// snapshot; mutable target names never leak through this diagnostic response.
func (r *Repo) dependencyEdges(ctx context.Context, ids []string) ([]dependencyEdge, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	qMarks := placeholders(len(ids))
	query := `SELECT edge.source_plugin_id,edge.target_plugin_id,edge.target_version_id,edge.relation_type,
 target.plugin_id IS NOT NULL,COALESCE(target.status=1 AND target.deleted_at IS NULL,0),COALESCE(target.listing_state='published',0),
 dep_release.version_id IS NOT NULL,edge.formal,
 CASE WHEN edge.relation_type='expert_connector' THEN COALESCE(target.plugin_name,'')
 ELSE COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(snapshot.manifest_json,'$.plugin_name')),''),target.plugin_name,'') END
 FROM (
 SELECT ref.source_plugin_id,ref.target_plugin_id,ref.target_version_id,ref.relation_type,1 AS formal
 FROM plugin_version_relation_refs ref JOIN plugins src ON src.plugin_id=ref.source_plugin_id AND src.published_version_id=ref.source_version_id
 WHERE ref.source_plugin_id IN (` + qMarks + `) AND ref.relation_type IN ('expert_skill','expert_connector','expert_team_expert')
 UNION ALL
 SELECT live.source_plugin_id,live.target_plugin_id,live.target_version_id,live.relation_type,0 AS formal
 FROM plugin_relations live JOIN plugins src ON src.plugin_id=live.source_plugin_id AND src.published_version_id IS NULL
 WHERE live.source_plugin_id IN (` + qMarks + `) AND live.status=1 AND live.deleted_at IS NULL AND live.relation_type IN ('expert_skill','expert_connector','expert_team_expert')
 ) edge LEFT JOIN plugins target ON target.plugin_id=edge.target_plugin_id
 LEFT JOIN plugin_version_releases dep_release ON dep_release.plugin_id=target.plugin_id AND dep_release.version_id=COALESCE(edge.target_version_id,target.published_version_id)
 LEFT JOIN plugin_versions snapshot ON snapshot.plugin_id=dep_release.plugin_id AND snapshot.version_id=dep_release.version_id
 ORDER BY edge.source_plugin_id,edge.relation_type,edge.target_plugin_id`
	args := make([]any, 0, 2*len(ids))
	for i := 0; i < 2; i++ {
		for _, id := range ids {
			args = append(args, id)
		}
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read dependency edges: %w", err)
	}
	defer rows.Close()
	out := []dependencyEdge{}
	for rows.Next() {
		var e dependencyEdge
		var pin sql.NullString
		if err := rows.Scan(&e.SourceID, &e.TargetID, &pin, &e.RelationType, &e.TargetExists, &e.TargetActive, &e.TargetListed, &e.ReleaseExists, &e.Formal, &e.TargetName); err != nil {
			return nil, fmt.Errorf("scan dependency edge: %w", err)
		}
		e.TargetVersionID = nullString(pin)
		out = append(out, e)
	}
	return out, rows.Err()
}

// DependencyIssues returns diagnostics only for roots already selected under
// the caller's Scope. The caller supplies no arbitrary child IDs; the second
// wave follows only member edges from those roots.
func (r *Repo) DependencyIssues(ctx context.Context, scope Scope, roots []model.Plugin) (map[string][]model.DependencyIssue, error) {
	result := make(map[string][]model.DependencyIssue, len(roots))
	ids := make([]string, 0, len(roots))
	teams := map[string]bool{}
	for _, p := range roots {
		if p.ListingState != model.PluginListingStatePublished || (p.Type != model.PluginTypeExpert && p.Type != model.PluginTypeExpertTeam) {
			continue
		}
		ids = append(ids, p.ID)
		teams[p.ID] = p.Type == model.PluginTypeExpertTeam
	}
	if len(ids) > 0 && !scope.Admin {
		// The diagnostic query reads even delisted/deleted targets. Re-check only
		// the authorized roots here, so arbitrary IDs cannot turn it into an
		// unscoped target-name oracle if a caller is added in the future.
		args := make([]any, 0, len(ids)+2)
		for _, id := range ids {
			args = append(args, id)
		}
		args = append(args, scope.CallerUID, scope.SpaceID)
		var count int
		if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugins p WHERE p.plugin_id IN (`+placeholders(len(ids))+`) AND p.status=1 AND p.deleted_at IS NULL AND p.owner_uid=? AND p.space_id=?`, args...).Scan(&count); err != nil {
			return nil, err
		}
		if count != len(ids) {
			return nil, ErrNotFound
		}
	}
	first, err := r.dependencyEdges(ctx, ids)
	if err != nil {
		return nil, err
	}
	members := map[string][]string{}
	for _, e := range first {
		if reason := e.reason(); reason != "" {
			result[e.SourceID] = append(result[e.SourceID], model.DependencyIssue{TargetPluginID: e.TargetID, TargetName: e.TargetName, RelationType: e.RelationType, Reason: reason, Via: []string{}})
		}
		if teams[e.SourceID] && e.RelationType == "expert_team_expert" && e.TargetExists && e.TargetActive {
			members[e.TargetID] = append(members[e.TargetID], e.SourceID)
		}
	}
	memberIDs := make([]string, 0, len(members))
	for id := range members {
		memberIDs = append(memberIDs, id)
	}
	sort.Strings(memberIDs)
	for start := 0; start < len(memberIDs); start += 100 {
		end := min(start+100, len(memberIDs))
		second, err := r.dependencyEdges(ctx, memberIDs[start:end])
		if err != nil {
			return nil, err
		}
		for _, e := range second {
			if reason := e.reason(); reason != "" {
				for _, rootID := range members[e.SourceID] {
					result[rootID] = append(result[rootID], model.DependencyIssue{TargetPluginID: e.TargetID, TargetName: e.TargetName, RelationType: e.RelationType, Reason: reason, Via: []string{e.SourceID}})
				}
			}
		}
	}
	for id := range result {
		sort.Slice(result[id], func(i, j int) bool {
			a, b := result[id][i], result[id][j]
			return strings.Join(a.Via, "/")+a.RelationType+a.TargetPluginID < strings.Join(b.Via, "/")+b.RelationType+b.TargetPluginID
		})
	}
	return result, nil
}
