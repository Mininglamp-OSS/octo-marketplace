package plugin

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Dependency availability reads the immutable relations snapshot of the
// released root (and of the team's pinned member version), so it works even
// when the optional reference index was never built. Legacy resources without
// a published pointer use their current live relations.
type availabilityAlias string

const (
	availabilityRoot   availabilityAlias = "p"
	availabilityMember availabilityAlias = "member"
)

func (a availabilityAlias) sql() string {
	if a != availabilityRoot && a != availabilityMember {
		panic("unsupported dependency availability source alias")
	}
	return string(a)
}

const snapshotRelationColumns = `relation_type VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci PATH '$.relation_type',
 relation_type_legacy VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci PATH '$.Type',
 target_plugin_id VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci PATH '$.target_plugin_id',
 target_plugin_id_legacy VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci PATH '$.TargetPluginID',
 target_version_id VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci PATH '$.target_version_id',
 target_version_id_legacy VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci PATH '$.TargetVersionID',
 status INT PATH '$.status',
 status_legacy INT PATH '$.Status',
 deleted_at VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci PATH '$.deleted_at',
 deleted_at_legacy VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci PATH '$.DeletedAt'`

// snapshotField accepts both the canonical snake_case snapshot document and the
// historical Go field names still accepted by model.DecodeSnapshotRelations.
func snapshotField(alias, name string) string {
	return `COALESCE(` + alias + `.` + name + `,` + alias + `.` + name + `_legacy)`
}

// snapshotTargetUnavailableSQL is shared by the root and the pinned member
// snapshot. An unpinned edge (supported when references are disabled) is
// checked against the target's published release, matching diagnostics.
func snapshotTargetUnavailableSQL(edge, target string) string {
	edgeType := snapshotField(edge, "relation_type")
	edgeTarget := snapshotField(edge, "target_plugin_id")
	edgePin := snapshotField(edge, "target_version_id")
	return `(` + target + `.plugin_id IS NULL OR ` + target + `.status<>1 OR ` + target + `.deleted_at IS NOT NULL OR ` + target + `.listing_state<>'published' OR (` + edgeType + ` IN ('expert_skill','expert_team_expert') AND NOT EXISTS (SELECT 1 FROM plugin_version_releases dep_release WHERE dep_release.plugin_id=` + edgeTarget + ` AND dep_release.version_id=COALESCE(` + edgePin + `,` + target + `.published_version_id))))`
}

func snapshotMemberUnavailableSQL(edge, target string) string {
	edgeTarget := snapshotField(edge, "target_plugin_id")
	edgePin := snapshotField(edge, "target_version_id")
	memberType := snapshotField("member_edge", "relation_type")
	return `EXISTS (SELECT 1 FROM plugin_versions member_version
 JOIN JSON_TABLE(member_version.relations_json, '$[*]' COLUMNS (` + snapshotRelationColumns + `)) member_edge
 LEFT JOIN plugins member_target ON member_target.plugin_id=` + snapshotField("member_edge", "target_plugin_id") + `
 WHERE member_version.plugin_id=` + edgeTarget + `
   AND member_version.version_id=COALESCE(` + edgePin + `,` + target + `.published_version_id)
   AND ` + memberType + ` IN ('expert_skill','expert_connector')
   AND COALESCE(member_edge.status,member_edge.status_legacy,1)=1
   AND COALESCE(member_edge.deleted_at,member_edge.deleted_at_legacy) IS NULL
   AND ` + snapshotTargetUnavailableSQL("member_edge", "member_target") + `)`
}

func unavailableSnapshotSQL(sourceID, sourceVersion string) string {
	edgeType := snapshotField("edge", "relation_type")
	return `EXISTS (SELECT 1 FROM plugin_versions source_version
 JOIN JSON_TABLE(source_version.relations_json, '$[*]' COLUMNS (` + snapshotRelationColumns + `)) edge
 LEFT JOIN plugins target ON target.plugin_id=` + snapshotField("edge", "target_plugin_id") + `
 WHERE source_version.plugin_id=` + sourceID + ` AND source_version.version_id=` + sourceVersion + `
   AND ` + edgeType + ` IN ('expert_skill','expert_connector','expert_team_expert')
   AND COALESCE(edge.status,edge.status_legacy,1)=1
   AND COALESCE(edge.deleted_at,edge.deleted_at_legacy) IS NULL
   AND (` + snapshotTargetUnavailableSQL("edge", "target") + ` OR (` + edgeType + `='expert_team_expert' AND ` + snapshotMemberUnavailableSQL("edge", "target") + `)))`
}

