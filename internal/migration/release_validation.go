package migration

import (
	"context"
	"database/sql"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func verifyMigrationRelease(ctx context.Context, q applyQuery, n Node) error {
	if n.VersionID == "" {
		return nil
	}
	var declared, released, current, currentID, publishedID sql.NullString
	err := q.QueryRowContext(ctx, `SELECT v.declared_version,r.version_label,p.current_version,p.current_version_id,p.published_version_id FROM plugins p JOIN plugin_versions v ON v.plugin_id=p.plugin_id AND v.version_id=? LEFT JOIN plugin_version_releases r ON r.plugin_id=v.plugin_id AND r.version_id=v.version_id WHERE p.plugin_id=?`, n.VersionID, n.Plugin.ID).Scan(&declared, &released, &current, &currentID, &publishedID)
	if err != nil || !declared.Valid || declared.String != n.VersionLabel || !current.Valid || current.String != n.VersionLabel || !currentID.Valid || currentID.String != n.VersionID {
		return errApply
	}
	if n.Release {
		if !released.Valid || released.String != n.VersionLabel || !publishedID.Valid || publishedID.String != n.VersionID {
			return errApply
		}
	} else if released.Valid || publishedID.Valid {
		return errApply
	}
	return nil
}

// Snapshot sequence (plugin_versions.version) is intentionally independent of
// the business label. A saved snapshot alone is never a released dependency.
func validateMigrationReleases(p Plan) []Issue {
	var issues []Issue
	nodes := make(map[string]Node, len(p.Nodes))
	for _, n := range p.Nodes {
		nodes[n.Plugin.ID] = n
		if n.VersionID == "" {
			continue // Connectors have no saved version in the reference contract.
		}
		_, valid := model.ParseVersion(n.VersionLabel)
		for _, part := range strings.Split(n.VersionLabel, ".") {
			if len(part) > 9 || strings.Trim(part, "0123456789") != "" {
				valid = false
			}
		}
		if !valid {
			issues = append(issues, Issue{n.SourceID, "invalid_declared_version"})
		}
	}
	for _, n := range p.Nodes {
		for _, edge := range n.Relations {
			if edge.Type == "expert_connector" {
				continue
			}
			target, ok := nodes[edge.TargetPluginID]
			if !ok || !target.Release || target.VersionID == "" || edge.TargetVersionID == nil || *edge.TargetVersionID != target.VersionID {
				issues = append(issues, Issue{n.SourceID, "released_version_required"})
			}
		}
	}
	return issues
}
