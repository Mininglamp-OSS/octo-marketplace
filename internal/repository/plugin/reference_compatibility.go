package plugin

import (
	"context"
	"database/sql"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Compatibility releases predate the reverse index. Read their immutable
// snapshots rather than skipping delete protection or rewriting old history.
// The caller already holds the target lock used by reference/release writers.
func rejectSnapshotIncomingRelations(ctx context.Context, tx *sql.Tx, target string) error {
	members := map[InstallVersionKey]struct{}{}
	inspect := func(raw []byte, team bool) error {
		rels, err := model.DecodeSnapshotRelations(raw)
		if err != nil {
			return ErrReferenceIndexIncomplete
		}
		for _, rel := range rels {
			if rel.TargetPluginID == target {
				return ErrRelationInUse
			}
			if team && rel.Type == "expert_team_expert" && rel.TargetVersionID != nil && *rel.TargetVersionID != "" {
				members[InstallVersionKey{PluginID: rel.TargetPluginID, VersionID: *rel.TargetVersionID}] = struct{}{}
			}
		}
		return nil
	}
	// A missing current release snapshot is an integrity failure, not evidence
	// that no one references the target. Delisted sources still protect edges.
	if err := func() error {
		rows, err := tx.QueryContext(ctx, `SELECT p.plugin_type,v.relations_json FROM plugins p
 LEFT JOIN plugin_versions v ON v.plugin_id=p.plugin_id AND v.version_id=p.published_version_id
 WHERE p.status=1 AND p.deleted_at IS NULL AND p.published_version_id IS NOT NULL`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind model.PluginType
			var raw []byte
			if err = rows.Scan(&kind, &raw); err != nil {
				return err
			}
			if err = inspect(raw, kind == model.PluginTypeExpertTeam); err != nil {
				return err
			}
		}
		return rows.Err()
	}(); err != nil {
		return err
	}
	// A Team may pin an older member release, whose Skill references are absent
	// from that member's current published version. Batch those exact pairs.
	keys := make([]InstallVersionKey, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	for start := 0; start < len(keys); start += 100 {
		batch := keys[start:min(start+100, len(keys))]
		args := make([]any, 0, 2*len(batch))
		pairs := make([]string, 0, len(batch))
		for _, key := range batch {
			pairs = append(pairs, "(?,?)")
			args = append(args, key.PluginID, key.VersionID)
		}
		if err := func() error {
			rows, err := tx.QueryContext(ctx, `SELECT v.relations_json,p.status,p.deleted_at FROM plugin_versions v
 JOIN plugins p ON p.plugin_id=v.plugin_id WHERE (v.plugin_id,v.version_id) IN (`+strings.Join(pairs, ",")+`)`, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			found := 0
			for rows.Next() {
				var raw []byte
				var status int
				var deleted sql.NullTime
				if err = rows.Scan(&raw, &status, &deleted); err != nil {
					return err
				}
				found++
				if status != 1 || deleted.Valid {
					continue
				}
				if err = inspect(raw, false); err != nil {
					return err
				}
			}
			if err = rows.Err(); err != nil {
				return err
			}
			if found != len(batch) {
				return ErrReferenceIndexIncomplete
			}
			return nil
		}(); err != nil {
			return err
		}
	}
	return nil
}
