package plugin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// ListFilter restricts a scoped Plugin listing. Sort accepts newest (default),
// oldest, updated, name, placement, views, installs, downloads, or
// comprehensive; callers validate unsupported values.
type ListFilter struct {
	PlacementCode string
	Type          model.PluginType
	CategoryID    string
	Tags          []string
	Keyword       string
	Mine          bool
	Sort          string
	Limit         int
	Offset        int
	// AllSpaces drops the per-Space visibility predicate — set ONLY by the admin
	// service, never from a caller-supplied filter. Visibility optionally narrows
	// the admin listing to one visibility class (e.g. system connectors).
	AllSpaces  bool
	Visibility model.PluginVisibility
	// IncludeReviewState appends the two review facts a displayed listing status
	// needs, so 我的发布 does not have to fetch the review list and join client-side.
	//
	// Set ONLY for mode=mine. The marketplace grid is the hot path and every row in
	// it is published by construction, so its display status is a constant and the
	// two extra correlated subqueries per row would buy nothing.
	IncludeReviewState bool
}

// pluginMetric embeds a correlated counter lookup, mirroring the expert list
// pattern: join-free, so the placement GROUP BY stays trivially valid and
// missing metric rows read as zero.
func pluginMetric(column string) string {
	return `COALESCE((SELECT rm.` + column + ` FROM resource_metrics rm WHERE rm.resource_type='plugin' AND rm.resource_id=p.plugin_id),0)`
}

var pluginMetricColumns = `,` + pluginMetric("view_count") + `,` + pluginMetric("install_count") + `,` + pluginMetric("download_count")

// pluginReviewStateColumns resolves the review half of the displayed status.
//
// has_pending_review stays a separate EXISTS rather than being inferred from
// latest_review_status='pending': the precedence rule is about EXISTENCE, and an
// EXISTS on (plugin_id, status) is the cheapest question this schema can answer.
//
// The `(status='pending') DESC` leading sort key is what makes "the review the
// author cares about" deterministic. The single-pending invariant guarantees at
// most one such row, so it sorts to the top when present and the ordering
// degrades to plain latest-by-submission otherwise.
const pluginReviewStateColumns = `,EXISTS(SELECT 1 FROM plugin_review_requests rr
  WHERE rr.plugin_id=p.plugin_id AND rr.status='pending' AND rr.deleted_at IS NULL)
,(SELECT rr2.review_id FROM plugin_review_requests rr2
  WHERE rr2.plugin_id=p.plugin_id AND rr2.deleted_at IS NULL
  ORDER BY (rr2.status='pending') DESC, rr2.submitted_at DESC, rr2.review_id DESC LIMIT 1)
,(SELECT rr3.status FROM plugin_review_requests rr3
  WHERE rr3.plugin_id=p.plugin_id AND rr3.deleted_at IS NULL
  ORDER BY (rr3.status='pending') DESC, rr3.submitted_at DESC, rr3.review_id DESC LIMIT 1)`

