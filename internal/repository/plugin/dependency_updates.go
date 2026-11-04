package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// DependencyUpdateSummaries enriches an entire owner-list page with one query.
// Only confirmed formal direct references and readable available targets join.
func (r *Repo) DependencyUpdateSummaries(ctx context.Context, sc Scope, items []model.Plugin) (map[string][]model.DependencyUpdate, error) {
	ids := []string{}
	for _, p := range items {
		if p.Type == model.PluginTypeExpert || p.Type == model.PluginTypeExpertTeam {
			ids = append(ids, p.ID)
		}
	}
	if !r.versionManagement {
		return map[string][]model.DependencyUpdate{}, nil
	}
	return queryDependencyUpdates(ctx, r.db, sc, ids)
}

type reminderQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func queryDependencyUpdates(ctx context.Context, db reminderQuery, sc Scope, ids []string) (map[string][]model.DependencyUpdate, error) {
	out := map[string][]model.DependencyUpdate{}
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
 IF(t.plugin_type='connector',b.revision,old.version_id),`+candidate+`,COALESCE(old.version_label,''),COALESCE(newrel.version_label,''),d.dependency_plugin_id IS NOT NULL
 FROM plugins src JOIN plugin_versions v ON v.plugin_id=src.plugin_id AND v.version_id=src.published_version_id JOIN JSON_TABLE(v.relations_json,'$[*]' COLUMNS (`+snapshotRelationColumns+`)) e
 JOIN plugins t ON t.plugin_id=`+id+`
 LEFT JOIN plugin_version_releases old ON old.plugin_id=t.plugin_id AND old.version_id=`+pin+`
 LEFT JOIN plugin_version_releases newrel ON newrel.plugin_id=t.plugin_id AND newrel.version_id=t.published_version_id
 LEFT JOIN plugin_versions tv ON tv.plugin_id=t.plugin_id AND tv.version_id=newrel.version_id
 LEFT JOIN plugin_connector_baselines b ON b.source_id=v.plugin_id AND b.target_id=t.plugin_id
 LEFT JOIN plugin_dependency_reminder_skips d ON d.space_id=? AND d.uid=? AND d.plugin_id=v.plugin_id AND d.dependency_plugin_id=t.plugin_id AND d.target_version_id=`+candidate+`
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
		var u model.DependencyUpdate
		if err = rows.Scan(&source, &u.PluginID, &u.Name, &u.Type, &u.FromVersionID, &u.TargetVersionID, &u.FromVersion, &u.TargetVersion, &u.IsSkipped); err != nil {
			return nil, err
		}
		if u.Type == model.PluginTypeConnector {
			u.FromConfigRevision, u.TargetConfigRevision = u.FromVersionID, u.TargetVersionID
			u.FromVersionID, u.TargetVersionID = "", ""
		} else if !model.VersionAdvanced(u.FromVersion, u.TargetVersion) {
			continue
		}
		out[source] = append(out[source], u)
	}
	return out, rows.Err()
}
func (r *Repo) DependencyUpdates(ctx context.Context, sc Scope, id string) (*model.DependencyUpdates, error) {
	if !r.versionManagement {
		return nil, ErrVersionBaselineUnavailable
	}
	root, err := readVisibilityNode(ctx, r.db, id)
	if err != nil {
		return nil, err
	}
	if root.Plugin.OwnerUID != sc.CallerUID || root.Plugin.SpaceID == nil || *root.Plugin.SpaceID != sc.SpaceID || root.Plugin.IsEmbedded || (root.Plugin.Type != model.PluginTypeExpert && root.Plugin.Type != model.PluginTypeExpertTeam) {
		return nil, ErrNotFound
	}
	if root.Plugin.ListingState != model.PluginListingStatePublished {
		return nil, ErrConflict
	}
	found, err := queryDependencyUpdates(ctx, r.db, sc, []string{id})
	if err != nil {
		return nil, err
	}
	updates := found[id]
	if updates == nil {
		updates = []model.DependencyUpdate{}
	}
	return &model.DependencyUpdates{PluginID: id, PublishedVersionID: root.PublishedID, DraftRevision: model.PluginDraftRevision(&root.Plugin), Updates: updates}, nil
}

func (r *Repo) SkipDependencyUpdates(ctx context.Context, sc Scope, id string, targets []model.DependencyUpdateTarget) error {
	if !ValidUpdateTargets(targets) {
		return ErrInvalidRelation
	}
	if !r.versionManagement {
		return ErrVersionBaselineUnavailable
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	root, err := getOwnedForUpdate(ctx, tx, sc, id)
	if err != nil {
		return err
	}
	if root.IsEmbedded || (root.Type != model.PluginTypeExpert && root.Type != model.PluginTypeExpertTeam) {
		return ErrNotFound
	}
	if root.ListingState != model.PluginListingStatePublished {
		return ErrConflict
	}
	candidates, err := queryDependencyUpdates(ctx, tx, sc, []string{id})
	if err != nil {
		return err
	}
	allowed := candidateKeys(candidates[id])
	for _, target := range targets {
		if !allowed[target] {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `INSERT IGNORE INTO plugin_dependency_reminder_skips (space_id,uid,plugin_id,dependency_plugin_id,target_version_id,created_at) VALUES (?,?,?,?,?,?)`, sc.SpaceID, sc.CallerUID, id, target.PluginID, targetKey(target), r.now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const connectorFingerprint = "SHA2(CONCAT(t.manifest_hash,':',t.plugin_hash),256)"

func targetKey(t model.DependencyUpdateTarget) string {
	if t.TargetConfigRevision != "" {
		return t.TargetConfigRevision
	}
	return t.TargetVersionID
}

// ValidUpdateTargets enforces the request bound and mutually exclusive revision forms.
func ValidUpdateTargets(ts []model.DependencyUpdateTarget) bool {
	if len(ts) == 0 || len(ts) > 100 {
		return false
	}
	seen := map[string]bool{}
	for _, t := range ts {
		if strings.TrimSpace(t.PluginID) == "" || seen[t.PluginID] || (t.TargetConfigRevision == "") == (t.TargetVersionID == "") || strings.TrimSpace(targetKey(t)) == "" {
			return false
		}
		seen[t.PluginID] = true
	}
	return true
}
func candidateKeys(updates []model.DependencyUpdate) map[model.DependencyUpdateTarget]bool {
	m := map[model.DependencyUpdateTarget]bool{}
	for _, u := range updates {
		m[model.DependencyUpdateTarget{PluginID: u.PluginID, TargetVersionID: u.TargetVersionID, TargetConfigRevision: u.TargetConfigRevision}] = true
	}
	return m
}

// RefreshConnectorBaselines records first-seen Connector revisions for a released
// Expert and retains acknowledged revisions for references that still exist.
func RefreshConnectorBaselines(ctx context.Context, tx *sql.Tx, source, version string) error {
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

func approveConnectorRevisions(ctx context.Context, tx *sql.Tx, source, review string) error {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT selection_json FROM plugin_review_config_targets WHERE review_id=?", review).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var acks map[string]string
	if err = json.Unmarshal(raw, &acks); err != nil {
		return err
	}
	for id, want := range acks {
		var actual string
		if err = tx.QueryRowContext(ctx, `SELECT `+connectorFingerprint+` FROM plugins t WHERE t.plugin_id=? AND t.plugin_type='connector' AND t.listing_state='published' AND t.status=1 AND t.deleted_at IS NULL FOR UPDATE`, id).Scan(&actual); err == sql.ErrNoRows {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if want != actual {
			return ErrConflict
		}
	}
	return ackConnectors(ctx, tx, source, acks)
}
