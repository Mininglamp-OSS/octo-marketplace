package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Compatibility releases predate the reverse index. Read their immutable
// snapshots rather than skipping delete protection or rewriting old history.
// The caller already holds the target lock used by reference/release writers.
func rejectSnapshotIncomingRelations(ctx context.Context, tx *sql.Tx, target string) error {
	return rejectCompatibilityIncomingRelations(ctx, tx, target, false)
}

// The indexed path still needs to account for publication evidence that predates
// formal release pointers. Formal roots keep their existing index validation.
func rejectLegacySnapshotIncomingRelations(ctx context.Context, tx *sql.Tx, target string) error {
	return rejectCompatibilityIncomingRelations(ctx, tx, target, true)
}

func rejectCompatibilityIncomingRelations(ctx context.Context, tx *sql.Tx, target string, legacyOnly bool) error {
	// A true value means an exact applicable snapshot; false is a candidate for
	// an unresolved legacy publication. Only target-relevant ambiguity blocks
	// deletion, rather than turning unrelated old history into a global ban.
	members := map[InstallVersionKey]bool{}
	inspect := func(rels []model.PluginRelation, team, certain bool) error {
		for _, rel := range rels {
			if rel.TargetPluginID == target {
				if certain {
					return ErrRelationInUse
				}
				return ErrReferenceIndexIncomplete
			}
			if team && rel.Type == "expert_team_expert" {
				key := InstallVersionKey{PluginID: rel.TargetPluginID}
				if rel.TargetVersionID != nil {
					if *rel.TargetVersionID == "" {
						return ErrReferenceIndexIncomplete
					}
					key.VersionID = *rel.TargetVersionID
				}
				members[key] = members[key] || certain
			}
		}
		return nil
	}
	check := func(snapshot compatibilitySnapshot, parentCertain bool) error {
		if snapshot.Status != 1 || snapshot.Deleted.Valid {
			return nil
		}
		raw := snapshot.Relations
		if snapshot.Live {
			// Very old embedded members may never have had a snapshot. Their
			// current live graph remains the only available dependency evidence.
			rels, err := compatibilityLiveRelations(ctx, tx, snapshot.PluginID)
			if err != nil {
				return err
			}
			return inspect(rels, snapshot.Kind == model.PluginTypeExpertTeam, parentCertain && snapshot.Certain)
		}
		rels, err := model.DecodeSnapshotRelations(raw)
		if err != nil {
			return ErrReferenceIndexIncomplete
		}
		return inspect(rels, snapshot.Kind == model.PluginTypeExpertTeam, parentCertain && snapshot.Certain)
	}
	where := `p.status=1 AND p.deleted_at IS NULL AND p.plugin_id<>? AND (
 p.published_version_id IS NOT NULL OR p.listing_state IN ('published','delisted')
 OR EXISTS (SELECT 1 FROM plugin_review_requests rr WHERE rr.plugin_id=p.plugin_id AND rr.status='approved' AND rr.deleted_at IS NULL)
 OR EXISTS (SELECT 1 FROM plugin_audit_logs a WHERE a.plugin_id=p.plugin_id AND a.action IN ('publish','review_approve')))`
	if legacyOnly {
		where += ` AND p.published_version_id IS NULL`
	}
	roots, err := compatibilitySnapshots(ctx, tx, where, []any{target})
	if err != nil {
		return err
	}
	for _, root := range roots {
		if err := check(root, true); err != nil {
			return err
		}
	}
	// An unpinned legacy Team member selects its applicable publication, while a
	// pinned member keeps its exact historical version. Never normalize a pin to
	// latest or infer that a newer draft replaced the old published graph.
	var pinned []InstallVersionKey
	var unpinned []string
	for key := range members {
		if key.VersionID == "" {
			unpinned = append(unpinned, key.PluginID)
		} else {
			pinned = append(pinned, key)
		}
	}
	for start := 0; start < len(pinned); start += 100 {
		batch := pinned[start:min(start+100, len(pinned))]
		args := make([]any, 0, 2*len(batch))
		pairs := make([]string, 0, len(batch))
		for _, key := range batch {
			pairs = append(pairs, "(?,?)")
			args = append(args, key.PluginID, key.VersionID)
		}
		rows, err := tx.QueryContext(ctx, `SELECT v.plugin_id,v.version_id,v.relations_json,p.plugin_type,p.status,p.deleted_at FROM plugin_versions v
 JOIN plugins p ON p.plugin_id=v.plugin_id WHERE (v.plugin_id,v.version_id) IN (`+strings.Join(pairs, ",")+`)`, args...)
		if err != nil {
			return err
		}
		var selected []compatibilitySnapshot
		for rows.Next() {
			var snapshot compatibilitySnapshot
			var version string
			if err := rows.Scan(&snapshot.PluginID, &version, &snapshot.Relations, &snapshot.Kind, &snapshot.Status, &snapshot.Deleted); err != nil {
				rows.Close()
				return err
			}
			snapshot.Certain = members[InstallVersionKey{PluginID: snapshot.PluginID, VersionID: version}]
			selected = append(selected, snapshot)
		}
		readErr := rows.Err()
		rows.Close()
		if readErr != nil {
			return readErr
		}
		if len(selected) != len(batch) {
			return ErrReferenceIndexIncomplete
		}
		for _, snapshot := range selected {
			if snapshot.Status == 1 && !snapshot.Deleted.Valid && snapshot.Kind != model.PluginTypeExpert {
				return ErrReferenceIndexIncomplete
			}
			if err := check(snapshot, true); err != nil {
				return err
			}
		}
	}
	for start := 0; start < len(unpinned); start += 100 {
		batch := unpinned[start:min(start+100, len(unpinned))]
		args := make([]any, 0, len(batch))
		for _, id := range batch {
			args = append(args, id)
		}
		selected, err := compatibilitySnapshots(ctx, tx, `p.plugin_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")+`)`, args)
		if err != nil {
			return err
		}
		found := map[string]bool{}
		for _, snapshot := range selected {
			found[snapshot.PluginID] = true
			if snapshot.Status == 1 && !snapshot.Deleted.Valid && snapshot.Kind != model.PluginTypeExpert {
				return ErrReferenceIndexIncomplete
			}
			if err := check(snapshot, members[InstallVersionKey{PluginID: snapshot.PluginID}]); err != nil {
				return err
			}
		}
		if len(found) != len(batch) {
			return ErrReferenceIndexIncomplete
		}
	}
	return nil
}

type compatibilitySnapshot struct {
	PluginID          string
	Kind              model.PluginType
	Status            int
	Deleted           sql.NullTime
	Relations         []byte
	Certain           bool
	Live              bool
	current           bool
	approvalProven    bool
	approvalRelations []byte
}

// Formal pointers select exactly one release. A currently listed legacy row can
// use its consistent current snapshot: legacy Admin/private writes directly
// affect listed content. Delisted/draft rows with publication evidence may have
// newer unapproved saves; without an exact release identity their history is
// only a set of candidates, not proof that every old version remains published.
// A candidate that reaches the target requires explicit baseline reconciliation.
func compatibilitySnapshots(ctx context.Context, tx *sql.Tx, where string, args []any) ([]compatibilitySnapshot, error) {
	const current = `(p.listing_state='published' AND current_v.version_id IS NOT NULL
 AND current_v.manifest_hash=p.manifest_hash AND current_v.plugin_hash=p.plugin_hash)`
	const approved = `(p.listing_state='delisted' AND current_v.version_id IS NOT NULL
 AND current_v.manifest_hash=p.manifest_hash AND current_v.plugin_hash=p.plugin_hash
 AND current_v.declared_version=approval.version AND current_v.created_at=approval.reviewed_at
 AND current_v.created_by=approval.reviewer_uid
 AND current_v.manifest_hash=approval.manifest_hash AND current_v.plugin_hash=approval.plugin_hash
 AND NOT EXISTS (SELECT 1 FROM plugin_versions same_time WHERE same_time.plugin_id=p.plugin_id
  AND same_time.created_at=current_v.created_at AND same_time.version_id<>current_v.version_id)
 AND NOT EXISTS (SELECT 1 FROM plugin_audit_logs changed WHERE changed.plugin_id=p.plugin_id
  AND changed.action IN ('update','publish') AND changed.created_at>=approval.reviewed_at))`
	rows, err := tx.QueryContext(ctx, `SELECT p.plugin_id,p.plugin_type,p.status,p.deleted_at,
 p.published_version_id,p.current_version_id,current_v.version_id,
 (p.published_version_id IS NOT NULL OR `+current+`),v.version_id,v.relations_json,
 COALESCE(`+approved+`,FALSE),approval.relations_json
 FROM plugins p
 LEFT JOIN plugin_versions current_v ON current_v.plugin_id=p.plugin_id AND current_v.version_id=p.current_version_id
 LEFT JOIN plugin_review_requests approval ON p.published_version_id IS NULL
 AND approval.plugin_id=p.plugin_id AND approval.status='approved' AND approval.deleted_at IS NULL
 AND approval.reviewed_at IS NOT NULL
 AND NOT EXISTS (SELECT 1 FROM plugin_review_requests newer WHERE newer.plugin_id=p.plugin_id
  AND newer.status='approved' AND newer.deleted_at IS NULL AND newer.reviewed_at>=approval.reviewed_at
  AND newer.review_id<>approval.review_id)
 LEFT JOIN plugin_versions v ON v.plugin_id=p.plugin_id AND (
  (p.published_version_id IS NOT NULL AND v.version_id=p.published_version_id)
  OR (p.published_version_id IS NULL AND ((`+current+` AND v.version_id=p.current_version_id) OR NOT `+current+`)))
 WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []compatibilitySnapshot
	for rows.Next() {
		var snapshot compatibilitySnapshot
		var published, saved, currentVersion, selected sql.NullString
		if err := rows.Scan(&snapshot.PluginID, &snapshot.Kind, &snapshot.Status, &snapshot.Deleted, &published, &saved, &currentVersion, &snapshot.Certain, &selected, &snapshot.Relations, &snapshot.approvalProven, &snapshot.approvalRelations); err != nil {
			return nil, err
		}
		if snapshot.Status == 1 && !snapshot.Deleted.Valid {
			if !published.Valid && saved.Valid && !currentVersion.Valid {
				return nil, ErrReferenceIndexIncomplete
			}
			if published.Valid && !selected.Valid {
				return nil, ErrReferenceIndexIncomplete
			}
		}
		snapshot.Live = !published.Valid && !saved.Valid && !selected.Valid
		snapshot.current = saved.Valid && selected.Valid && saved.String == selected.String
		out = append(out, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// A later delist alone must not resurrect superseded publications. Confirm
	// that the current saved snapshot was produced by the unique latest approval,
	// with no subsequent content write (including relation-only/Admin writes).
	// Keep all candidates if identity or graph equality cannot be proved.
	proven := map[string]compatibilitySnapshot{}
	for _, snapshot := range out {
		if !snapshot.Certain && snapshot.current && snapshot.approvalProven && equivalentApprovedRelations(snapshot.PluginID, snapshot.Relations, snapshot.approvalRelations) {
			snapshot.Certain = true
			proven[snapshot.PluginID] = snapshot
		}
	}
	kept := out[:0]
	for _, snapshot := range out {
		if selected, ok := proven[snapshot.PluginID]; ok {
			if snapshot.current {
				kept = append(kept, selected)
			}
			continue
		}
		kept = append(kept, snapshot)
	}
	return kept, nil
}

// Approval may reconcile relation IDs and timestamps. Compare only graph
// semantics, retaining pins, membership wiring, order and active/deleted state.
func equivalentApprovedRelations(source string, a, b []byte) bool {
	canonical := func(raw []byte, reviewed bool) ([]string, bool) {
		rels, err := model.DecodeSnapshotRelations(raw)
		if err != nil {
			return nil, false
		}
		out := make([]string, 0, len(rels))
		for _, rel := range rels {
			if reviewed && rel.SourcePluginID == "" {
				rel.SourcePluginID = source
			}
			if reviewed && rel.Status == 0 {
				rel.Status = 1
			}
			var data map[string]any
			if len(rel.Data) > 0 && string(rel.Data) != "null" {
				decoder := json.NewDecoder(strings.NewReader(string(rel.Data)))
				decoder.UseNumber()
				if decoder.Decode(&data) != nil {
					return nil, false
				}
			}
			if len(data) == 0 {
				data = nil
			}
			encoded, err := json.Marshal(struct {
				Source, Target, Type string
				Pin                  *string
				Order, Status        int
				Deleted              bool
				Data                 map[string]any
			}{rel.SourcePluginID, rel.TargetPluginID, rel.Type, rel.TargetVersionID, rel.SortOrder, rel.Status, rel.DeletedAt != nil, data})
			if err != nil {
				return nil, false
			}
			out = append(out, string(encoded))
		}
		sort.Strings(out)
		return out, true
	}
	left, leftOK := canonical(a, false)
	right, rightOK := canonical(b, true)
	if !leftOK || !rightOK || len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func compatibilityLiveRelations(ctx context.Context, tx *sql.Tx, source string) ([]model.PluginRelation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT target_plugin_id,relation_type,target_version_id FROM plugin_relations WHERE source_plugin_id=? AND status=1 AND deleted_at IS NULL`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PluginRelation
	for rows.Next() {
		var rel model.PluginRelation
		var pin sql.NullString
		if err := rows.Scan(&rel.TargetPluginID, &rel.Type, &pin); err != nil {
			return nil, err
		}
		rel.TargetVersionID = nullString(pin)
		out = append(out, rel)
	}
	return out, rows.Err()
}
