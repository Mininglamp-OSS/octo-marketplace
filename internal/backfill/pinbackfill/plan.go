// Package pinbackfill performs the one-time P-model cutover mutation that fills
// missing target_version_id pins in already-released Expert/Team snapshots.
//
// This package is deliberately NOT a read-time dependency resolver. Read paths
// (ResolvePinnedGraph and the install coordinator) must continue to fail closed
// on an unpinned released snapshot. The only way an existing released snapshot
// gains a pin is this explicit, offline-authorized tool.
//
// The write performed here is an approved exception to the normal "published
// snapshots are immutable" rule: it changes only target_version_id values that
// are NULL/blank, and it mirrors the same pins into the immutable reverse
// reference index. It never creates a plugin_version, never allocates or moves a
// release, never changes plugins.published_version_id/current_version_id, and
// never writes a pin on an expert_connector edge.
package pinbackfill

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/jcs"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

const (
	// PlanSchemaVersion is the only plan document this tool accepts.
	PlanSchemaVersion = "1.0"

	// PlanSourceScope is SHA-256(JCS({"kind":"pin-backfill","schema_version":"1.0"})).
	// A deployment's trust issuer must allow this scope before it can sign a
	// pin-backfill authorization. Preview always emits this fixed cutover scope;
	// apply additionally accepts a deliberately different signed scope so a
	// deployment can run the same verifier under its own namespace.
	PlanSourceScope = "cccd8e95cfccb2504030b41a17b305033ff51c0971a86998ca89b316f08d03ab"

	// PlanSourceIdentity names the one operation this plan authorizes. The plan
	// digest is the per-execution binding; the identity is stable so an operator
	// can grant exactly the pin-backfill scope without granting generic stock
	// migration authority.
	PlanSourceIdentity = "pin-backfill@v1"

	// PlanBatchID is the fixed audit batch label for this cutover. The signed
	// authorization and the plan must agree on it.
	PlanBatchID = "pin-backfill-v1"
)

const (
	maxSources     = 100000
	maxSourceEdges = 4000
)

// Resolution explains why this pin is proposed. It is part of the signed plan
// so apply can distinguish a newly resolved head from an existing pin it must
// preserve.
const (
	// ResolutionCurrentRelease means both the snapshot and the reverse index were
	// missing a pin; preview resolved the target's current published release.
	ResolutionCurrentRelease = "current_release"
	// ResolutionSnapshotPin means the snapshot already carried the pin and only
	// the reverse-reference row needs repair. The pin must not be moved.
	ResolutionSnapshotPin = "snapshot_pin"
	// ResolutionReferencePin means the reverse index already carried the pin and
	// only the snapshot needs repair. The pin must not be moved.
	ResolutionReferencePin = "reference_pin"
)

var (
	// ErrLimit reports a bounded read that would exceed its explicit cap.
	ErrLimit = errors.New("pin backfill evidence exceeds limit")
	// ErrRead reports a consistent read that failed.
	ErrRead = errors.New("pin backfill read failed")
	// ErrInvalidPlan reports a structurally invalid or unsupported plan.
	ErrInvalidPlan = errors.New("pin backfill plan invalid")
	// ErrPlanStale reports a plan or source that changed after preview.
	ErrPlanStale = errors.New("pin backfill plan stale")
	// ErrAuthorization reports an absent, expired, tampered, or mismatched
	// offline authorization artifact.
	ErrAuthorization = errors.New("pin backfill authorization invalid")
	// ErrWrite reports a failed per-source transaction.
	ErrWrite = errors.New("pin backfill write failed")
)

// Plan is the deterministic, signed unit of pin-backfill work. It contains no
// timestamps, random IDs, or environment values, so the same database/source
// state always produces the same JCS digest.
type Plan struct {
	// PlanDigest is the self-describing copy of the signed digest. Digest()
	// ignores this field (a document cannot hash itself), and apply separately
	// compares it with SHA-256(JCS(plan without plan_digest)). It is emitted so a
	// preview file records both the plan and the exact value apply must receive.
	PlanDigest     string          `json:"plan_digest,omitempty"`
	SchemaVersion  string          `json:"schema_version"`
	SourceScope    string          `json:"source_scope"`
	SourceIdentity string          `json:"source_identity"`
	BatchID        string          `json:"batch_id"`
	Sources        []SourcePlan    `json:"sources"`
	Blocked        []BlockedSource `json:"blocked"`
}

