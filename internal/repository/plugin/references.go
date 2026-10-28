package plugin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

type ReferenceValidationError struct{ Field, Reason string }

func (e *ReferenceValidationError) Error() string { return "invalid plugin reference" }
func (e *ReferenceValidationError) Unwrap() error { return ErrInvalidRelation }
func referenceFieldError(index, depth int, field, reason string) error {
	path := "relations"
	if depth == 0 {
		path = fmt.Sprintf("relations[%d].%s", index, field)
	}
	return &ReferenceValidationError{Field: path, Reason: reason}
}

var ErrReferenceIndexIncomplete = errors.New("reference index coverage is incomplete")

// WithReferences enables independent references only after the complete delivery
// is configured. Construction-time only; the default preserves legacy writes.
func (r *Repo) WithReferences(enabled bool) *Repo { r.references = enabled; return r }

func referenceVisibility(v model.PluginVisibility) model.PluginVisibility {
	if v == "public" {
		return model.PluginVisibilitySystem
	}
	return v
}
func referenceAudience(source, target model.Plugin) bool {
	sv, tv := referenceVisibility(source.Visibility), referenceVisibility(target.Visibility)
	if tv == model.PluginVisibilitySystem {
		return true
	}
	if source.SpaceID == nil || target.SpaceID == nil || *source.SpaceID != *target.SpaceID {
		return false
	}
	if sv == model.PluginVisibilitySystem {
		return false
	}
	if tv == model.PluginVisibilitySpace {
		return true
	}
	return sv == model.PluginVisibilityPrivate && tv == model.PluginVisibilityPrivate && source.OwnerUID == target.OwnerUID
}

// ValidateReferenceMetadata is shared by service and transaction validation.
// No embedded instructions or ability overrides are accepted on a team edge.
func ValidateReferenceMetadata(source model.PluginType, rels []model.PluginRelation) error {
	seen := map[string]bool{}
	keys := map[string]bool{}
	leaders := 0
	for _, edge := range rels {
		if edge.Status != 1 || edge.TargetPluginID == "" || edge.TargetPluginID == edge.SourcePluginID || !validPersistedRelation(edge.Type, source, edge.TargetPluginType) {
			return ErrInvalidRelation
		}
		key := edge.Type + "\x00" + edge.TargetPluginID
		if seen[key] {
			return ErrInvalidRelation
		}
		seen[key] = true
		if edge.Type == "expert_connector" {
			if edge.TargetVersionID != nil {
				return ErrInvalidRelation
			}
		} else if edge.TargetVersionID == nil || strings.TrimSpace(*edge.TargetVersionID) == "" {
			return ErrInvalidRelation
		}
		var data map[string]json.RawMessage
		if len(edge.Data) > 0 && string(edge.Data) != "null" {
			if model.ValidateUniqueJSONFields(edge.Data) != nil || json.Unmarshal(edge.Data, &data) != nil || data == nil {
				return ErrInvalidRelation
			}
		}
		if edge.Type != "expert_team_expert" {
			if len(data) != 0 {
				return ErrInvalidRelation
			}
			continue
		}
		for k := range data {
			if k != "member_key" && k != "role" && k != "is_leader" {
				return ErrInvalidRelation
			}
		}
		var memberKey, role string
		var leader bool
		if json.Unmarshal(data["member_key"], &memberKey) != nil || memberKey == "" || len(memberKey) > 128 || keys[memberKey] {
			return ErrInvalidRelation
		}
		keys[memberKey] = true
		if v, ok := data["role"]; ok && (json.Unmarshal(v, &role) != nil || !utf8.ValidString(role) || utf8.RuneCountInString(strings.TrimSpace(role)) > 500) {
			return ErrInvalidRelation
		}
		if v, ok := data["is_leader"]; ok && json.Unmarshal(v, &leader) != nil {
			return ErrInvalidRelation
		}
		if !leader && strings.TrimSpace(role) == "leader" {
			return ErrInvalidRelation
		}
		if leader {
			leaders++
		}
	}
	if source == model.PluginTypeExpertTeam && leaders != 1 {
		return ErrInvalidRelation
	}
	return nil
}