func (r *Repo) Get(ctx context.Context, scope Scope, pluginID string) (*model.Plugin, error) {
	var row *sql.Row
	if scope.Admin {
		row = r.db.QueryRowContext(ctx, `SELECT `+pluginColumns+pluginMetricColumns+` FROM plugins p
WHERE p.plugin_id=? AND p.status=1 AND p.deleted_at IS NULL`, pluginID)
	} else {
		row = r.db.QueryRowContext(ctx, `SELECT `+pluginColumns+pluginMetricColumns+` FROM plugins p
WHERE p.plugin_id=? AND p.status=1 AND p.deleted_at IS NULL AND `+visibilitySQL, pluginID, scope.SpaceID, scope.CallerUID)
	}
	p, err := scanPluginWithMetrics(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// List returns one page and the total number of rows matching the same scoped filters.
func (r *Repo) List(ctx context.Context, scope Scope, f ListFilter) ([]model.Plugin, int64, error) {
	from, where, args := buildListQuery(scope, f)
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT p.plugin_id) `+from+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	queryArgs := append(append([]any(nil), args...), limit, max(f.Offset, 0))
	// The placement join can yield one row per (placement_code, category) pair
	// for the same plugin; collapse to one row so the page matches the total.
	group := ``
	if f.PlacementCode != "" {
		group = ` GROUP BY p.plugin_id`
	}
	reviewState := ``
	if f.IncludeReviewState {
		reviewState = pluginReviewStateColumns
	}
	q := `SELECT ` + pluginSummaryColumns + pluginMetricColumns + reviewState + from + where + group + listOrder(f) + ` LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(ctx, q, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.Plugin
	for rows.Next() {
		p, err := scanPluginRow(rows, false, true, f.IncludeReviewState)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// listedSQL is the marketplace GRID's listing gate: only a published row is on
// the shelf. It is deliberately NOT part of visibilitySQL, which is a scope rule
// and still lets an owner READ their own draft (detail, versions, edit).
//
// Every surface that mirrors the grid must apply it — the grid itself
// (buildListQuery), its tag chips (ListTags) and its category counts
// (ListPlacementCategories) — or a facet advertises rows the grid will not
// return: a chip that filters to nothing, a count that overstates the page. The
// "mine" variants of those surfaces deliberately omit it, because 我的发布 shows
// every state, and so does the admin listing (AllSpaces).
//
// 'published' is a literal, not a placeholder, so appending it never disturbs a
// call site's argument list.
const listedSQL = ` AND p.listing_state='published'`

func buildListQuery(scope Scope, f ListFilter) (string, string, []any) {
	from := ` FROM plugins p`
	args := []any{}
	if f.PlacementCode != "" {
		from += ` JOIN plugin_placements pp ON pp.plugin_id=p.plugin_id AND pp.placement_code=? AND pp.visible=1`
		args = append(args, f.PlacementCode)
	}
	// Embedded plugins (promoted from a parent's JSON) are parts of a parent
	// asset: reachable via detail and relations, never listed as catalog entries.
	where := ` WHERE p.status=1 AND p.deleted_at IS NULL AND p.is_embedded=0`
	if f.AllSpaces {
		// Admin listing: no per-Space scoping. Never reachable from a caller
		// filter — only the admin service sets AllSpaces.
	} else {
		where += ` AND ` + visibilitySQL
		args = append(args, scope.SpaceID, scope.CallerUID)
	}
	if f.Visibility != "" {
		where += ` AND p.visibility=?`
		args = append(args, string(f.Visibility))
	}
	if f.Type != "" {
		where += ` AND p.plugin_type=?`
		args = append(args, f.Type)
	}
	if f.CategoryID != "" {
		if f.PlacementCode != "" {
			where += ` AND pp.category_id=?`
		} else {
			where += ` AND p.category_id=?`
		}
		args = append(args, f.CategoryID)
	}
	// Tag filter is AND, matching the legacy MCP list semantics: a row must
	// carry every selected tag, not the union that OR would surface.
	for _, tag := range f.Tags {
		where += ` AND JSON_CONTAINS(p.tags_json, JSON_QUOTE(?), '$')`
		args = append(args, tag)
	}
	if f.Keyword != "" {
		where += ` AND p.plugin_name LIKE ? ESCAPE '!'`
		args = append(args, "%"+escapeLike(f.Keyword)+"%")
	}
	if f.Mine {
		where += ` AND p.owner_uid=? AND p.space_id=?`
		args = append(args, scope.CallerUID, scope.SpaceID)
	} else if !f.AllSpaces {
		// The marketplace GRID shows listed plugins only — including to the author.
		// That exclusion is what distinguishes a private draft from a published
		// private plugin; without it "save as draft" and "publish" would look
		// identical to the person who just pressed one of them.
		//
		// This is a LIST rule, not a scope rule: visibilitySQL deliberately still
		// lets an owner READ their own draft (detail, versions, edit), and Mine —
		// which backs 我的发布 — deliberately shows every state. The admin listing
		// (AllSpaces) manages rows in all states and is exempt.
		where += listedSQL
	}
	return from, where, args
}

func listOrder(f ListFilter) string {
	recent := `p.created_at DESC,p.plugin_id DESC`
	switch f.Sort {
	case "oldest":
		return ` ORDER BY p.created_at ASC,p.plugin_id ASC`
	case "updated":
		return ` ORDER BY p.updated_at DESC,p.plugin_id DESC`
	case "name":
		return ` ORDER BY p.plugin_name ASC,p.plugin_id ASC`
	case "views":
		return ` ORDER BY ` + pluginMetric("view_count") + ` DESC,` + recent
	case "installs":
		return ` ORDER BY ` + pluginMetric("install_count") + ` DESC,` + recent
	case "downloads":
		return ` ORDER BY ` + pluginMetric("download_count") + ` DESC,` + recent
	case "comprehensive":
		// Mirrors the expert/skill catalog ranking (repository/expert/list.go —
		// keep the weights in sync): installs 5×, views 1×, plus a recency boost
		// decaying over days so fresh listings still surface.
		return ` ORDER BY (` + pluginMetric("install_count") + ` * 5 + ` + pluginMetric("view_count") +
			` + 20 / POW(TIMESTAMPDIFF(HOUR, p.created_at, NOW()) / 24 + 2, 1.2)) DESC,` + recent
	case "placement":
		if f.PlacementCode != "" {
			return ` ORDER BY MIN(pp.sort_order) ASC,p.plugin_id ASC`
		}
	}
	return ` ORDER BY ` + recent
}

func escapeLike(s string) string {
	r := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return r.Replace(s)
}

// GetWithRelations returns only live one-level relations whose target is also visible.
func (r *Repo) GetWithRelations(ctx context.Context, scope Scope, pluginID string) (*model.Plugin, []model.PluginRelation, error) {
	p, err := r.Get(ctx, scope, pluginID)
	if err != nil {
		return nil, nil, err
	}
	relWhere, relArgs := visibilitySQL, []any{pluginID, scope.SpaceID, scope.CallerUID}
	if scope.Admin {
		relWhere, relArgs = "1=1", []any{pluginID}
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+graphEdgeColumns+`
FROM plugin_relations r JOIN plugins p ON p.plugin_id=r.target_plugin_id
WHERE r.source_plugin_id=? AND r.status=1 AND r.deleted_at IS NULL AND p.status=1 AND p.deleted_at IS NULL AND `+relWhere+`
ORDER BY r.sort_order,r.relation_id`, relArgs...)
	if err != nil {
		return nil, nil, err
	}
	rels, err := scanRelationRows(rows)
	if err != nil {
		return nil, nil, err
	}
	return p, rels, nil
}

// graphEdgeColumns is the column set used by graph edge queries. It is the
// same 12 columns GetWithRelations selects, so both paths share a scan.
const graphEdgeColumns = `r.relation_id,r.source_plugin_id,r.target_plugin_id,p.plugin_type,r.relation_type,r.sort_order,
 r.relation_json,r.status,r.created_by,r.created_at,r.updated_at,r.deleted_at`

// graphEdgeWhere returns the target-visibility predicate and bind args for edge
// queries (used after the source-selection clause). Admin sees every live edge;
// non-admin sees targets satisfying the caller's visibilitySQL — the same
// predicate GetWithRelations applies, so /plugins/detail and the graph closure
// expose exactly the same set of children.
//
// Embedded (is_embedded=1) children get no relaxation here even though they are
// per-parent copies owned by a single container graph. Every writer that mints
// them — container import, container reupload, RebuildGraph, and the backfill —
// stamps each child with its container top's (visibility, space_id, owner_uid),
// so visibilitySQL on a child is already equivalent to visibilitySQL on the
// container we authorized as the root. Relaxing the check would therefore
// return no additional rows today while permanently dropping the per-row guard,
// leaving any future divergence between a container and its children to
// disclose them cross-Space with no code change here.
func graphEdgeWhere(scope Scope) (string, []any) {
	if scope.Admin {
		return "1=1", nil
	}
	return visibilitySQL, []any{scope.SpaceID, scope.CallerUID}
}

// scanRelationRow scans one relation-edge row from a result set selecting
// graphEdgeColumns (equivalently, the column list GetWithRelations uses).
func scanRelationRow(rows *sql.Rows) (model.PluginRelation, error) {
	var (
		x       model.PluginRelation
		data    []byte
		deleted sql.NullTime
	)
	if err := rows.Scan(&x.ID, &x.SourcePluginID, &x.TargetPluginID, &x.TargetPluginType, &x.Type, &x.SortOrder, &data, &x.Status, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt, &deleted); err != nil {
		return x, err
	}
	x.Data = cloneJSON(data)
	if deleted.Valid {
		x.DeletedAt = &deleted.Time
	}
	return x, nil
}

// scanRelationRows drains a relation-edge result set in row order.
func scanRelationRows(rows *sql.Rows) ([]model.PluginRelation, error) {
	defer rows.Close()
	var rels []model.PluginRelation
	for rows.Next() {
		x, err := scanRelationRow(rows)
		if err != nil {
			return nil, err
		}
		rels = append(rels, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rels, nil
}

// graphEdges accumulates graph edges and their deduplicated target IDs across
// both traversal levels while enforcing the node, edge, and JSON-document byte
// caps mid-scan.
//
// The node cap alone does not bound cost: a graph whose members share targets
// (the common case for system skills) keeps the unique-node count low while the
// edge count grows as members x targets-per-member. Capping edges as well, and
// checking both while draining rather than after, means an over-cap graph is
// never fully materialized.
type graphEdges struct {
	rels         []model.PluginRelation
	targetIDs    []string
	targetSeen   map[string]bool
	payloadBytes int64
}

func newGraphEdges(payloadBytes int64) *graphEdges {
	return &graphEdges{targetSeen: map[string]bool{}, payloadBytes: payloadBytes}
}

// drain consumes rows, appending each edge and recording first-seen targets.
// It returns ErrGraphTooLarge the moment any cap would be exceeded.
func (g *graphEdges) drain(rows *sql.Rows) error {
	defer rows.Close()
	for rows.Next() {
		if len(g.rels) >= maxGraphEdges {
			return ErrGraphTooLarge
		}
		x, err := scanRelationRow(rows)
		if err != nil {
			return err
		}
		payloadBytes, ok := addGraphPayloadBytes(g.payloadBytes, x.Data)
		if !ok {
			return ErrGraphTooLarge
		}
		g.payloadBytes = payloadBytes
		g.rels = append(g.rels, x)
		if !g.targetSeen[x.TargetPluginID] {
			if len(g.targetIDs) >= maxGraphNodes {
				return ErrGraphTooLarge
			}
			g.targetSeen[x.TargetPluginID] = true
			g.targetIDs = append(g.targetIDs, x.TargetPluginID)
		}
	}
	return rows.Err()
}

// graphNodeWhere returns the visibility predicate (excluding the mandatory
// p.plugin_id IN (…) list bound by the caller) and bind args for the batch
// node-payload query. Non-admin re-checks visibilitySQL per node as defense in
// depth against a target that became invisible between the edge query and this
// one. It deliberately matches graphEdgeWhere: see that function for why
// embedded children get no separate treatment.
func graphNodeWhere(scope Scope) (string, []any) {
	if scope.Admin {
		return "1=1", nil
	}
	return visibilitySQL, []any{scope.SpaceID, scope.CallerUID}
}

func stringSliceAsAny(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

func addGraphPayloadBytes(current int64, parts ...[]byte) (int64, bool) {
	for _, part := range parts {
		if int64(len(part)) > maxGraphPayloadBytes-current {
			return current, false
		}
		current += int64(len(part))
	}
	return current, true
}

// GetGraphClosure returns the transitive closure of the relation graph rooted
// at rootID, with a fixed maximum depth of two hops (expert_team -> expert ->
// skill/connector) enforced by the relation-type matrix.
//
// Visibility:
//   - The root must be visible under scope (this is the authorization anchor).
//   - Every descendant must independently satisfy the same caller-scoped
//     visibility predicate /plugins/detail applies, on both the edge queries and
//     the node-payload query. Descendants that fail it are omitted silently,
//     edge and node, so a read-only page degrades rather than failing closed the
//     way the install path does.
//
// The method performs at most four SQL round-trips regardless of fan-out (one
// for a leaf root, two when no child is visible), and fails closed with
// ErrGraphTooLarge when the deduplicated child count exceeds maxGraphNodes, the
// edge count exceeds maxGraphEdges, or JSON documents exceed
// maxGraphPayloadBytes. The caps are enforced while draining each result set,
// so an over-cap graph is never fully materialized. On success
// relations carries every visible edge in the closure (both levels, sorted by
// source then sort_order), and nodes carries the deduplicated set of visible
// child plugins in full projection (including package blobs), in first-seen
// order. This graph is the client installation contract; ordinary list queries
// continue to use pluginSummaryColumns.
//
// nodes is not guaranteed to be edge-reachable from the root: when an
// intermediate ancestor vanishes between the edge scan and the node query, its
// edges are dropped while its own children — which passed the caller's
// visibility check independently — remain. Consumers must treat nodes as a
// lookup table keyed by plugin_id, not as a tree derived from relations.
func (r *Repo) GetGraphClosure(ctx context.Context, scope Scope, rootID string) (*model.Plugin, []model.PluginRelation, []*model.Plugin, error) {
	root, err := r.getGraphRoot(ctx, scope, rootID)
	if err != nil {
		return nil, nil, nil, err
	}
	payloadBytes, ok := addGraphPayloadBytes(0, root.Manifest, root.Package)
	if !ok {
		return nil, nil, nil, ErrGraphTooLarge
	}

	// Leaves (skill/connector) have no valid outgoing edges per the relation
	// matrix; short-circuit without issuing an edge query.
	if root.Type == model.PluginTypeSkill || root.Type == model.PluginTypeConnector {
		return root, nil, nil, nil
	}

	// ---- Level 1: edges directly from root ---------------------------------
	edgeWhere, edgeWhereArgs := graphEdgeWhere(scope)
	l1Q := `SELECT ` + graphEdgeColumns + ` FROM plugin_relations r JOIN plugins p ON p.plugin_id=r.target_plugin_id
WHERE r.source_plugin_id=? AND r.status=1 AND r.deleted_at IS NULL AND p.status=1 AND p.deleted_at IS NULL AND ` + edgeWhere + `
ORDER BY r.sort_order,r.relation_id
LIMIT ` + graphEdgeLimit
	l1Args := append([]any{rootID}, edgeWhereArgs...)
	l1Rows, err := r.db.QueryContext(ctx, l1Q, l1Args...)
	if err != nil {
		return nil, nil, nil, wrapped("graph l1 edges", err)
	}
	edges := newGraphEdges(payloadBytes)
	if err := edges.drain(l1Rows); err != nil {
		if errors.Is(err, ErrGraphTooLarge) {
			return nil, nil, nil, err
		}
		return nil, nil, nil, wrapped("graph l1 scan", err)
	}
	// Snapshot the level-1 targets before the next hop appends to the shared
	// target list; only these are valid level-2 sources.
	l1Targets := append([]string(nil), edges.targetIDs...)

	// ---- Level 2: edges from level-1 sources (expert_team roots only) ------
	// The relation matrix only allows expert_team -> expert -> skill/connector to
	// nest two hops; expert roots already reached leaves in level 1.
	if root.Type == model.PluginTypeExpertTeam && len(l1Targets) > 0 {
		l2Q := `SELECT ` + graphEdgeColumns + ` FROM plugin_relations r JOIN plugins p ON p.plugin_id=r.target_plugin_id
WHERE r.source_plugin_id IN (` + placeholders(len(l1Targets)) + `) AND r.status=1 AND r.deleted_at IS NULL AND p.status=1 AND p.deleted_at IS NULL AND ` + edgeWhere + `
ORDER BY r.source_plugin_id,r.sort_order,r.relation_id
LIMIT ` + graphEdgeLimit
		l2Args := append(append([]any(nil), stringSliceAsAny(l1Targets)...), edgeWhereArgs...)
		l2Rows, err := r.db.QueryContext(ctx, l2Q, l2Args...)
		if err != nil {
			return nil, nil, nil, wrapped("graph l2 edges", err)
		}
		if err := edges.drain(l2Rows); err != nil {
			if errors.Is(err, ErrGraphTooLarge) {
				return nil, nil, nil, err
			}
			return nil, nil, nil, wrapped("graph l2 scan", err)
		}
	}

	allRels, targetIDs := edges.rels, edges.targetIDs
	payloadBytes = edges.payloadBytes
	if len(targetIDs) == 0 {
		return root, allRels, nil, nil
	}

	// ---- Batch node payloads (full projection) ----------------------------
	nodeWhere, nodeArgs := graphNodeWhere(scope)
	nodeQ := `SELECT ` + pluginColumns + pluginMetricColumns + graphOwnerReviewColumns + ` FROM plugins p
WHERE p.status=1 AND p.deleted_at IS NULL AND p.plugin_id IN (` + placeholders(len(targetIDs)) + `) AND ` + nodeWhere
	fullArgs := append(graphOwnerReviewArgs(scope), stringSliceAsAny(targetIDs)...)
	fullArgs = append(fullArgs, nodeArgs...)
	nRows, err := r.db.QueryContext(ctx, nodeQ, fullArgs...)
	if err != nil {
		return nil, nil, nil, wrapped("graph nodes", err)
	}
	defer nRows.Close()
	present := map[string]*model.Plugin{}
	for nRows.Next() {
		p, err := scanPluginRow(nRows, true, true, true)
		if err != nil {
			return nil, nil, nil, wrapped("graph node scan", err)
		}
		payloadBytes, ok = addGraphPayloadBytes(payloadBytes, p.Manifest, p.Package)
		if !ok {
			return nil, nil, nil, ErrGraphTooLarge
		}
		present[p.ID] = p
	}
	if err := nRows.Err(); err != nil {
		return nil, nil, nil, wrapped("graph node iter", err)
	}

	// Defense-in-depth: if a target was filtered out by the node WHERE
	// (concurrent delete or corrupted edge), drop edges that referenced it and
	// do not include it in the node slice.
	//
	// The target test also drops any edge pointing back at the root, since the
	// root never enters targetIDs and so never enters present. The relation
	// matrix makes such an edge unconstructible (no type admits a container as a
	// target), so this is a backstop against corrupt data, not a live case.
	if len(present) != len(targetIDs) {
		filtered := allRels[:0]
		for _, rel := range allRels {
			if rel.SourcePluginID != root.ID && present[rel.SourcePluginID] == nil {
				continue // edge from a vanished ancestor; drop
			}
			if present[rel.TargetPluginID] == nil {
				continue
			}
			filtered = append(filtered, rel)
		}
		allRels = filtered
	}
	nodes := make([]*model.Plugin, 0, len(present))
	for _, id := range targetIDs {
		if p, ok := present[id]; ok {
			nodes = append(nodes, p)
		}
	}
	return root, allRels, nodes, nil
}

// CountMemberRelations returns per-team counts of live expert_team_expert
// edges whose target Plugin is itself live. Members are embedded Plugins
// promoted with their team, so target liveness (not caller visibility) is the
// correct predicate: a team visible to the caller never leaks counts of
// resources outside its own graph.
func (r *Repo) CountMemberRelations(ctx context.Context, teamIDs []string) (map[string]int, error) {
	out := make(map[string]int, len(teamIDs))
	if len(teamIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(teamIDs))
	for _, id := range teamIDs {
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT r.source_plugin_id, COUNT(*) FROM plugin_relations r
JOIN plugins t ON t.plugin_id=r.target_plugin_id AND t.status=1 AND t.deleted_at IS NULL
WHERE r.source_plugin_id IN (`+placeholders(len(teamIDs))+`) AND r.relation_type='expert_team_expert' AND r.status=1 AND r.deleted_at IS NULL
GROUP BY r.source_plugin_id`, args...)
	if err != nil {
		return nil, wrapped("count member relations", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		out[id] = count
	}
	return out, rows.Err()
}

// CountDeclaredRelations counts a plugin's live one-level relations to live
// targets WITHOUT the caller-visibility predicate — the full declared topology
// its publisher committed. The install path compares this against the
// visibility-filtered set (GetWithRelations) to detect that a dependency was
// hidden from the caller and refuse rather than silently provision a partial
// expert/squad (P1-1). It returns only a count, so it reveals nothing beyond the
// number of edges the plugin already declares; the target-liveness predicate
// matches GetWithRelations so the comparison isolates visibility drops from
// liveness drops.
func (r *Repo) CountDeclaredRelations(ctx context.Context, pluginID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_relations r
JOIN plugins p ON p.plugin_id=r.target_plugin_id AND p.status=1 AND p.deleted_at IS NULL
WHERE r.source_plugin_id=? AND r.status=1 AND r.deleted_at IS NULL`, pluginID).Scan(&n)
	if err != nil {
		return 0, wrapped("count declared relations", err)
	}
	return n, nil
}

func scanPlugin(s interface{ Scan(...any) error }) (*model.Plugin, error) {
	return scanPluginRow(s, true, false, false)
}

// scanPluginWithMetrics scans a row selected with pluginColumns plus the
// correlated metric counters appended by pluginMetricColumns.
func scanPluginWithMetrics(s interface{ Scan(...any) error }) (*model.Plugin, error) {
	return scanPluginRow(s, true, true, false)
}

func scanPluginRow(s interface{ Scan(...any) error }, includePackage, includeMetrics, includeReviewState bool) (*model.Plugin, error) {
	var p model.Plugin
	var category, space, botUID, botName, version, versionName sql.NullString
	var rating sql.NullInt64
	var reviewID, reviewStatus sql.NullString
	var tags, manifest, pkg, attachKeys []byte
	var deleted sql.NullTime
	dest := []any{&p.ID, &p.Name, &p.Type, &p.IsEmbedded, &category, &tags, &p.Publisher, &p.OwnerUID, &space, &p.Visibility, &p.ListingState, &p.CreatorName, &p.CreatedByType, &botUID, &botName, &p.Icon, &p.ToolCount, &rating, &manifest}
	if includePackage {
		dest = append(dest, &pkg, &attachKeys)
	}
	dest = append(dest, &p.ManifestHash, &p.PluginHash, &version, &versionName, &p.Status, &p.CreatedAt, &p.UpdatedAt, &deleted)
	if includeMetrics {
		dest = append(dest, &p.ViewCount, &p.InstallCount, &p.DownloadCount)
	}
	if includeReviewState {
		dest = append(dest, &p.HasPendingReview, &reviewID, &reviewStatus)
	}
	if err := s.Scan(dest...); err != nil {
		return nil, err
	}
	if reviewID.Valid {
		p.LatestReviewID = reviewID.String
	}
	if reviewStatus.Valid {
		p.LatestReviewStatus = model.ReviewStatus(reviewStatus.String)
	}
	p.CategoryID = nullString(category)
	p.SpaceID = nullString(space)
	p.CreatedByBotUID = nullString(botUID)
	p.CreatedByBotName = nullString(botName)
	p.CurrentVersionID = nullString(version)
	p.CurrentVersion = nullString(versionName)
	if rating.Valid {
		value := int(rating.Int64)
		p.Rating = &value
	}
	if deleted.Valid {
		p.DeletedAt = &deleted.Time
	}
	p.Tags = cloneJSON(tags)
	p.Manifest = cloneJSON(manifest)
	if includePackage {
		p.Package = cloneJSON(pkg)
		p.AttachmentKeys = cloneJSON(attachKeys)
	}
	return &p, nil
}
func cloneJSON(v []byte) []byte {
	if v == nil {
		return nil
	}
	return append([]byte(nil), v...)
}
func nullString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	x := v.String
	return &x
}
func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }
func wrapped(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("plugin repository %s: %w", op, err)
}