// SourcePlan is one released source version that will be changed. Sources with
// unresolved targets are omitted from Sources and reported under Blocked.
type SourcePlan struct {
	PluginID   string                 `json:"plugin_id"`
	VersionID  string                 `json:"version_id"`
	Source     string                 `json:"source"`
	PluginType model.PluginType       `json:"plugin_type"`
	OwnerUID   string                 `json:"owner_uid"`
	SpaceID    *string                `json:"space_id"`
	Visibility model.PluginVisibility `json:"visibility"`
	// PublishedVersionID and EditRevision bind apply to the source plugin state
	// that existed at preview. Publishing a newer version changes this pointer
	// and refuses the whole plan (plan_stale) even though older released
	// snapshots remain physically immutable.
	PublishedVersionID     *string          `json:"published_version_id"`
	EditRevision           uint64           `json:"edit_revision"`
	SnapshotDigest         string           `json:"snapshot_digest"`
	ExpectedSnapshotDigest string           `json:"expected_snapshot_digest"`
	Relations              []RelationChange `json:"relations"`
}

// RelationChange names the exact pins this plan supplies. Only Skill/Expert
// edges ever appear here.
type RelationChange struct {
	RelationID      string `json:"relation_id"`
	RelationType    string `json:"relation_type"`
	TargetPluginID  string `json:"target_plugin_id"`
	TargetVersionID string `json:"target_version_id"`
	Resolution      string `json:"resolution"`
}

// BlockedSource is a released source version that cannot be safely backfilled.
// It is never applied, even when it also contains resolvable edges.
type BlockedSource struct {
	PluginID  string          `json:"plugin_id"`
	VersionID string          `json:"version_id"`
	Source    string          `json:"source"`
	Reasons   []BlockedReason `json:"reasons"`
}

// BlockedReason is a stable, non-secret reason code. Target IDs are included so
// an operator can investigate without querying the database for hidden data.
type BlockedReason struct {
	RelationID     string `json:"relation_id,omitempty"`
	TargetPluginID string `json:"target_plugin_id,omitempty"`
	Code           string `json:"code"`
}

type referenceRow struct {
	SourcePluginID  string
	RelationID      string
	RelationType    string
	TargetPluginID  string
	TargetVersionID *string
}

type targetLookup struct {
	Plugin    *model.Plugin
	Published *string
}