func (r *Repo) lockReferences(ctx context.Context, tx *sql.Tx, scope Scope, source model.Plugin, rels []model.PluginRelation, inGraph map[string]struct{}) error {
	if !r.references {
		return lockRelationTargets(ctx, tx, scope, source, rels, inGraph)
	}
	if source.IsEmbedded {
		return ErrInvalidRelation
	}
	existing := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT relation_type,target_plugin_id,target_version_id FROM plugin_relations WHERE source_plugin_id=? AND status=1 AND deleted_at IS NULL`, source.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var typ, target string
		var pin sql.NullString
		if err := rows.Scan(&typ, &target, &pin); err != nil {
			rows.Close()
			return err
		}
		existing[typ+"\x00"+target+"\x00"+pin.String] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	return validateReferenceTree(ctx, tx, source, source, rels, true, map[string]bool{}, 0, existing)
}

// validateReferenceTree checks frozen versions, never editable descendant edges.
// The root audience is checked at every depth, so team composition cannot hide a
// private dependency inside a broader member. Errors never disclose a target.
func validateReferenceTree(ctx context.Context, tx *sql.Tx, root, source model.Plugin, rels []model.PluginRelation, listed bool, seen map[string]bool, depth int, existing ...map[string]bool) error {
	if depth > 2 || len(rels) > maxGraphEdges {
		return ErrInvalidRelation
	}
	// Snapshot codecs from the legacy path may omit target type; load authoritative
	// types first, then validate structural and metadata constraints together.
	resolved := make([]model.PluginRelation, len(rels))
	copy(resolved, rels)
	targets := make([]model.Plugin, len(rels))
	for i, edge := range rels {
		target, err := lockIndependentReferenceTarget(ctx, tx, root, source, edge.TargetPluginID)
		if err != nil {
			return err
		}
		if listed && target.ListingState != model.PluginListingStatePublished {
			pin := ""
			if edge.TargetVersionID != nil {
				pin = *edge.TargetVersionID
			}
			key := edge.Type + "\x00" + edge.TargetPluginID + "\x00" + pin
			if len(existing) == 0 || !existing[0][key] {
				return referenceFieldError(i, depth, "target_plugin_id", "target_not_published")
			}
		}
		resolved[i].TargetPluginType = target.Type
		targets[i] = target
	}
	if err := ValidateReferenceMetadata(source.Type, resolved); err != nil {
		return err
	}
	for i, edge := range resolved {
		if err := validateReleasedReference(ctx, tx, root, targets[i], edge, seen, i, depth); err != nil {
			return err
		}
	}
	return nil
}

func lockIndependentReferenceTarget(ctx context.Context, tx *sql.Tx, root, source model.Plugin, targetID string) (model.Plugin, error) {
	var target model.Plugin
	var sid sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT plugin_id,plugin_type,is_embedded,owner_uid,space_id,visibility,listing_state FROM plugins WHERE plugin_id=? AND status=1 AND deleted_at IS NULL FOR UPDATE`, targetID).Scan(&target.ID, &target.Type, &target.IsEmbedded, &target.OwnerUID, &sid, &target.Visibility, &target.ListingState)
	if err == sql.ErrNoRows {
		return target, ErrNotFound
	}
	if err != nil {
		return target, err
	}
	target.SpaceID = nullString(sid)
	if target.IsEmbedded || !referenceAudience(source, target) || !referenceAudience(root, target) {
		return target, ErrNotFound
	}
	return target, nil
}

