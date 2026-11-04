package plugin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

type visibilityReleaseEdges struct {
	ID        string
	Label     string
	Name      string
	Relations []model.PluginRelation
}
type visibilityNode struct {
	Plugin      model.Plugin
	PublishedID string
	Releases    []visibilityReleaseEdges
	Pending     bool
}
type visibilityReader func(context.Context, string) (*visibilityNode, error)
type visibilityQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func visibilityBaseline(n *visibilityNode) string {
	// Include all releases as well as current connector config. A new formal
	// historical dependency changes the plan even when the displayed head did not.
	raw, _ := json.Marshal(struct {
		Scope           model.PluginVisibility
		State           model.PluginListingState
		Published, Hash string
		Releases        []visibilityReleaseEdges
		Pending         bool
	}{n.Plugin.Visibility, n.Plugin.ListingState, n.PublishedID, n.Plugin.PluginHash, n.Releases, n.Pending})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func visibilityItem(n *visibilityNode, to model.PluginVisibility) model.VisibilityPlanItem {
	version := n.PublishedID
	if n.Plugin.Type == model.PluginTypeConnector {
		version = ""
	}
	return model.VisibilityPlanItem{PluginID: n.Plugin.ID, Name: visibilityNodeName(n), Type: n.Plugin.Type, From: n.Plugin.Visibility, To: to, PublishedVersionID: version, Baseline: visibilityBaseline(n)}
}
func buildVisibilityPlan(ctx context.Context, sc Scope, root *visibilityNode, target model.PluginVisibility, read visibilityReader, strict bool) (*model.VisibilityPlan, error) {
	plan := &model.VisibilityPlan{PluginID: root.Plugin.ID, Changes: []model.VisibilityPlanItem{}, Affected: []model.VisibilityPlanItem{}, Blockers: []model.VisibilityPlanBlocker{}}
	visited := map[string]bool{}
	evidence := map[string]string{}
	var visit func(*visibilityNode) error
	visit = func(n *visibilityNode) error {
		if visited[n.Plugin.ID] {
			return nil
		}
		visited[n.Plugin.ID] = true
		if len(visited) > maxGraphNodes {
			return ErrGraphTooLarge
		}
		evidence[n.Plugin.ID] = visibilityBaseline(n)
		own := n.Plugin.OwnerUID == sc.CallerUID && n.Plugin.SpaceID != nil && *n.Plugin.SpaceID == sc.SpaceID
		accessible := own || n.Plugin.Visibility == model.PluginVisibilitySystem || n.Plugin.Visibility == model.PluginVisibilityPublic || (n.Plugin.SpaceID != nil && *n.Plugin.SpaceID == sc.SpaceID && n.Plugin.Visibility == model.PluginVisibilitySpace)
		block := func(reason string) {
			name := ""
			if accessible {
				name = n.Plugin.Name
			}
			plan.Blockers = append(plan.Blockers, model.VisibilityPlanBlocker{PluginID: n.Plugin.ID, Name: name, Reason: reason})
		}
		if !accessible {
			block("dependency_not_accessible")
			return nil
		}
		if n.Plugin.ListingState != model.PluginListingStatePublished && !(n.Plugin.ID == root.Plugin.ID && n.Plugin.ListingState == model.PluginListingStateDelisted) {
			block("dependency_unavailable")
			return nil
		}
		if n.Plugin.Visibility == model.PluginVisibilityPrivate {
			if !own || (n.Plugin.IsEmbedded && strict) {
				block("dependency_not_owned")
				return nil
			}
			if n.Pending {
				block("review_pending")
				return nil
			}
			if !n.Plugin.IsEmbedded && n.Plugin.Type != model.PluginTypeConnector && n.PublishedID == "" {
				block("version_baseline_unavailable")
				return nil
			}
			if !n.Plugin.IsEmbedded {
				plan.Changes = append(plan.Changes, visibilityItem(n, target))
			}
		} else if n.Plugin.ID == root.Plugin.ID {
			plan.Changes = append(plan.Changes, visibilityItem(n, target))
		}
		if target != model.PluginVisibilitySpace {
			return nil
		}
		for _, release := range n.Releases {
			for _, edge := range release.Relations {
				if edge.Status != 1 || edge.DeletedAt != nil {
					continue
				}
				if edge.Type != "expert_skill" && edge.Type != "expert_connector" && edge.Type != "expert_team_expert" {
					continue
				}
				child, err := read(ctx, edge.TargetPluginID)
				if errors.Is(err, ErrNotFound) {
					plan.Blockers = append(plan.Blockers, model.VisibilityPlanBlocker{PluginID: edge.TargetPluginID, Reason: "dependency_unavailable"})
					continue
				}
				if err != nil {
					return err
				}
				// Validate the exact frozen pin; never silently follow the live draft.
				if child.Plugin.Type != model.PluginTypeConnector {
					found := false
					for _, v := range child.Releases {
						if edge.TargetVersionID != nil && v.ID == *edge.TargetVersionID {
							found = true
							break
						}
					}
					if !found && (strict || edge.TargetVersionID != nil || (!child.Plugin.IsEmbedded && child.PublishedID == "")) {
						plan.Blockers = append(plan.Blockers, model.VisibilityPlanBlocker{PluginID: edge.TargetPluginID, Reason: "released_version_required"})
						continue
					}
				}
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	sort.Slice(plan.Changes, func(i, j int) bool { return plan.Changes[i].PluginID < plan.Changes[j].PluginID })
	sort.Slice(plan.Blockers, func(i, j int) bool {
		return plan.Blockers[i].PluginID+plan.Blockers[i].Reason < plan.Blockers[j].PluginID+plan.Blockers[j].Reason
	})
	raw, _ := json.Marshal(struct {
		Plan     *model.VisibilityPlan
		Evidence map[string]string
	}{plan, evidence})
	sum := sha256.Sum256(raw)
	plan.Revision = hex.EncodeToString(sum[:])
	return plan, nil
}

func readVisibilityNode(ctx context.Context, q visibilityQuery, id string) (*visibilityNode, error) {
	p, err := scanPlugin(q.QueryRowContext(ctx, `SELECT `+pluginColumns+` FROM plugins p WHERE p.plugin_id=? AND p.status=1 AND p.deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	n := &visibilityNode{Plugin: *p}
	var published sql.NullString
	if err = q.QueryRowContext(ctx, `SELECT published_version_id,EXISTS(SELECT 1 FROM plugin_review_requests rr WHERE rr.plugin_id=plugins.plugin_id AND rr.status='pending' AND rr.deleted_at IS NULL) FROM plugins WHERE plugin_id=?`, id).Scan(&published, &n.Pending); err != nil {
		return nil, err
	}
	n.PublishedID = published.String
	rows, err := q.QueryContext(ctx, `SELECT v.version_id,v.relations_json,rel.version_label,COALESCE(JSON_UNQUOTE(JSON_EXTRACT(v.manifest_json,'$.plugin_name')),'') FROM plugin_versions v JOIN plugin_version_releases rel ON rel.plugin_id=v.plugin_id AND rel.version_id=v.version_id WHERE v.plugin_id=? ORDER BY v.version_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v visibilityReleaseEdges
		var raw []byte
		if err = rows.Scan(&v.ID, &raw, &v.Label, &v.Name); err != nil {
			return nil, err
		}
		v.Relations, err = model.DecodeSnapshotRelations(raw)
		if err != nil {
			return nil, err
		}
		n.Releases = append(n.Releases, v)
		if len(n.Releases) > maxGraphEdges {
			return nil, ErrGraphTooLarge
		}
	}
	return n, rows.Err()
}

func (r *Repo) VisibilityPlan(ctx context.Context, sc Scope, id string, target model.PluginVisibility) (*model.VisibilityPlan, error) {
	if !r.versionManagement {
		return nil, ErrVersionBaselineUnavailable
	}
	if target != model.PluginVisibilityPrivate && target != model.PluginVisibilitySpace {
		return nil, ErrConflict
	}
	root, err := readVisibilityNode(ctx, r.db, id)
	if err != nil {
		return nil, err
	}
	if root.Plugin.OwnerUID != sc.CallerUID || root.Plugin.SpaceID == nil || *root.Plugin.SpaceID != sc.SpaceID || root.Plugin.IsEmbedded || root.Plugin.Visibility == model.PluginVisibilitySystem || root.Plugin.Visibility == model.PluginVisibilityPublic {
		return nil, ErrNotFound
	}
	return visibilityPlanWithImpact(ctx, sc, root, target, r.db, r.references)
}
func visibilityPlanWithImpact(ctx context.Context, sc Scope, root *visibilityNode, target model.PluginVisibility, q visibilityQuery, strict bool) (*model.VisibilityPlan, error) {
	cache := map[string]*visibilityNode{root.Plugin.ID: root}
	read := func(ctx context.Context, id string) (*visibilityNode, error) {
		if n := cache[id]; n != nil {
			return n, nil
		}
		n, e := readPlanNode(ctx, q, id, strict)
		if e == nil {
			cache[id] = n
		}
		return n, e
	}
	plan, err := buildVisibilityPlan(ctx, sc, root, target, read, strict)
	if err != nil || target != model.PluginVisibilityPrivate {
		return plan, err
	}
	// Reverse visibility impact is scoped to this tenant. Names are the minimal
	// confirmation metadata; package/config content is never returned.
	rows, err := q.QueryContext(ctx, `SELECT plugin_id FROM plugins WHERE space_id=? AND status=1 AND deleted_at IS NULL AND listing_state='published' AND plugin_type IN ('expert','expert_team') ORDER BY plugin_id`, sc.SpaceID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	affected := map[string]bool{}
	legacy := map[string][]model.PluginRelation{}
	var contains func(string, *string, map[string]bool, bool) (bool, error)
	contains = func(id string, pin *string, seen map[string]bool, rootAccessible bool) (bool, error) {
		if id == root.Plugin.ID {
			return !rootAccessible, nil
		}
		// A member's listing state gates every pinned version. Once the
		// resource pauses, all its consumers must persist their own pause too.
		if affected[id] {
			return true, nil
		}
		key := id
		if pin != nil {
			key += ":" + *pin
		}
		if seen[key] {
			return false, nil
		}
		seen[key] = true
		if len(seen) > maxGraphNodes {
			return false, ErrGraphTooLarge
		}
		n, e := read(ctx, id)
		if errors.Is(e, ErrNotFound) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		releases := n.Releases
		// Catalog availability uses live relations for pre-baseline published roots.
		// Match that compatibility path without substituting live edges for a pin.
		if pin == nil && n.PublishedID == "" && n.Plugin.ListingState == model.PluginListingStatePublished {
			rels, loaded := legacy[id]
			if !loaded {
				rels, e = compatibilityLiveRelations(ctx, q, id)
				if e != nil {
					return false, e
				}
				for i := range rels {
					rels[i].Status = 1
				}
				legacy[id] = rels
			}
			releases = append(releases, visibilityReleaseEdges{Relations: rels})
		}
		for _, v := range releases {
			if pin != nil && v.ID != *pin {
				continue
			}
			for _, edge := range v.Relations {
				if edge.Status == 1 && edge.DeletedAt == nil && (edge.Type == "expert_skill" || edge.Type == "expert_connector" || edge.Type == "expert_team_expert") {
					found, e := contains(edge.TargetPluginID, edge.TargetVersionID, seen, rootAccessible)
					if e != nil || found {
						return found, e
					}
				}
			}
		}
		return false, nil
	}
	for {
		grew := false
		for _, id := range ids {
			if id == root.Plugin.ID || affected[id] {
				continue
			}
			n, e := read(ctx, id)
			if e != nil {
				return nil, e
			}
			rootAccessible := n.Plugin.Visibility == model.PluginVisibilityPrivate && n.Plugin.OwnerUID == root.Plugin.OwnerUID
			found, e := contains(id, nil, map[string]bool{}, rootAccessible)
			if e != nil {
				return nil, e
			}
			if !found {
				continue
			}
			affected[id] = true
			if len(affected) > maxGraphNodes {
				return nil, ErrGraphTooLarge
			}
			grew = true
			item := visibilityItem(n, n.Plugin.Visibility)
			if n.Plugin.Visibility == model.PluginVisibilityPrivate && n.Plugin.OwnerUID != sc.CallerUID {
				item.Name = ""
			}
			plan.Affected = append(plan.Affected, item)
		}
		if !grew {
			break
		}
	}
	raw, _ := json.Marshal(plan)
	sum := sha256.Sum256(raw)
	plan.Revision = hex.EncodeToString(sum[:])
	return plan, nil
}

func sameVisibilityPlan(a, b *model.VisibilityPlan) bool {
	return a != nil && b != nil && strings.EqualFold(a.Revision, b.Revision)
}

func sortStringsUnique(ids *[]string) {
	sort.Strings(*ids)
	out := (*ids)[:0]
	for _, id := range *ids {
		if len(out) == 0 || out[len(out)-1] != id {
			out = append(out, id)
		}
	}
	*ids = out
}

func visibilityNodeName(n *visibilityNode) string {
	for _, v := range n.Releases {
		if v.ID == n.PublishedID && v.Name != "" {
			return v.Name
		}
	}
	return n.Plugin.Name
}

func readPlanNode(ctx context.Context, q visibilityQuery, id string, strict bool) (*visibilityNode, error) {
	n, err := readVisibilityNode(ctx, q, id)
	if err != nil || strict || !n.Plugin.IsEmbedded || n.PublishedID != "" {
		return n, err
	}
	edges, err := compatibilityLiveRelations(ctx, q, id)
	if err != nil {
		return nil, err
	}
	for i := range edges {
		edges[i].Status = 1
	}
	n.Releases = append(n.Releases, visibilityReleaseEdges{Relations: edges})
	return n, nil
}