// Read builds a deterministic plan from a consistent, read-only view. It never
// writes, locks a business row, or resolves a pin lazily at read time.
func Read(ctx context.Context, db *sql.DB, limit int) (Plan, error) {
	if limit < 1 || limit > maxSources {
		return Plan{}, ErrLimit
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Plan{}, ErrRead
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT p.plugin_id,p.plugin_type,p.owner_uid,p.space_id,p.visibility,
		p.published_version_id,p.edit_revision,v.version_id,v.relations_json
		FROM plugins p
		JOIN plugin_versions v ON v.plugin_id=p.plugin_id
		JOIN plugin_version_releases rel ON rel.plugin_id=v.plugin_id AND rel.version_id=v.version_id
		WHERE p.plugin_type IN ('expert','expert_team')
		  AND p.status=1 AND p.deleted_at IS NULL
		  AND p.listing_state='published' AND p.is_embedded=0
		ORDER BY p.plugin_id,v.version_id LIMIT ?`, limit+1)
	if err != nil {
		return Plan{}, ErrRead
	}
	type sourceRow struct {
		Plugin    model.Plugin
		VersionID string
		Relations json.RawMessage
	}
	var sources []sourceRow
	for rows.Next() {
		var (
			row       sourceRow
			sid       sql.NullString
			published sql.NullString
		)
		if err := rows.Scan(&row.Plugin.ID, &row.Plugin.Type, &row.Plugin.OwnerUID, &sid,
			&row.Plugin.Visibility, &published, &row.Plugin.EditRevision, &row.VersionID, &row.Relations); err != nil {
			rows.Close()
			return Plan{}, ErrRead
		}
		row.Plugin.SpaceID = nullStringPtr(sid)
		row.Plugin.ListingState = model.PluginListingStatePublished
		row.Plugin.PublishedVersionID = nullStringPtr(published)
		sources = append(sources, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Plan{}, ErrRead
	}
	if len(sources) > limit {
		return Plan{}, ErrLimit
	}

	plan := Plan{
		SchemaVersion:  PlanSchemaVersion,
		SourceScope:    PlanSourceScope,
		SourceIdentity: PlanSourceIdentity,
		BatchID:        PlanBatchID,
		Sources:        []SourcePlan{},
		Blocked:        []BlockedSource{},
	}
	targets := map[string]*targetLookup{}
	for _, source := range sources {
		refs, err := readReferences(ctx, tx, source.Plugin.ID, source.VersionID)
		if err != nil {
			return Plan{}, err
		}
		sourcePlan, blocked, ok, err := buildSourcePlan(ctx, tx, source.Plugin, source.VersionID, source.Relations, refs, targets)
		if err != nil {
			return Plan{}, err
		}
		if blocked != nil {
			plan.Blocked = append(plan.Blocked, *blocked)
			continue
		}
		if !ok {
			continue
		}
		plan.Sources = append(plan.Sources, sourcePlan)
	}
	sort.Slice(plan.Sources, func(i, j int) bool {
		if plan.Sources[i].PluginID != plan.Sources[j].PluginID {
			return plan.Sources[i].PluginID < plan.Sources[j].PluginID
		}
		return plan.Sources[i].VersionID < plan.Sources[j].VersionID
	})
	sort.Slice(plan.Blocked, func(i, j int) bool {
		if plan.Blocked[i].PluginID != plan.Blocked[j].PluginID {
			return plan.Blocked[i].PluginID < plan.Blocked[j].PluginID
		}
		return plan.Blocked[i].VersionID < plan.Blocked[j].VersionID
	})
	digest, err := Digest(plan)
	if err != nil {
		return Plan{}, ErrRead
	}
	plan.PlanDigest = digest
	return plan, nil
}

func buildSourcePlan(ctx context.Context, tx *sql.Tx, source model.Plugin, versionID string, raw json.RawMessage, refs map[string]referenceRow, targets map[string]*targetLookup) (SourcePlan, *BlockedSource, bool, error) {
	rels, err := model.DecodeSnapshotRelations(raw)
	if err != nil {
		return SourcePlan{}, blocked(source, versionID, []BlockedReason{{Code: "invalid_snapshot"}}), false, nil
	}
	if len(rels) > maxSourceEdges {
		return SourcePlan{}, blocked(source, versionID, []BlockedReason{{Code: "snapshot_too_large"}}), false, nil
	}
	seen := map[string]bool{}
	var reasons []BlockedReason
	var changes []RelationChange
	pins := map[string]string{}
	for i := range rels {
		rel := &rels[i]
		if rel.ID == "" || rel.TargetPluginID == "" || rel.TargetPluginID == source.ID || seen[rel.ID] {
			reasons = append(reasons, BlockedReason{RelationID: rel.ID, Code: "invalid_relation"})
			continue
		}
		seen[rel.ID] = true
		if rel.Status != 1 || rel.DeletedAt != nil {
			// A released snapshot should only carry live composition edges. This
			// tool only fills pins; it must not rewrite historical status/audit
			// fields, and a graph containing one cannot resolve under the P model.
			reasons = append(reasons, BlockedReason{RelationID: rel.ID, Code: "inactive_relation"})
			continue
		}
		ref, hasRef := refs[rel.ID]
		if hasRef && (ref.SourcePluginID != source.ID || ref.RelationType != rel.Type || ref.TargetPluginID != rel.TargetPluginID) {
			reasons = append(reasons, BlockedReason{RelationID: rel.ID, TargetPluginID: rel.TargetPluginID, Code: "index_conflict"})
			continue
		}
		switch rel.Type {
		case "expert_connector":
			// Connectors are ID + effective resources and never carry a version.
			// This tool leaves them exactly as stored; a stored version (in either
			// the snapshot or the reverse index) is a corrupt snapshot and must be
			// investigated, not silently cleared.
			if rel.TargetVersionID != nil {
				reasons = append(reasons, BlockedReason{RelationID: rel.ID, TargetPluginID: rel.TargetPluginID, Code: "connector_version_forbidden"})
			}
			if hasRef && trimPin(ref.TargetVersionID) != "" {
				reasons = append(reasons, BlockedReason{RelationID: rel.ID, TargetPluginID: rel.TargetPluginID, Code: "connector_version_forbidden"})
			}
			continue
		case "expert_skill", "expert_team_expert":
		default:
			reasons = append(reasons, BlockedReason{RelationID: rel.ID, TargetPluginID: rel.TargetPluginID, Code: "invalid_relation_type"})
			continue
		}
		snapshotPin := trimPin(rel.TargetVersionID)
		referencePin := ""
		if hasRef {
			referencePin = trimPin(ref.TargetVersionID)
		}
		if snapshotPin != "" && referencePin != "" && snapshotPin != referencePin {
			reasons = append(reasons, BlockedReason{RelationID: rel.ID, TargetPluginID: rel.TargetPluginID, Code: "index_conflict"})
			continue
		}
		switch {
		case snapshotPin != "":
			// A missing reverse-index ROW is not itself a missing pin; repairing
			// index coverage belongs to the reference-index backfill. Only an
			// existing blank row is in this tool's scope, because the requirement
			// explicitly names blank plugin_version_relation_refs pins and a valid
			// snapshot pin must be left untouched.
			if hasRef && referencePin == "" {
				changes = append(changes, RelationChange{RelationID: rel.ID, RelationType: rel.Type, TargetPluginID: rel.TargetPluginID, TargetVersionID: snapshotPin, Resolution: ResolutionSnapshotPin})
				pins[rel.ID] = snapshotPin
			}
		case referencePin != "":
			changes = append(changes, RelationChange{RelationID: rel.ID, RelationType: rel.Type, TargetPluginID: rel.TargetPluginID, TargetVersionID: referencePin, Resolution: ResolutionReferencePin})
			pins[rel.ID] = referencePin
		default:
			target, reason, err := resolveTarget(ctx, tx, source, rel, targets)
			if err != nil {
				return SourcePlan{}, nil, false, err
			}
			if reason != "" {
				reasons = append(reasons, BlockedReason{RelationID: rel.ID, TargetPluginID: rel.TargetPluginID, Code: reason})
				continue
			}
			changes = append(changes, RelationChange{RelationID: rel.ID, RelationType: rel.Type, TargetPluginID: rel.TargetPluginID, TargetVersionID: target, Resolution: ResolutionCurrentRelease})
			pins[rel.ID] = target
		}
	}
	for relationID := range refs {
		if !seen[relationID] {
			reasons = append(reasons, BlockedReason{RelationID: relationID, Code: "index_conflict"})
		}
	}
	if len(reasons) > 0 {
		return SourcePlan{}, blocked(source, versionID, reasons), false, nil
	}
	if len(changes) == 0 {
		return SourcePlan{}, nil, false, nil
	}
	currentDigest, err := snapshotDigest(raw)
	if err != nil {
		return SourcePlan{}, blocked(source, versionID, []BlockedReason{{Code: "invalid_snapshot"}}), false, nil
	}
	expectedRaw, err := encodeSnapshotWithPins(raw, rels, pins)
	if err != nil {
		return SourcePlan{}, blocked(source, versionID, []BlockedReason{{Code: "invalid_snapshot"}}), false, nil
	}
	expectedDigest, err := snapshotDigest(expectedRaw)
	if err != nil {
		return SourcePlan{}, blocked(source, versionID, []BlockedReason{{Code: "invalid_snapshot"}}), false, nil
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].RelationID < changes[j].RelationID })
	return SourcePlan{
		PluginID:               source.ID,
		VersionID:              versionID,
		Source:                 source.ID + "@" + versionID,
		PluginType:             source.Type,
		OwnerUID:               source.OwnerUID,
		SpaceID:                cloneString(source.SpaceID),
		Visibility:             source.Visibility,
		PublishedVersionID:     cloneString(source.PublishedVersionID),
		EditRevision:           source.EditRevision,
		SnapshotDigest:         currentDigest,
		ExpectedSnapshotDigest: expectedDigest,
		Relations:              changes,
	}, nil, true, nil
}

func blocked(source model.Plugin, versionID string, reasons []BlockedReason) *BlockedSource {
	return &BlockedSource{
		PluginID:  source.ID,
		VersionID: versionID,
		Source:    source.ID + "@" + versionID,
		Reasons:   normalizeReasons(reasons),
	}
}

func normalizeReasons(in []BlockedReason) []BlockedReason {
	seen := map[BlockedReason]bool{}
	out := make([]BlockedReason, 0, len(in))
	for _, reason := range in {
		if seen[reason] {
			continue
		}
		seen[reason] = true
		out = append(out, reason)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RelationID != out[j].RelationID {
			return out[i].RelationID < out[j].RelationID
		}
		if out[i].TargetPluginID != out[j].TargetPluginID {
			return out[i].TargetPluginID < out[j].TargetPluginID
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func readReferences(ctx context.Context, tx *sql.Tx, sourcePluginID, sourceVersionID string) (map[string]referenceRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT source_plugin_id,relation_id,relation_type,target_plugin_id,target_version_id
		FROM plugin_version_relation_refs WHERE source_version_id=? ORDER BY relation_id`, sourceVersionID)
	if err != nil {
		return nil, ErrRead
	}
	defer rows.Close()
	out := map[string]referenceRow{}
	for rows.Next() {
		var (
			ref referenceRow
			pin sql.NullString
		)
		ref.SourcePluginID = sourcePluginID
		if err := rows.Scan(&ref.SourcePluginID, &ref.RelationID, &ref.RelationType, &ref.TargetPluginID, &pin); err != nil {
			return nil, ErrRead
		}
		ref.TargetVersionID = nullStringPtr(pin)
		out[ref.RelationID] = ref
	}
	if err := rows.Err(); err != nil {
		return nil, ErrRead
	}
	return out, nil
}