// Both strict reference graphs and retained pins in legacy roots traverse the
// exact released snapshot under the same locks, audience checks and graph bounds.
func validateReleasedReference(ctx context.Context, tx *sql.Tx, root, target model.Plugin, edge model.PluginRelation, seen map[string]bool, index, depth int) error {
	if edge.TargetVersionID == nil {
		return nil
	}
	key := edge.TargetPluginID + "\x00" + *edge.TargetVersionID
	if seen[key] {
		return nil
	}
	seen[key] = true
	if len(seen) > maxGraphNodes {
		return ErrGraphTooLarge
	}
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT v.relations_json FROM plugin_versions v JOIN plugin_version_releases rel ON rel.plugin_id=v.plugin_id AND rel.version_id=v.version_id WHERE v.plugin_id=? AND v.version_id=? FOR UPDATE`, edge.TargetPluginID, *edge.TargetVersionID).Scan(&raw)
	if err == sql.ErrNoRows {
		return referenceFieldError(index, depth, "target_version_id", "released_version_required")
	}
	if err != nil {
		return err
	}
	children, err := model.DecodeSnapshotRelations(raw)
	if err != nil {
		return ErrInvalidRelation
	}
	return validateReferenceTree(ctx, tx, root, target, children, false, seen, depth+1)
}

func writeReferenceIndex(ctx context.Context, tx *sql.Tx, source, version string, rels []model.PluginRelation) error {
	for _, edge := range rels {
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_version_relation_refs (source_plugin_id,source_version_id,relation_id,relation_type,target_plugin_id,target_version_id) VALUES (?,?,?,?,?,?)`, source, version, edge.ID, edge.Type, edge.TargetPluginID, edge.TargetVersionID); err != nil {
			return err
		}
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT relations_json FROM plugin_versions WHERE plugin_id=? AND version_id=?`, source, version).Scan(&raw); err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	_, err := tx.ExecContext(ctx, `INSERT INTO plugin_version_reference_index_state(source_plugin_id,source_version_id,relation_count,snapshot_digest,verified_at) VALUES (?,?,?,?,UTC_TIMESTAMP(3))`, source, version, len(rels), hex.EncodeToString(digest[:]))
	return err
}

func (r *Repo) validateReleaseReferences(ctx context.Context, tx *sql.Tx, p model.Plugin, versionID string) error {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT relations_json FROM plugin_versions WHERE plugin_id=? AND version_id=?`, p.ID, versionID).Scan(&raw); err != nil {
		return err
	}
	rels, err := model.DecodeSnapshotRelations(raw)
	if err != nil {
		return ErrInvalidRelation
	}
	if !r.references {
		if len(rels) == 0 {
			return nil
		}
		owned, err := liveRelationTargetSet(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		sc := Scope{CallerUID: p.OwnerUID, Admin: p.SpaceID == nil}
		if p.SpaceID != nil {
			sc.SpaceID = *p.SpaceID
		}
		// A rollback can leave a mixed root: retained pins alongside new unpinned
		// edges. Preserve legacy validation for the complete root, including owned
		// embedded children and unlisted targets. A filtered pinned subset is not
		// a complete Team and cannot be checked for a leader on its own.
		if err := lockRelationTargets(ctx, tx, sc, p, rels, owned); err != nil {
			return err
		}
		seen := map[string]bool{}
		for i, edge := range rels {
			if edge.TargetVersionID == nil {
				continue
			}
			if len(rels) > maxGraphEdges {
				return ErrInvalidRelation
			}
			target, err := lockIndependentReferenceTarget(ctx, tx, p, p, edge.TargetPluginID)
			if err != nil {
				return err
			}
			if target.ListingState != model.PluginListingStatePublished {
				return referenceFieldError(i, 0, "target_plugin_id", "target_not_published")
			}
			if err := validateReleasedReference(ctx, tx, p, target, edge, seen, i, 0); err != nil {
				return err
			}
		}
		return nil
	}
	return validateReferenceTree(ctx, tx, p, p, rels, true, map[string]bool{}, 0)
}

func (r *Repo) rejectPublishedMutation(ctx context.Context, tx *sql.Tx, p *model.Plugin) error {
	if !r.references {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id=?`, p.ID).Scan(&count); err != nil {
		return err
	}
	if count > 0 || p.ListingState == model.PluginListingStatePublished || p.ListingState == model.PluginListingStateDelisted {
		return ErrConflict
	}
	// A legacy approved publication is also evidence; delisting must not erase it.
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_review_requests WHERE plugin_id=? AND status='approved'`, p.ID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrConflict
	}
	return nil
}