func liveTargetUnavailableSQL(edge, target string) string {
	return `(` + target + `.plugin_id IS NULL OR ` + target + `.status<>1 OR ` + target + `.deleted_at IS NOT NULL OR ` + target + `.listing_state<>'published' OR (` + edge + `.relation_type IN ('expert_skill','expert_team_expert') AND ` + edge + `.target_version_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM plugin_version_releases dep_release WHERE dep_release.plugin_id=` + edge + `.target_plugin_id AND dep_release.version_id=` + edge + `.target_version_id)))`
}

// A legacy team member is an expert, so its live graph is only skills and
// connectors. Keeping this fragment separate avoids recursively embedding the
// team branch while building unavailableLiveSQL.
func unavailableMemberLiveSQL(source availabilityAlias) string {
	s := source.sql()
	return `EXISTS (SELECT 1 FROM plugin_relations live
 LEFT JOIN plugins target ON target.plugin_id=live.target_plugin_id
 WHERE live.source_plugin_id=` + s + `.plugin_id AND live.status=1 AND live.deleted_at IS NULL
   AND live.relation_type IN ('expert_skill','expert_connector')
   AND ` + liveTargetUnavailableSQL("live", "target") + `)`
}

func unavailableLiveSQL(source availabilityAlias) string {
	s := source.sql()
	member := `EXISTS (SELECT 1 FROM plugins member WHERE member.plugin_id=live.target_plugin_id AND (
 (live.target_version_id IS NOT NULL AND ` + unavailableSnapshotSQL("member.plugin_id", "live.target_version_id") + `)
 OR (live.target_version_id IS NULL AND (
   (member.published_version_id IS NOT NULL AND ` + unavailableSnapshotSQL("member.plugin_id", "member.published_version_id") + `)
   OR (member.published_version_id IS NULL AND ` + unavailableMemberLiveSQL(availabilityMember) + `)
 ))
))`
	return `EXISTS (SELECT 1 FROM plugin_relations live
 LEFT JOIN plugins target ON target.plugin_id=live.target_plugin_id
 WHERE live.source_plugin_id=` + s + `.plugin_id AND live.status=1 AND live.deleted_at IS NULL
   AND live.relation_type IN ('expert_skill','expert_connector','expert_team_expert')
   AND (` + liveTargetUnavailableSQL("live", "target") + ` OR (live.relation_type='expert_team_expert' AND ` + member + `)))`
}

// unavailableDependencySQL is used by catalog list, tag/category counts, detail
// and graph. Released roots use their frozen snapshot; only legacy roots without
// a published pointer inspect editable live relations. The fixed relation matrix
// permits expert -> skill/connector and team -> expert, so two levels cover every
// published dependency path.
func unavailableDependencySQL(source availabilityAlias) string {
	s := source.sql()
	return `(` + s + `.plugin_type IN ('expert','expert_team') AND ((` + s + `.published_version_id IS NOT NULL AND ` + unavailableSnapshotSQL(s+".plugin_id", s+".published_version_id") + `) OR (` + s + `.published_version_id IS NULL AND ` + unavailableLiveSQL(source) + `)))`
}

func dependencyAvailabilitySQL() string {
	return ` AND NOT ` + unavailableDependencySQL(availabilityRoot)
}