// resolveTarget validates the target row and its current formal release. It
// takes no lock because preview runs inside a read-only repeatable-read
// snapshot; apply repeats every check under FOR UPDATE before writing.
func resolveTarget(ctx context.Context, tx *sql.Tx, source model.Plugin, rel *model.PluginRelation, cache map[string]*targetLookup) (string, string, error) {
	lookup, ok := cache[rel.TargetPluginID]
	if !ok {
		var err error
		lookup, err = loadTarget(ctx, tx, rel.TargetPluginID)
		if err != nil {
			return "", "", err
		}
		cache[rel.TargetPluginID] = lookup
	}
	if lookup == nil || lookup.Plugin == nil {
		return "", "target_not_found", nil
	}
	target := lookup.Plugin
	if target.IsEmbedded {
		return "", "target_embedded", nil
	}
	if !validRelationKind(source.Type, rel.Type, target.Type) {
		return "", "target_type_mismatch", nil
	}
	if target.ListingState != model.PluginListingStatePublished {
		return "", "target_not_published", nil
	}
	if !audienceAllows(source, *target) {
		return "", "target_audience_mismatch", nil
	}
	if lookup.Published == nil || strings.TrimSpace(*lookup.Published) == "" {
		return "", "target_unreleased", nil
	}
	pin := strings.TrimSpace(*lookup.Published)
	var found string
	err := tx.QueryRowContext(ctx, `SELECT version_id FROM plugin_version_releases WHERE plugin_id=? AND version_id=?`, target.ID, pin).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "target_release_missing", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("%w: read target release", ErrRead)
	}
	return pin, "", nil
}