// ensureReferenceIndexComplete refuses a misleading partial result. Every
// historical snapshot the caller may inspect must have a verified index,
// including empty snapshots, and its source digest/count must still match.
func (r *Repo) ensureReferenceIndexComplete(ctx context.Context, scope Scope) error {
	where, args := "1=1", []any{}
	if !scope.Admin {
		where = visibilitySQL + ` AND (rel.version_id IS NOT NULL OR (p.owner_uid=? AND p.space_id=?))`
		args = []any{scope.SpaceID, scope.CallerUID, scope.CallerUID, scope.SpaceID}
	}
	var incomplete bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plugin_versions v JOIN plugins p ON p.plugin_id=v.plugin_id LEFT JOIN plugin_version_releases rel ON rel.plugin_id=v.plugin_id AND rel.version_id=v.version_id LEFT JOIN plugin_version_reference_index_state idx ON idx.source_plugin_id=v.plugin_id AND idx.source_version_id=v.version_id WHERE p.status=1 AND p.deleted_at IS NULL AND `+where+` AND (idx.source_version_id IS NULL OR idx.snapshot_digest<>SHA2(CAST(v.relations_json AS CHAR CHARACTER SET utf8mb4),256) OR idx.relation_count<>(SELECT COUNT(*) FROM plugin_version_relation_refs edge WHERE edge.source_version_id=v.version_id)))`, args...).Scan(&incomplete)
	if err != nil {
		return err
	}
	if incomplete {
		return ErrReferenceIndexIncomplete
	}
	return nil
}

// rejectPublishedIncomingRelations protects only each surviving source's current
// published graph. Historical releases remain audit data, not permanent delete
// locks. Team→Expert→Skill expansion follows each Team's pinned member version,
// even after the member has published a newer graph without that Skill.
// The target row is already locked, serializing concurrent reference writers.
func (r *Repo) rejectPublishedIncomingRelations(ctx context.Context, tx *sql.Tx, target string) error {
	if !r.references {
		return rejectSnapshotIncomingRelations(ctx, tx, target)
	}
	// Enabling the reverse index does not erase publication evidence predating
	// formal release pointers. Check those legacy graphs before indexed releases.
	if err := rejectLegacySnapshotIncomingRelations(ctx, tx, target); err != nil {
		return err
	}
	var incomplete bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM (
  SELECT p.plugin_id,p.published_version_id AS version_id FROM plugins p
   WHERE p.status=1 AND p.deleted_at IS NULL AND p.published_version_id IS NOT NULL
  UNION
  SELECT member.target_plugin_id,member.target_version_id
   FROM plugins team JOIN plugin_version_relation_refs member
    ON member.source_plugin_id=team.plugin_id AND member.source_version_id=team.published_version_id
   WHERE team.status=1 AND team.deleted_at IS NULL AND member.relation_type='expert_team_expert'
 ) selected
 LEFT JOIN plugin_versions v ON v.plugin_id=selected.plugin_id AND v.version_id=selected.version_id
 LEFT JOIN plugin_version_reference_index_state idx ON idx.source_plugin_id=v.plugin_id AND idx.source_version_id=v.version_id
 WHERE v.version_id IS NULL OR idx.source_version_id IS NULL
 OR idx.snapshot_digest<>SHA2(CAST(v.relations_json AS CHAR CHARACTER SET utf8mb4),256)
 OR idx.relation_count<>(SELECT COUNT(*) FROM plugin_version_relation_refs edge WHERE edge.source_plugin_id=v.plugin_id AND edge.source_version_id=v.version_id))`).Scan(&incomplete); err != nil {
		return err
	}
	if incomplete {
		return ErrReferenceIndexIncomplete
	}
	rows, err := tx.QueryContext(ctx, `SELECT edge.relation_id FROM plugin_version_relation_refs edge
 JOIN plugins source ON source.plugin_id=edge.source_plugin_id
 WHERE edge.target_plugin_id=? AND source.status=1 AND source.deleted_at IS NULL
 AND (source.published_version_id=edge.source_version_id OR EXISTS (
  SELECT 1 FROM plugins team JOIN plugin_version_relation_refs member
   ON member.source_plugin_id=team.plugin_id AND member.source_version_id=team.published_version_id
  WHERE team.status=1 AND team.deleted_at IS NULL AND member.relation_type='expert_team_expert'
   AND member.target_plugin_id=edge.source_plugin_id AND member.target_version_id=edge.source_version_id))
 ORDER BY edge.source_plugin_id,edge.relation_id FOR UPDATE`, target)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return ErrRelationInUse
	}
	return rows.Err()
}
