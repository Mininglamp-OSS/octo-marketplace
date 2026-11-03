package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

type DependencyChoice struct {
	TargetID string `json:"target_plugin_id" binding:"required"`
	Revision string `json:"target_revision" binding:"required"`
}
type DependencySelection struct {
	DraftRevision string             `json:"draft_revision" binding:"required"`
	PublishedID   string             `json:"published_version_id" binding:"required"`
	Choices       []DependencyChoice `json:"choices" binding:"required,min=1,max=100,dive"`
}
type DependencyUpdate struct {
	DependencyChoice
	Name         string           `json:"name"`
	Type         model.PluginType `json:"plugin_type"`
	Current      string           `json:"current_revision"`
	CurrentLabel string           `json:"current_version,omitempty"`
	TargetLabel  string           `json:"target_version,omitempty"`
	Dismissed    bool             `json:"is_dismissed"`
}
type DependencyPreview struct {
	DraftRevision string             `json:"draft_revision"`
	PublishedID   string             `json:"published_version_id"`
	IncludesDraft bool               `json:"includes_draft_changes"`
	Updates       []DependencyUpdate `json:"updates"`
}

const connectorFingerprint = "SHA2(CONCAT(t.manifest_hash,':',t.plugin_hash),256)"

func loadUpdatePreview(ctx context.Context, tx *sql.Tx, sc Scope, p *model.Plugin) (*DependencyPreview, error) {
	if p.ListingState != model.PluginListingStatePublished {
		return nil, ErrConflict
	}
	if p.IsEmbedded || (p.Type != model.PluginTypeExpert && p.Type != model.PluginTypeExpertTeam) {
		return nil, ErrNotFound
	}
	var published sql.NullString
	var includesDraft bool
	if err := tx.QueryRowContext(ctx, "SELECT p.published_version_id,COALESCE(d.manifest_hash<>v.manifest_hash OR d.plugin_hash<>v.plugin_hash OR NOT(d.relations_json<=>v.relations_json) OR NOT(d.attachment_keys_json<=>v.attachment_keys_json),FALSE) FROM plugins p LEFT JOIN plugin_versions d ON d.plugin_id=p.plugin_id AND d.version_id=p.current_version_id LEFT JOIN plugin_versions v ON v.plugin_id=p.plugin_id AND v.version_id=p.published_version_id WHERE p.plugin_id=? AND p.space_id=?", p.ID, sc.SpaceID).Scan(&published, &includesDraft); err != nil {
		return nil, err
	}
	out := &DependencyPreview{DraftRevision: model.PluginDraftRevision(p), PublishedID: published.String, Updates: []DependencyUpdate{}}
	if !published.Valid {
		return out, nil
	}
	out.IncludesDraft = includesDraft
	items, err := queryDependencyUpdates(ctx, tx, sc, []string{p.ID})
	if err == nil && len(items[p.ID]) > 0 {
		out.Updates = items[p.ID]
	}
	return out, err
}

type reminderQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func queryDependencyUpdates(ctx context.Context, db reminderQuery, sc Scope, ids []string) (map[string][]DependencyUpdate, error) {
	out := map[string][]DependencyUpdate{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{sc.SpaceID, sc.CallerUID, sc.SpaceID, sc.CallerUID}
	for _, id := range ids {
		args = append(args, id)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	id, pin, kind := snapshotField("e", "target_plugin_id"), snapshotField("e", "target_version_id"), snapshotField("e", "relation_type")
	candidate := "IF(t.plugin_type='connector'," + connectorFingerprint + ",t.published_version_id)"
	rows, err := db.QueryContext(ctx, `SELECT src.plugin_id,t.plugin_id,COALESCE(JSON_UNQUOTE(JSON_EXTRACT(tv.manifest_json,'$.plugin_name')),t.plugin_name),t.plugin_type,
 IF(t.plugin_type='connector',b.revision,old.version_id),`+candidate+`,COALESCE(old.version_label,''),COALESCE(newrel.version_label,''),d.target_id IS NOT NULL
 FROM plugins src JOIN plugin_versions v ON v.plugin_id=src.plugin_id AND v.version_id=src.published_version_id JOIN JSON_TABLE(v.relations_json,'$[*]' COLUMNS (`+snapshotRelationColumns+`)) e
 JOIN plugins t ON t.plugin_id=`+id+`
 LEFT JOIN plugin_version_releases old ON old.plugin_id=t.plugin_id AND old.version_id=`+pin+`
 LEFT JOIN plugin_version_releases newrel ON newrel.plugin_id=t.plugin_id AND newrel.version_id=t.published_version_id
 LEFT JOIN plugin_versions tv ON tv.plugin_id=t.plugin_id AND tv.version_id=newrel.version_id
 LEFT JOIN plugin_connector_baselines b ON b.source_id=v.plugin_id AND b.target_id=t.plugin_id
 LEFT JOIN plugin_update_dismissals d ON d.space_id=? AND d.owner_uid=? AND d.source_id=v.plugin_id AND d.target_id=t.plugin_id AND d.revision=`+candidate+`
 WHERE src.space_id=? AND src.owner_uid=? AND src.plugin_id IN (`+placeholders+`) AND src.listing_state='published' AND src.is_embedded=0 AND src.plugin_type IN ('expert','expert_team') AND COALESCE(e.status,e.status_legacy,1)=1 AND COALESCE(e.deleted_at,e.deleted_at_legacy) IS NULL
 AND t.status=1 AND t.deleted_at IS NULL AND t.listing_state='published'
 AND (t.visibility IN ('system','public') OR (t.space_id=src.space_id AND (t.visibility='space' OR (t.visibility='private' AND t.owner_uid=src.owner_uid AND src.visibility='private'))))
 AND (t.plugin_type<>'expert' OR NOT EXISTS(SELECT 1 FROM plugins p WHERE p.plugin_id=t.plugin_id AND `+unavailableDependencySQL(availabilityRoot)+`))
 AND ((`+kind+`='expert_connector' AND t.plugin_type='connector' AND b.revision<>`+connectorFingerprint+`)
 OR (`+kind+` IN ('expert_skill','expert_team_expert') AND old.version_id<>newrel.version_id)) ORDER BY t.plugin_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var source string
		var u DependencyUpdate
		if err = rows.Scan(&source, &u.TargetID, &u.Name, &u.Type, &u.Current, &u.Revision, &u.CurrentLabel, &u.TargetLabel, &u.Dismissed); err != nil {
			return nil, err
		}
		out[source] = append(out[source], u)
	}
	return out, rows.Err()
}
func (r *Repo) DependencyUpdates(ctx context.Context, sc Scope, id string, skip []DependencyChoice) (*DependencyPreview, error) {
	if !r.references || !r.versionManagement {
		return nil, ErrNotFound
	}
	if skip != nil && !ValidDismissChoices(skip) {
		return nil, ErrInvalidRelation
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, err := getOwnedForUpdate(ctx, tx, sc, id)
	if err != nil {
		return nil, err
	}
	plan, err := loadUpdatePreview(ctx, tx, sc, p)
	if err != nil {
		return nil, err
	}
	available := map[string]*DependencyUpdate{}
	for i := range plan.Updates {
		u := &plan.Updates[i]
		available[u.TargetID] = u
	}
	for _, c := range skip {
		u := available[c.TargetID]
		if u == nil || u.Revision != c.Revision {
			return nil, ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO plugin_update_dismissals(space_id,owner_uid,source_id,target_id,revision) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE revision=VALUES(revision)`, sc.SpaceID, sc.CallerUID, id, c.TargetID, c.Revision); err != nil {
			return nil, err
		}
		u.Dismissed = true
	}
	return plan, tx.Commit()
}

// Apply only selected direct edges in the already locked source transaction.
func (r *Repo) selectUpdates(ctx context.Context, tx *sql.Tx, sc Scope, p *model.Plugin, s *DependencySelection, rels []model.PluginRelation) (map[string]string, error) {
	if !r.references || !r.versionManagement || p.ListingState != model.PluginListingStatePublished || len(s.Choices) == 0 || len(s.Choices) > 100 {
		return nil, ErrConflict
	}
	for _, c := range s.Choices {
		var id string
		if err := tx.QueryRowContext(ctx, `SELECT plugin_id FROM plugins t WHERE t.plugin_id=? AND t.status=1 AND t.deleted_at IS NULL AND t.listing_state='published' AND (t.visibility IN ('system','public') OR (t.space_id=? AND (t.visibility='space' OR (t.visibility='private' AND t.owner_uid=? AND ?='private')))) AND IF(t.plugin_type='connector',`+connectorFingerprint+`,t.published_version_id)=? FOR UPDATE`, c.TargetID, sc.SpaceID, sc.CallerUID, p.Visibility, c.Revision).Scan(&id); err == sql.ErrNoRows {
			return nil, ErrConflict
		} else if err != nil {
			return nil, err
		}
	}
	plan, err := loadUpdatePreview(ctx, tx, sc, p)
	if err != nil {
		return nil, err
	}
	if plan.DraftRevision != s.DraftRevision || plan.PublishedID != s.PublishedID {
		return nil, ErrConflict
	}
	candidates := map[string]DependencyUpdate{}
	for _, u := range plan.Updates {
		candidates[u.TargetID] = u
	}
	connectors := map[string]string{}
	for _, c := range s.Choices {
		u, ok := candidates[c.TargetID]
		if !ok || c.Revision != u.Revision {
			return nil, ErrConflict
		}
		delete(candidates, c.TargetID)
		found := false
		for i := range rels {
			edge := &rels[i]
			if edge.TargetPluginID != c.TargetID {
				continue
			}
			found = true
			if u.Type == model.PluginTypeConnector {
				connectors[c.TargetID] = c.Revision
			} else {
				pin := c.Revision
				edge.TargetVersionID = &pin
			}
		}
		if !found {
			return nil, ErrConflict
		}
	}
	return connectors, nil
}

func refreshConnectorBaselines(ctx context.Context, tx *sql.Tx, source, version string) error {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT relations_json FROM plugin_versions WHERE plugin_id=? AND version_id=?`, source, version).Scan(&raw); err != nil {
		return err
	}
	edges, err := model.DecodeSnapshotRelations(raw)
	if err != nil {
		return err
	}
	ids := []any{source}
	for _, edge := range edges {
		if edge.Type != "expert_connector" || edge.DeletedAt != nil || (edge.Status != 0 && edge.Status != 1) {
			continue
		}
		ids = append(ids, edge.TargetPluginID)
		if _, err = tx.ExecContext(ctx, `INSERT INTO plugin_connector_baselines(source_id,target_id,revision) SELECT ?,t.plugin_id,`+connectorFingerprint+` FROM plugins t WHERE t.plugin_id=? ON DUPLICATE KEY UPDATE source_id=VALUES(source_id)`, source, edge.TargetPluginID); err != nil {
			return err
		}
	}
	q := "DELETE FROM plugin_connector_baselines WHERE source_id=?"
	if len(ids) > 1 {
		q += " AND target_id NOT IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)-1), ",") + ")"
	}
	_, err = tx.ExecContext(ctx, q, ids...)
	return err
}

func ackConnectors(ctx context.Context, tx *sql.Tx, id string, revisions map[string]string) error {
	for target, rev := range revisions {
		if _, err := tx.ExecContext(ctx, "UPDATE plugin_connector_baselines SET revision=? WHERE source_id=? AND target_id=?", rev, id, target); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) FillUpdateIndicators(ctx context.Context, sc Scope, items []model.Plugin) error {
	ids := []string{}
	for _, p := range items {
		if p.Type == model.PluginTypeExpert || p.Type == model.PluginTypeExpertTeam {
			ids = append(ids, p.ID)
		}
	}
	candidates, err := queryDependencyUpdates(ctx, r.db, sc, ids)
	if err != nil {
		return err
	}
	for i := range items {
		for _, u := range candidates[items[i].ID] {
			if !u.Dismissed {
				items[i].HasDependencyUpdates = true
				break
			}
		}
	}
	return nil
}

func applySelectedLivePins(ctx context.Context, tx *sql.Tx, source string, selection *DependencySelection, edges []model.PluginRelation) error {
	selected := map[string]bool{}
	for _, c := range selection.Choices {
		selected[c.TargetID] = true
	}
	for _, edge := range edges {
		if !selected[edge.TargetPluginID] || edge.TargetVersionID == nil {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE plugin_relations SET target_version_id=? WHERE source_plugin_id=? AND target_plugin_id=? AND relation_type=? AND status=1 AND deleted_at IS NULL`, *edge.TargetVersionID, source, edge.TargetPluginID, edge.Type); err != nil {
			return err
		}
	}
	return nil
}