func loadTarget(ctx context.Context, tx *sql.Tx, targetID string) (*targetLookup, error) {
	var (
		p         model.Plugin
		sid       sql.NullString
		published sql.NullString
		status    int
		deleted   sql.NullTime
	)
	err := tx.QueryRowContext(ctx, `SELECT plugin_id,plugin_type,is_embedded,owner_uid,space_id,visibility,listing_state,published_version_id,status,deleted_at
		FROM plugins WHERE plugin_id=?`, targetID).Scan(&p.ID, &p.Type, &p.IsEmbedded, &p.OwnerUID, &sid, &p.Visibility, &p.ListingState, &published, &status, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read target", ErrRead)
	}
	if status != 1 || deleted.Valid {
		return nil, nil
	}
	p.SpaceID = nullStringPtr(sid)
	return &targetLookup{Plugin: &p, Published: nullStringPtr(published)}, nil
}

func validRelationKind(source model.PluginType, relation string, target model.PluginType) bool {
	switch relation {
	case "expert_skill":
		return source == model.PluginTypeExpert && target == model.PluginTypeSkill
	case "expert_team_expert":
		return source == model.PluginTypeExpertTeam && target == model.PluginTypeExpert
	case "expert_connector":
		return source == model.PluginTypeExpert && target == model.PluginTypeConnector
	default:
		return false
	}
}