// HasUnavailableDependencies is the name-free public detail gate. It checks
// the same predicate as catalog pagination without reading target metadata.
func (r *Repo) HasUnavailableDependencies(ctx context.Context, scope Scope, pluginID string) (bool, error) {
	where, args := `p.plugin_id=? AND p.status=1 AND p.deleted_at IS NULL AND p.listing_state='published'`, []any{pluginID}
	if !scope.Admin {
		where += ` AND ` + visibilitySQL
		args = append(args, scope.SpaceID, scope.CallerUID)
	}
	var blocked bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plugins p WHERE `+where+` AND `+unavailableDependencySQL(availabilityRoot)+`)`, args...).Scan(&blocked)
	return blocked, err
}

type dependencyEdge struct {
	TargetID, RelationType, TargetName  string
	TargetVersionID                     *string
	TargetExists, TargetActive          bool
	TargetListed, ReleaseExists, Formal bool
	RootID                              string
	Via                                 []string
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

// loadDependencyEdges reads a source's immutable released snapshot, falling
// back to editable live relations only for a legacy row without a published
// pointer. It never depends on the optional reference index.
func (r *Repo) loadDependencyEdges(ctx context.Context, sourceID string) ([]dependencyEdge, error) {
	var published sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT published_version_id FROM plugins WHERE plugin_id=? AND status=1 AND deleted_at IS NULL`, sourceID).Scan(&published)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if published.Valid && published.String != "" {
		return r.loadSnapshotEdges(ctx, sourceID, published.String)
	}
	return r.loadLiveEdges(ctx, sourceID)
}