// Managed review has already locked and validated the frozen references. A newer
// Skill or member release does not invalidate an available frozen pin. Connectors
// are current-state resources, so they must still match the submitted revision.
func (r *Repo) approveDependencySelection(ctx context.Context, tx *sql.Tx, p *model.Plugin, reviewID string, edges []model.PluginRelation) (bool, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT selection_json FROM plugin_review_dependency_updates WHERE review_id=?`, reviewID).Scan(&raw)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !r.references {
		return false, ErrConflict
	}
	var selection DependencySelection
	if err = json.Unmarshal(raw, &selection); err != nil {
		return false, err
	}
	var published sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT published_version_id FROM plugins WHERE plugin_id=? AND space_id=?`, p.ID, p.SpaceID).Scan(&published); err != nil {
		return false, err
	}
	if published.String != selection.PublishedID || p.ListingState != model.PluginListingStatePublished {
		return false, ErrConflict
	}
	choices := map[string]string{}
	for _, c := range selection.Choices {
		choices[c.TargetID] = c.Revision
	}
	acknowledged := map[string]string{}
	for _, edge := range edges {
		wanted, ok := choices[edge.TargetPluginID]
		if !ok {
			continue
		}
		delete(choices, edge.TargetPluginID)
		if edge.Type != "expert_connector" {
			if edge.TargetVersionID == nil || *edge.TargetVersionID != wanted {
				return false, ErrConflict
			}
			continue
		}
		var current string
		if err = tx.QueryRowContext(ctx, `SELECT `+connectorFingerprint+` FROM plugins t WHERE t.plugin_id=? FOR UPDATE`, edge.TargetPluginID).Scan(&current); err != nil {
			return false, err
		}
		if wanted != current {
			return false, ErrConflict
		}
		acknowledged[edge.TargetPluginID] = current
	}
	if len(choices) > 0 {
		return false, ErrConflict
	}
	if err = ackConnectors(ctx, tx, p.ID, acknowledged); err != nil {
		return false, err
	}
	if model.PluginDraftRevision(p) != selection.DraftRevision {
		return false, nil
	}
	return true, applySelectedLivePins(ctx, tx, p.ID, &selection, edges)
}

// ValidDismissChoices bounds work before any source row is locked.
func ValidDismissChoices(choices []DependencyChoice) bool {
	if len(choices) == 0 || len(choices) > 100 {
		return false
	}
	seen := make(map[string]bool, len(choices))
	for _, c := range choices {
		if strings.TrimSpace(c.TargetID) == "" || strings.TrimSpace(c.Revision) == "" || seen[c.TargetID] {
			return false
		}
		seen[c.TargetID] = true
	}
	return true
}