// audienceAllows is the canonical reference-audience rule (see
// repository/plugin.referenceAudience). It is repeated here because this
// package must not depend on repository write internals, and the rule is small
// enough to pin with tests.
func audienceAllows(source, target model.Plugin) bool {
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

func referenceVisibility(v model.PluginVisibility) model.PluginVisibility {
	if v == model.PluginVisibilityPublic {
		return model.PluginVisibilitySystem
	}
	return v
}

// Digest returns lowercase SHA-256(JCS(plan)). The self-describing PlanDigest
// field is excluded, so a preview file can carry its own digest without making
// the digest self-referential. The plan contains no volatile fields, so
// preview -> apply -> re-preview is reproducible.
func Digest(plan Plan) (string, error) {
	plan.PlanDigest = ""
	return jcs.Digest(plan)
}

// snapshotDigest canonicalizes the snapshot document itself. JCS removes MySQL
// whitespace/key-order normalization and preserves every stored field, including
// legacy Go field names, so apply can detect any change beyond the planned pins.
func snapshotDigest(raw json.RawMessage) (string, error) {
	return jcs.DigestJSON(raw)
}

// encodeSnapshotWithPins rewrites only the target_version_id value on the
// selected rows. It preserves legacy field spellings, unknown fields, ordering
// of the relation array, and all non-pin evidence.
func encodeSnapshotWithPins(raw json.RawMessage, rels []model.PluginRelation, pins map[string]string) (json.RawMessage, error) {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	if len(rows) != len(rels) {
		return nil, fmt.Errorf("snapshot shape mismatch")
	}
	if len(pins) == 0 {
		return raw, nil
	}
	for i := range rels {
		pin, ok := pins[rels[i].ID]
		if !ok {
			continue
		}
		value, err := json.Marshal(pin)
		if err != nil {
			return nil, err
		}
		row := rows[i]
		changed := false
		for _, key := range []string{"target_version_id", "TargetVersionID"} {
			if _, exists := row[key]; exists {
				row[key] = value
				changed = true
			}
		}
		if !changed {
			row["target_version_id"] = value
		}
	}
	return json.Marshal(rows)
}

func indexDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func trimPin(pin *string) string {
	if pin == nil {
		return ""
	}
	return strings.TrimSpace(*pin)
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	out := value.String
	return &out
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}