func (r *Repo) loadSnapshotEdges(ctx context.Context, sourceID, versionID string) ([]dependencyEdge, error) {
	var raw []byte
	err := r.db.QueryRowContext(ctx, `SELECT relations_json FROM plugin_versions WHERE plugin_id=? AND version_id=?`, sourceID, versionID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rels, err := model.DecodeSnapshotRelations(raw)
	if err != nil {
		return nil, err
	}
	out := []dependencyEdge{}
	for _, rel := range rels {
		if rel.Status != 1 || rel.DeletedAt != nil || (rel.Type != "expert_skill" && rel.Type != "expert_connector" && rel.Type != "expert_team_expert") {
			continue
		}
		out = append(out, dependencyEdge{TargetID: rel.TargetPluginID, RelationType: rel.Type, TargetVersionID: rel.TargetVersionID, Formal: true})
	}
	return out, nil
}

func (r *Repo) loadLiveEdges(ctx context.Context, sourceID string) ([]dependencyEdge, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT target_plugin_id,target_version_id,relation_type FROM plugin_relations WHERE source_plugin_id=? AND status=1 AND deleted_at IS NULL AND relation_type IN ('expert_skill','expert_connector','expert_team_expert')`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dependencyEdge{}
	for rows.Next() {
		var e dependencyEdge
		var pin sql.NullString
		if err := rows.Scan(&e.TargetID, &pin, &e.RelationType); err != nil {
			return nil, err
		}
		e.TargetVersionID = nullString(pin)
		out = append(out, e)
	}
	return out, rows.Err()
}

// batchDependencyTargetInfo resolves target state, release presence and released
// names for all diagnostic edges with two queries instead of one query per edge.
func (r *Repo) batchDependencyTargetInfo(ctx context.Context, edges []*dependencyEdge) error {
	if len(edges) == 0 {
		return nil
	}
	idSet := map[string]struct{}{}
	for _, e := range edges {
		idSet[e.TargetID] = struct{}{}
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	type targetMeta struct {
		exists, active, listed bool
		published, name        string
	}
	meta := make(map[string]targetMeta, len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT p.plugin_id,
 COALESCE(p.status=1 AND p.deleted_at IS NULL,0),
 COALESCE(p.listing_state='published',0),
 p.published_version_id,
 CASE WHEN p.plugin_type='connector' THEN COALESCE(p.plugin_name,'') ELSE COALESCE(JSON_UNQUOTE(JSON_EXTRACT(snapshot.manifest_json,'$.plugin_name')),'') END
 FROM plugins p
 LEFT JOIN plugin_versions snapshot ON snapshot.plugin_id=p.plugin_id AND snapshot.version_id=p.published_version_id
 WHERE p.plugin_id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var active, listed int
		var published sql.NullString
		var name string
		if err := rows.Scan(&id, &active, &listed, &published, &name); err != nil {
			rows.Close()
			return err
		}
		meta[id] = targetMeta{exists: true, active: active != 0, listed: listed != 0, published: published.String, name: name}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	versionSet := map[string]struct{}{}
	for _, e := range edges {
		m, ok := meta[e.TargetID]
		if !ok {
			continue
		}
		e.TargetExists, e.TargetActive, e.TargetListed, e.TargetName = m.exists, m.active, m.listed, m.name
		version := m.published
		if e.TargetVersionID != nil {
			version = *e.TargetVersionID
		}
		if version != "" {
			versionSet[version] = struct{}{}
		}
	}
	if len(versionSet) == 0 {
		return nil
	}
	versions := make([]string, 0, len(versionSet))
	for version := range versionSet {
		versions = append(versions, version)
	}
	releaseArgs := make([]any, 0, len(ids)+len(versions))
	for _, id := range ids {
		releaseArgs = append(releaseArgs, id)
	}
	for _, version := range versions {
		releaseArgs = append(releaseArgs, version)
	}
	releaseRows, err := r.db.QueryContext(ctx, `SELECT plugin_id,version_id FROM plugin_version_releases WHERE plugin_id IN (`+placeholders(len(ids))+`) AND version_id IN (`+placeholders(len(versions))+`)`, releaseArgs...)
	if err != nil {
		return err
	}
	releaseSet := map[string]struct{}{}
	for releaseRows.Next() {
		var id, version string
		if err := releaseRows.Scan(&id, &version); err != nil {
			releaseRows.Close()
			return err
		}
		releaseSet[id+"\x00"+version] = struct{}{}
	}
	if err := releaseRows.Close(); err != nil {
		return err
	}
	if err := releaseRows.Err(); err != nil {
		return err
	}
	for _, e := range edges {
		m, ok := meta[e.TargetID]
		if !ok {
			continue
		}
		version := m.published
		if e.TargetVersionID != nil {
			version = *e.TargetVersionID
		}
		_, e.ReleaseExists = releaseSet[e.TargetID+"\x00"+version]
	}
	return nil
}

// DependencyIssues returns diagnostics only for roots already selected under
// the caller's Scope. The caller supplies no arbitrary child IDs; the second
// wave follows only member edges from those roots, using the team's pinned
// member version when one exists. Target metadata for both waves is batched.
func (r *Repo) DependencyIssues(ctx context.Context, scope Scope, roots []model.Plugin) (map[string][]model.DependencyIssue, error) {
	result := make(map[string][]model.DependencyIssue, len(roots))
	ids := make([]string, 0, len(roots))
	for _, p := range roots {
		if p.ListingState != model.PluginListingStatePublished || (p.Type != model.PluginTypeExpert && p.Type != model.PluginTypeExpertTeam) {
			continue
		}
		ids = append(ids, p.ID)
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
	var edges []*dependencyEdge
	for _, root := range roots {
		if root.ListingState != model.PluginListingStatePublished || (root.Type != model.PluginTypeExpert && root.Type != model.PluginTypeExpertTeam) {
			continue
		}
		rootEdges, err := r.loadDependencyEdges(ctx, root.ID)
		if err != nil {
			return nil, err
		}
		for i := range rootEdges {
			e := &rootEdges[i]
			e.RootID = root.ID
			edges = append(edges, e)
		}
		if root.Type != model.PluginTypeExpertTeam {
			continue
		}
		for _, e := range rootEdges {
			if e.RelationType != "expert_team_expert" {
				continue
			}
			var memberEdges []dependencyEdge
			if e.TargetVersionID != nil {
				memberEdges, err = r.loadSnapshotEdges(ctx, e.TargetID, *e.TargetVersionID)
			} else {
				memberEdges, err = r.loadDependencyEdges(ctx, e.TargetID)
			}
			if err != nil {
				return nil, err
			}
			for j := range memberEdges {
				me := &memberEdges[j]
				me.RootID = root.ID
				me.Via = []string{e.TargetID}
				edges = append(edges, me)
			}
		}
	}
	if err := r.batchDependencyTargetInfo(ctx, edges); err != nil {
		return nil, err
	}
	for _, e := range edges {
		if reason := e.reason(); reason != "" {
			result[e.RootID] = append(result[e.RootID], model.DependencyIssue{TargetPluginID: e.TargetID, TargetName: e.TargetName, RelationType: e.RelationType, Reason: reason, Via: e.Via})
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
