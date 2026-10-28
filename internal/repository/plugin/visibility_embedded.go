package plugin

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Only container-owned embedded rows inherit an audience change. Narrowing also
// visits live and historically released edges so previously promoted children
// cannot remain readable after a newer draft or release changes the topology. Widening visits the released topology only:
// an editable draft must not publish additional children through this operation.
func syncVisibilityEmbeddedAudience(ctx context.Context, tx *sql.Tx, root *model.Plugin, target model.PluginVisibility, now time.Time) error {
	if root.SpaceID == nil {
		return ErrNotFound
	}
	type node struct {
		plugin model.Plugin
		pin    *string
	}
	queue := []node{{plugin: *root}}
	seen := map[string]bool{}
	scope := Scope{CallerUID: root.OwnerUID, SpaceID: *root.SpaceID}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		key := n.plugin.ID
		if n.pin != nil {
			key += ":" + *n.pin
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if n.plugin.Type != model.PluginTypeExpert && n.plugin.Type != model.PluginTypeExpertTeam {
			continue
		}
		rels, err := visibilityOwnedRelations(ctx, tx, n.plugin.ID, n.pin, target == model.PluginVisibilityPrivate)
		if err != nil {
			return err
		}
		for _, edge := range rels {
			child, err := getOwnedForUpdate(ctx, tx, scope, edge.TargetPluginID)
			if errors.Is(err, ErrNotFound) {
				continue // Foreign ownership/Space does not belong to this container.
			}
			if err != nil {
				return err
			}
			if !child.IsEmbedded || !validPersistedRelation(edge.Type, n.plugin.Type, child.Type) {
				continue // Never descend through a standalone referenced Expert.
			}
			if target == model.PluginVisibilitySpace {
				_, err = tx.ExecContext(ctx, `UPDATE plugins SET visibility=?,listing_state='published',updated_at=? WHERE plugin_id=? AND owner_uid=? AND space_id=? AND is_embedded=1 AND deleted_at IS NULL`, target, now, child.ID, root.OwnerUID, *root.SpaceID)
			} else {
				_, err = tx.ExecContext(ctx, `UPDATE plugins SET visibility=?,updated_at=? WHERE plugin_id=? AND owner_uid=? AND space_id=? AND is_embedded=1 AND deleted_at IS NULL`, target, now, child.ID, root.OwnerUID, *root.SpaceID)
			}
			if err != nil {
				return err
			}
			queue = append(queue, node{plugin: *child, pin: edge.TargetVersionID})
		}
	}
	return nil
}

func visibilityOwnedRelations(ctx context.Context, tx *sql.Tx, id string, pin *string, includeLive bool) ([]model.PluginRelation, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT v.relations_json FROM plugins p JOIN plugin_versions v ON v.plugin_id=p.plugin_id AND v.version_id=COALESCE(?,p.published_version_id,p.current_version_id) WHERE p.plugin_id=?`, pin, id).Scan(&raw)
	var rels []model.PluginRelation
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		rels, err = model.DecodeSnapshotRelations(raw)
		if err != nil {
			return nil, ErrInvalidRelation
		}
	} else {
		includeLive = true // Pre-snapshot legacy embedded member.
	}
	if !includeLive {
		return rels, nil
	}
	// A replacement release may have removed an embedded descendant from both
	// the live graph and the published pointer. Release evidence, not every saved
	// draft, identifies additional topologies whose audience must be revoked.
	history, err := tx.QueryContext(ctx, `SELECT v.relations_json FROM plugin_versions v JOIN plugin_version_releases r ON r.plugin_id=v.plugin_id AND r.version_id=v.version_id WHERE v.plugin_id=? ORDER BY v.version_id`, id)
	if err != nil {
		return nil, err
	}
	for history.Next() {
		var snapshot []byte
		if err := history.Scan(&snapshot); err != nil {
			history.Close()
			return nil, err
		}
		previous, err := model.DecodeSnapshotRelations(snapshot)
		if err != nil {
			history.Close()
			return nil, ErrInvalidRelation
		}
		rels = append(rels, previous...)
	}
	err = history.Err()
	history.Close()
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT target_plugin_id,target_version_id,relation_type FROM plugin_relations WHERE source_plugin_id=? AND status=1 AND deleted_at IS NULL ORDER BY relation_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rel model.PluginRelation
		var pin sql.NullString
		if err := rows.Scan(&rel.TargetPluginID, &pin, &rel.Type); err != nil {
			return nil, err
		}
		rel.TargetVersionID = nullString(pin)
		rels = append(rels, rel)
	}
	return rels, rows.Err()
}
