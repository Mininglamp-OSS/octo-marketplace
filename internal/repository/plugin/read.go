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
	rows, err := r.db.QueryContext(ctx, `SELECT r.relation_id,r.source_plugin_id,r.target_plugin_id,p.plugin_type,r.relation_type,r.sort_order,
 r.relation_json,r.status,r.created_by,r.created_at,r.updated_at,r.deleted_at
FROM plugin_relations r JOIN plugins p ON p.plugin_id=r.target_plugin_id
WHERE r.source_plugin_id=? AND r.status=1 AND r.deleted_at IS NULL AND p.status=1 AND p.deleted_at IS NULL AND `+relWhere+`
ORDER BY r.sort_order,r.relation_id`, relArgs...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var rels []model.PluginRelation
	for rows.Next() {
		var x model.PluginRelation
		var data []byte
		var deleted sql.NullTime
		if err := rows.Scan(&x.ID, &x.SourcePluginID, &x.TargetPluginID, &x.TargetPluginType, &x.Type, &x.SortOrder, &data, &x.Status, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt, &deleted); err != nil {
			return nil, nil, err
		}
		x.Data = cloneJSON(data)
		if deleted.Valid {
			x.DeletedAt = &deleted.Time
		}
		rels = append(rels, x)
	}
	return p, rels, rows.Err()
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

// scanPluginSummary scans a row selected with pluginSummaryColumns plus metric
// counters; the package stays nil so list pages never materialize it.
func scanPluginSummary(s interface{ Scan(...any) error }) (*model.Plugin, error) {
	return scanPluginRow(s, false, true, false)
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
