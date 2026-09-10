package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Mutation contains current state, replacement relations, and append-only audit metadata.
type Mutation struct {
	Plugin       model.Plugin
	Relations    []model.PluginRelation
	Placements   []model.PluginPlacement
	OperatorID   string
	OperatorName string
	RequestID    string
	Remark       *string
	// SnapshotVersion, when set, appends one immutable plugin_versions row for this
	// Plugin inside the write transaction and points current_version_id at it,
	// setting current_version to the caller-declared label, so every save
	// (create / edit / container reupload) records a full version snapshot. Only
	// top-level nodes set it; embedded children version with their container.
	// Changelog is the optional note stored on that snapshot.
	SnapshotVersion bool
	Changelog       *string
	// ResetListingToDraft un-lists the plugin as part of this save. The ONLY
	// caller is Service.update, for a published row whose declared visibility
	// CHANGED (not merely widened); see the comment at the UPDATE statement for
	// why that case cannot leave the row published. When EnforceListingGate is
	// set this flag is IGNORED and the reset is re-derived from the locked row
	// instead — the service value is a hint the locked truth overrides.
	ResetListingToDraft bool
	// RefusePendingReview aborts the update with ErrReviewPending if an open
	// review request exists on the plugin, checked under the plugin row's lock.
	// Set by Service.update for a non-admin visibility CHANGE: the service already
	// refuses that case from an UNLOCKED HasPendingReview, but a concurrent
	// SubmitReview can land between that read and this transaction, after which
	// ApproveReview would stamp visibility=space against the author's since-changed
	// intent (or, on a private target, strand the row). Re-checking under lock here
	// closes the window in the same idiom PublishPlugin uses.
	RefusePendingReview bool
	// EnforceListingGate makes Repo.Update re-derive the two listing decisions
	// from the plugin row it LOCKS rather than trusting the service's unlocked
	// read: (a) a locked (published AND space) row is refused with
	// ErrListedRequiresReview, and (b) ResetListingToDraft is recomputed as
	// "locked row is published AND the declared visibility differs from the
	// locked visibility". Set by Service.update for every non-admin caller. This
	// closes the window where an approval (or a publish) commits between the
	// service's read and this write and turns an edit that looked legal on the
	// stale value into unreviewed content on a live org row (or a gate bypass).
	EnforceListingGate bool
	// EnforceForwardOnlyVersion makes Repo.Update re-derive the version ordering
	// rule against the label on the plugin row it LOCKS. Both write surfaces check
	// it first from an UNLOCKED read — Service.update (service.go) and
	// Service.AdminUpdate via adminVersionGuard (admin.go) — and both then take a
	// FOR UPDATE lock several round trips later. Anything that advances
	// current_version in that window (an ApproveReview publishing the frozen label,
	// a concurrent save) makes the earlier comparison stale, and the UPDATE would
	// stamp a label the locked row has already moved past. Restating it here is the
	// same discipline RefusePendingReview and EnforceListingGate apply, against the
	// same lock, and it shares one implementation (model.VersionNotRegressed) with
	// the unlocked pre-checks and with InsertReviewRequest's locked re-check so the
	// three cannot drift.
	//
	// Set by both update surfaces, so the locked verdict is authoritative rather
	// than additional: it is evaluated on the same two values the pre-check used
	// (stored label vs submitted label), so a caller the pre-check exempted — no
	// stored label, an omitted version that carries the stored one forward, or a
	// legacy unorderable current — is exempt here for the same reason and not by a
	// separate rule. Container reupload and the create paths do not set it: a graph
	// rebuild mints its own labels, and a create has no prior label to regress from.
	EnforceForwardOnlyVersion bool
}

// RelationSync reports the outcome of synchronizing a Plugin's one-level
// relations to the submitted target state: an empty relation ID creates, a
// known ID with changed fields updates, and live IDs absent from the submitted
// list are soft-deleted. Relations carries the final rows with assigned IDs.
type RelationSync struct {
	Created   []string
	Updated   []string
	Deleted   []string
	Relations []model.PluginRelation
	// NewVersionID is the id of the plugin_versions row this write snapshotted and
	// advanced current_version_id to (empty when the write did not snapshot). The
	// service stamps it back onto the returned plugin so the write response's
	// current_version_id matches the DB and a subsequent GET.
	NewVersionID string
}

// Create atomically inserts current state, one-level relations, and an audit event.
func (r *Repo) Create(ctx context.Context, scope Scope, m Mutation) (*RelationSync, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := r.now()
	p := m.Plugin
	// Non-admin writes are pinned to the caller's own identity/Space; the admin
	// surface supplies owner/Space (and NULL Space for system rows) itself.
	if !scope.Admin {
		p.OwnerUID = scope.CallerUID
		p.SpaceID = &scope.SpaceID
	}
	if p.ID == "" {
		p.ID = r.id()
	}
	if err = lockPluginCategory(ctx, tx, p.CategoryID, p.Type); err != nil {
		return nil, err
	}
	// A single Create is not a container graph, so no embedded target is exempt.
	if err = lockRelationTargets(ctx, tx, scope, p.Type, m.Relations, nil); err != nil {
		return nil, err
	}
	if err = insertPlugin(ctx, tx, now, p); err != nil {
		return nil, err
	}
	created, err := insertRelations(ctx, tx, r.id, now, p.ID, m.OperatorID, m.Relations)
	if err != nil {
		return nil, err
	}
	if err = insertPlacements(ctx, tx, r.id, now, p.ID, m.Placements); err != nil {
		return nil, err
	}
	var newVersionID string
	if m.SnapshotVersion {
		if newVersionID, err = snapshotVersion(ctx, tx, r.id, now, p, m.Relations, m.Changelog, m.OperatorID); err != nil {
			return nil, err
		}
	}
	if err = insertAudit(ctx, tx, r.id(), now, p, "create", m, "", p.PluginHash); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &RelationSync{Created: created, Updated: []string{}, Deleted: []string{}, Relations: m.Relations, NewVersionID: newVersionID}, nil
}

// jsonColumn maps an optional JSON document to a nullable SQL column value: NULL
// when empty (so attachment_keys_json stays NULL for rows without spilled
// storage attachments), the raw bytes as a string otherwise.
func jsonColumn(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

// insertPlugin writes one current-state plugin row. Callers hold the
// transaction and have already locked the category and relation targets.
//
// listing_state is written explicitly rather than left to the column DEFAULT:
// RebuildGraph re-inserts a container's embedded children, and relying on a
// default there is how a draft container's children would silently acquire a
// different listing state from their parent. An unset value falls back to the
// fail-closed 'draft' rather than to whatever the schema happens to default to.
func insertPlugin(ctx context.Context, tx *sql.Tx, now interface{}, p model.Plugin) error {
	listing := p.ListingState
	if listing == "" {
		listing = model.PluginListingStateDraft
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO plugins (plugin_id,plugin_name,plugin_type,is_embedded,category_id,tags_json,publisher,owner_uid,space_id,visibility,listing_state,
creator_name,created_by_type,created_by_bot_uid,created_by_bot_name,icon,tool_count,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,current_version_id,current_version,status,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.ID, p.Name, p.Type, p.IsEmbedded, p.CategoryID, string(p.Tags), p.Publisher, p.OwnerUID, p.SpaceID, p.Visibility, listing, p.CreatorName, p.CreatedByType, p.CreatedByBotUID, p.CreatedByBotName, p.Icon, p.ToolCount, string(p.Manifest), string(p.Package), jsonColumn(p.AttachmentKeys), p.ManifestHash, p.PluginHash, p.CurrentVersionID, p.CurrentVersion, p.Status, now, now)
	if err != nil {
		return wrapped("create", err)
	}
	return nil
}

// snapshotVersion appends one immutable plugin_versions row capturing p's current
// content plus the given relation set, then points the plugin's current_version_id
// at that new snapshot and sets current_version to the caller-declared label
// (p.CurrentVersion, defaulting to "1.0.0"). The history row's own version column
// is a per-plugin auto-increment sequence (1, 2, 3 ...) — distinct from the
// current_version LABEL — computed under the transaction: for Update the plugin
// row is already FOR UPDATE-locked, and a fresh Create has no prior snapshot, so
// the COUNT is race-free. relations is marshaled as a []model.PluginRelation, and
// nil becomes an empty JSON array to satisfy the relations_json ARRAY check.
// attachment_keys_json mirrors the current row's storage-attachment sidecar.
// Callers hold the transaction.
func snapshotVersion(ctx context.Context, tx *sql.Tx, newID func() string, now interface{}, p model.Plugin, relations []model.PluginRelation, changelog *string, createdBy string) (string, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=?`, p.ID).Scan(&count); err != nil {
		return "", wrapped("count versions", err)
	}
	seq := strconv.Itoa(count + 1)
	if relations == nil {
		relations = []model.PluginRelation{}
	}
	relationJSON, err := json.Marshal(relations)
	if err != nil {
		return "", err
	}
	versionID := newID()
	if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_versions (version_id,plugin_id,version,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,relations_json,changelog,created_by,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		versionID, p.ID, seq, string(p.Manifest), string(p.Package), jsonColumn(p.AttachmentKeys), p.ManifestHash, p.PluginHash, string(relationJSON), changelog, createdBy, now); err != nil {
		return "", wrapped("snapshot version", err)
	}
	// current_version is the caller-declared label; fall back to "1.0.0" so the
	// pointer is never left NULL (the service normally sets p.CurrentVersion).
	currentVersion := "1.0.0"
	if p.CurrentVersion != nil && *p.CurrentVersion != "" {
		currentVersion = *p.CurrentVersion
	}
	if _, err := tx.ExecContext(ctx, `UPDATE plugins SET current_version_id=?,current_version=?,updated_at=? WHERE plugin_id=? AND deleted_at IS NULL`, versionID, currentVersion, now, p.ID); err != nil {
		return "", wrapped("advance current version", err)
	}
	// The new history row is now the current version; return its id so the write
	// response carries the same pointer the DB (and a subsequent GET) will show.
	return versionID, nil
}

// CreateGraph atomically inserts several current-state plugins, their one-level
// relations, and one audit event per plugin in a single transaction. It backs
// the admin container import (an expert/team plus its skills/members and the
// relations wiring them): every plugin ID must be pre-assigned by the caller so
// relations can reference in-graph targets, and every plugin/target is inserted
// before any relation is validated so an intra-graph edge resolves. A failure at
// any node rolls the whole graph back — a partial expert/squad never commits.
func (r *Repo) CreateGraph(ctx context.Context, scope Scope, nodes []Mutation) ([]*RelationSync, error) {
	if len(nodes) == 0 {
		return nil, ErrInvalidRelation
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := r.now()
	seen := make(map[string]struct{}, len(nodes))
	// Phase 1: insert every plugin row so later relation-target locks resolve
	// intra-graph edges. Non-admin graph writes are pinned to the caller identity
	// and Space; the admin surface supplies owner/Space itself.
	for i := range nodes {
		p := nodes[i].Plugin
		if !scope.Admin {
			p.OwnerUID = scope.CallerUID
			p.SpaceID = &scope.SpaceID
		}
		if p.ID == "" {
			return nil, ErrInvalidRelation
		}
		if _, dup := seen[p.ID]; dup {
			return nil, ErrConflict
		}
		seen[p.ID] = struct{}{}
		if err = lockPluginCategory(ctx, tx, p.CategoryID, p.Type); err != nil {
			return nil, err
		}
		if err = insertPlugin(ctx, tx, now, p); err != nil {
			return nil, err
		}
		nodes[i].Plugin = p
	}
	// Phase 2: validate + insert every relation now that all targets exist. Every
	// in-graph plugin ID is exempt from the embedded-target guard so a container top
	// may wire its just-created embedded children.
	inGraph := make(map[string]struct{}, len(nodes))
	for i := range nodes {
		inGraph[nodes[i].Plugin.ID] = struct{}{}
	}
	syncs := make([]*RelationSync, len(nodes))
	for i := range nodes {
		p := nodes[i].Plugin
		if err = lockRelationTargets(ctx, tx, scope, p.Type, nodes[i].Relations, inGraph); err != nil {
			return nil, err
		}
		created, err := insertRelations(ctx, tx, r.id, now, p.ID, nodes[i].OperatorID, nodes[i].Relations)
		if err != nil {
			return nil, err
		}
		if err = insertPlacements(ctx, tx, r.id, now, p.ID, nodes[i].Placements); err != nil {
			return nil, err
		}
		syncs[i] = &RelationSync{Created: created, Updated: []string{}, Deleted: []string{}, Relations: nodes[i].Relations}
	}
	// Snapshot every flagged top node now that its relations exist, so a container
	// import records the top's initial version. Embedded children are not flagged.
	for i := range nodes {
		if nodes[i].SnapshotVersion {
			if syncs[i].NewVersionID, err = snapshotVersion(ctx, tx, r.id, now, nodes[i].Plugin, nodes[i].Relations, nodes[i].Changelog, nodes[i].OperatorID); err != nil {
				return nil, err
			}
		}
	}
	// Phase 3: append one create audit per plugin.
	for i := range nodes {
		if err = insertAudit(ctx, tx, r.id(), now, nodes[i].Plugin, "create", nodes[i], "", nodes[i].Plugin.PluginHash); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return syncs, nil
}

// RebuildGraph atomically re-uploads an existing top-level container plugin
// (expert or expert_team) in place from a freshly parsed archive, preserving its
// ID and identity while swapping every embedded child. In one transaction it:
// locks and proves the top plugin exists (phase 0); inserts the new embedded
// children so later relation-target locks resolve (phase 1) and wires their own
// relations (phase 2, a squad member's expert_skill edges); updates the top
// plugin row in place — new package/manifest/hash/tags/category, but the row's
// existing owner, Space, creator, and created_at are never touched (phase 3);
// resyncs the top plugin's relations to the new children, soft-deleting the old
// top→child edges (phase 4); soft-deletes the previous children and their now
// orphaned outgoing relations, one delete audit each (phase 5); and appends one
// update audit for the top plus one create audit per new child (phase 6). A
// failure at any step rolls the whole rebuild back, so a partial expert/squad
// never commits and the old graph survives intact. It returns the top plugin's
// relation sync (the new child edges) for the caller's Detail.
//
// The previous embedded child set is derived INSIDE this transaction from the
// committed graph AFTER the top is FOR UPDATE-locked (phase 0), never from a
// caller-supplied pre-parse snapshot: two concurrent container ops on the same
// top serialize on that lock, so each derives the child set from the state the
// prior op committed. This closes the concurrent-reupload / delete-vs-reupload
// race where a stale snapshot severed the top→child edge but soft-deleted the
// already-gone old child, leaving the swapped-in child live and unreachable.
func (r *Repo) RebuildGraph(ctx context.Context, scope Scope, top Mutation, newChildren []Mutation) (_ *RelationSync, err error) {
	defer func() { err = classifyDeadlock(err) }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := r.now()
	topPlugin := top.Plugin
	// Phase 0: lock the top plugin and prove it exists under scope. The type is
	// re-checked here (the service already matched the container kind) so a rebuild
	// can never change a row's plugin_type.
	before, err := getOwnedForUpdate(ctx, tx, scope, topPlugin.ID)
	if err != nil {
		return nil, err
	}
	if topPlugin.Type != before.Type {
		return nil, ErrInvalidRelation
	}
	// Derive the previous embedded child set from the CURRENT committed graph now
	// that the top is locked and before phase 4 severs the top→child edges. Only
	// genuine is_embedded descendants are returned, so a standalone catalog target
	// the top merely references is never torn down (see collectEmbeddedChildren).
	oldChildIDs, err := collectEmbeddedChildren(ctx, tx, topPlugin.ID, before.Type)
	if err != nil {
		return nil, err
	}
	// P2-1: the service stamped the top's preserved identity and each child's
	// visibility/listing_state/Space/owner from an UNLOCKED pre-parse read.
	// Re-derive the race-sensitive fields from the row locked here (`before`) so a
	// concurrent visibility/Space/owner change — or a delist/approve — during the
	// multi-second parse cannot be silently reverted or promoted. The top's
	// owner_uid/space_id are preserved by omission from its UPDATE, and so is its
	// listing_state; visibility IS written, so re-stamp it. Every new child is
	// freshly inserted below, so re-stamp all four there: a child whose listing
	// state disagrees with the top is either independently readable (child
	// published under a draft top) or breaks every non-owner install with
	// ErrDependencyHidden (child draft under a published top). A preserved legacy
	// `public` visibility on the locked row is normalized to `system` here (the
	// single NormalizeLegacyVisibility helper) so a reupload stops persisting the
	// retired value. Residual: the top's icon and category still come from the
	// service's unlocked read (they are content, not identity), so a concurrent
	// icon/category edit can still lose to this rebuild — acceptable, as the
	// security-sensitive fields are re-derived.
	lockedVisibility := model.NormalizeLegacyVisibility(before.Visibility)
	topPlugin.Visibility = lockedVisibility
	topPlugin.SpaceID = before.SpaceID
	topPlugin.OwnerUID = before.OwnerUID
	for i := range newChildren {
		newChildren[i].Plugin.Visibility = lockedVisibility
		newChildren[i].Plugin.ListingState = before.ListingState
		newChildren[i].Plugin.SpaceID = before.SpaceID
		newChildren[i].Plugin.OwnerUID = before.OwnerUID
	}
	// Phase 1: insert every new child row so later relation-target locks resolve.
	seen := make(map[string]struct{}, len(newChildren)+1)
	seen[topPlugin.ID] = struct{}{}
	for i := range newChildren {
		p := newChildren[i].Plugin
		if !scope.Admin {
			p.OwnerUID = scope.CallerUID
			p.SpaceID = &scope.SpaceID
		}
		if p.ID == "" {
			return nil, ErrInvalidRelation
		}
		if _, dup := seen[p.ID]; dup {
			return nil, ErrConflict
		}
		seen[p.ID] = struct{}{}
		if err = lockPluginCategory(ctx, tx, p.CategoryID, p.Type); err != nil {
			return nil, err
		}
		if err = insertPlugin(ctx, tx, now, p); err != nil {
			return nil, err
		}
		newChildren[i].Plugin = p
	}
	// Phase 2: wire each new child's own relations now that all targets exist. The
	// top plus every new child is exempt from the embedded-target guard so a member
	// expert may wire its just-created embedded skills, and (phase 4) the top may
	// wire its embedded children.
	inGraph := make(map[string]struct{}, len(newChildren)+1)
	inGraph[topPlugin.ID] = struct{}{}
	for i := range newChildren {
		inGraph[newChildren[i].Plugin.ID] = struct{}{}
	}
	for i := range newChildren {
		p := newChildren[i].Plugin
		if err = lockRelationTargets(ctx, tx, scope, p.Type, newChildren[i].Relations, inGraph); err != nil {
			return nil, err
		}
		if _, err = insertRelations(ctx, tx, r.id, now, p.ID, newChildren[i].OperatorID, newChildren[i].Relations); err != nil {
			return nil, err
		}
	}
	// Phase 3: update the top plugin row in place. owner_uid, space_id,
	// creator_name, created_by_*, and created_at are intentionally absent from the
	// SET list, so the row's identity survives the rebuild; only its content
	// (package/manifest/hash/tags/category) and preserved visibility are written.
	if err = lockPluginCategory(ctx, tx, topPlugin.CategoryID, topPlugin.Type); err != nil {
		return nil, err
	}
	updWhere, updTail := `WHERE plugin_id=? AND owner_uid=? AND space_id=? AND deleted_at IS NULL`, []any{topPlugin.ID, scope.CallerUID, scope.SpaceID}
	if scope.Admin {
		updWhere, updTail = `WHERE plugin_id=? AND deleted_at IS NULL`, []any{topPlugin.ID}
	}
	updArgs := append([]any{topPlugin.Name, topPlugin.Type, topPlugin.CategoryID, string(topPlugin.Tags), topPlugin.Publisher, topPlugin.Visibility, topPlugin.Icon, topPlugin.ToolCount, string(topPlugin.Manifest), string(topPlugin.Package), jsonColumn(topPlugin.AttachmentKeys), topPlugin.ManifestHash, topPlugin.PluginHash, topPlugin.Status, now}, updTail...)
	if _, err = tx.ExecContext(ctx, `UPDATE plugins SET plugin_name=?,plugin_type=?,category_id=?,tags_json=?,publisher=?,visibility=?,icon=?,tool_count=?,manifest_json=?,plugin_json=?,attachment_keys_json=?,manifest_hash=?,plugin_hash=?,status=?,updated_at=?
`+updWhere, updArgs...); err != nil {
		return nil, wrapped("rebuild", err)
	}
	if err = syncDefaultPlacement(ctx, tx, r.id, now, topPlugin.ID, topPlugin.CategoryID); err != nil {
		return nil, err
	}
	// Phase 4: resync the top plugin's relations to the new children. Every new
	// edge carries an empty ID so syncRelations inserts it and soft-deletes the
	// leftover old top→child edges.
	if err = lockRelationTargets(ctx, tx, scope, topPlugin.Type, top.Relations, inGraph); err != nil {
		return nil, err
	}
	sync, err := syncRelations(ctx, tx, r.id, now, topPlugin.ID, top.OperatorID, top.Relations)
	if err != nil {
		return nil, err
	}
	// Phase 5: soft-delete the previous children and their outgoing relations so
	// they stop surfacing anywhere, with one delete audit each.
	for _, id := range oldChildIDs {
		if err = softDeleteRebuiltChild(ctx, tx, r.id, scope, now, id, top); err != nil {
			return nil, err
		}
	}
	// Snapshot the rebuilt top as a new history version (its content was just
	// swapped) and advance current_version_id/current_version onto it.
	if top.SnapshotVersion {
		if sync.NewVersionID, err = snapshotVersion(ctx, tx, r.id, now, topPlugin, top.Relations, top.Changelog, top.OperatorID); err != nil {
			return nil, err
		}
	}
	// Phase 6: append one update audit for the top plus one create audit per child.
	if err = insertAudit(ctx, tx, r.id(), now, topPlugin, "update", top, before.PluginHash, topPlugin.PluginHash); err != nil {
		return nil, err
	}
	for i := range newChildren {
		if err = insertAudit(ctx, tx, r.id(), now, newChildren[i].Plugin, "create", newChildren[i], "", newChildren[i].Plugin.PluginHash); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return sync, nil
}

// softDeleteRebuiltChild soft-deletes one previous embedded child and its
// outgoing relations under lock, then appends a delete audit carrying the top
// rebuild's operator identity. A child already gone (concurrent delete) is a
// no-op so the rebuild stays idempotent. It deliberately does NOT run the
// live-incoming-relation guard Delete uses: the whole subtree is being torn down
// together (a squad member and the skills only it references), and the top's
// edges to these children were just soft-deleted in phase 4, so the only
// remaining incoming edges are intra-subtree and expected.
func softDeleteRebuiltChild(ctx context.Context, tx *sql.Tx, newID func() string, scope Scope, now interface{}, id string, top Mutation) error {
	before, err := getOwnedForUpdate(ctx, tx, scope, id)
	if err != nil {
		if err == ErrNotFound {
			return nil
		}
		return err
	}
	delWhere, delTail := `WHERE plugin_id=? AND owner_uid=? AND space_id=? AND deleted_at IS NULL`, []any{now, now, id, scope.CallerUID, scope.SpaceID}
	if scope.Admin {
		delWhere, delTail = `WHERE plugin_id=? AND deleted_at IS NULL`, []any{now, now, id}
	}
	res, err := tx.ExecContext(ctx, `UPDATE plugins SET deleted_at=?,updated_at=? `+delWhere, delTail...)
	if err != nil {
		return wrapped("rebuild delete child", err)
	}
	if err = mustAffect(res); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE plugin_relations SET deleted_at=?,updated_at=?
WHERE source_plugin_id=? AND deleted_at IS NULL`, now, now, id); err != nil {
		return wrapped("rebuild delete child relations", err)
	}
	// Cascade the same way the single-row Delete does, so the invariant is
	// structural: no path that soft-deletes a plugins row leaves a pending review
	// request pointing at it. Reaching it through the SERVICE requires a row that
	// became embedded after submitting (SubmitReview refuses is_embedded), so in
	// practice this matches zero rows — that is the point. Placing the cascade at
	// the soft-delete itself rather than at the two callers also covers RebuildGraph,
	// whose replaced children are removed exactly as permanently.
	if err = cancelPendingReviewFor(ctx, tx, now, id, top.OperatorID, top.OperatorName, reasonCanceledOnDelete); err != nil {
		return err
	}
	m := Mutation{OperatorID: top.OperatorID, OperatorName: top.OperatorName, RequestID: top.RequestID, Remark: top.Remark}
	return insertAudit(ctx, tx, newID(), now, *before, "delete", m, before.PluginHash, "")
}

// Update atomically updates an owned Plugin, synchronizes its one-level
// relations to the submitted target state, and appends audit.
func (r *Repo) Update(ctx context.Context, scope Scope, m Mutation) (_ *RelationSync, err error) {
	defer func() { err = classifyDeadlock(err) }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	before, err := getOwnedForUpdate(ctx, tx, scope, m.Plugin.ID)
	if err != nil {
		return nil, err
	}
	// A visibility change while a review is pending is refused by the service, but
	// from an UNLOCKED read. Re-check under the plugin row lock this transaction now
	// holds so a SubmitReview that committed after the service's read cannot slip a
	// pending request past the guard and have ApproveReview publish against the
	// author's since-changed intent. The service passes this only for the
	// visibility-change case; a content-only save leaves it false.
	if m.RefusePendingReview {
		var pendingID string
		if err := tx.QueryRowContext(ctx,
			`SELECT review_id FROM plugin_review_requests
			  WHERE plugin_id=? AND status='pending' AND deleted_at IS NULL LIMIT 1`,
			m.Plugin.ID).Scan(&pendingID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, wrapped("check pending review", err)
		} else if pendingID != "" {
			return nil, ErrReviewPending
		}
	}
	// Re-derive the two listing decisions from the row this transaction LOCKS,
	// not from the service's earlier unlocked read. An approval or a publish can
	// commit between that read and this lock; deciding from `before` (the locked
	// row) instead of the stale value closes the window in which an edit that
	// looked legal turns into unreviewed content on a live org row, or a widening
	// that overlaps a publish lists a row that never had a review request.
	//
	//   (a) A locked (published AND space) row cannot be edited through this path
	//       at all — it must go through the review workflow. This is the same
	//       gate the service applies from its unlocked read (ErrListedRequiresReview);
	//       restating it here makes the locked row authoritative.
	//   (b) Un-list-on-widen: a published row whose declared visibility differs
	//       from the locked visibility drops to draft. Recomputed here so a
	//       publish that committed after the service's read (turning a draft row
	//       published under the lock) still un-lists on the same save.
	//
	// The service passes EnforceListingGate for every non-admin caller; the exempt
	// system admin keeps the platform-operator escape hatch and is not gated here.
	resetListing := m.ResetListingToDraft
	if m.EnforceListingGate {
		if before.ListingState == model.PluginListingStatePublished && before.Visibility == model.PluginVisibilitySpace {
			return nil, ErrListedRequiresReview
		}
		resetListing = before.ListingState == model.PluginListingStatePublished && m.Plugin.Visibility != before.Visibility
	}
	// Forward-only, restated against the LOCKED label. Both update surfaces compare
	// the submitted label against an unlocked read taken several round trips before
	// this lock; an ApproveReview that publishes a frozen label in that window (or
	// a concurrent save) advances current_version, and without this the UPDATE
	// would stamp a label the locked row has already moved past — the same staleness
	// the two guards above close for the listing decisions. The rule and its
	// grandfathering both live in model.VersionNotRegressed, shared with the
	// unlocked pre-checks and with InsertReviewRequest, so an unchanged label and a
	// legacy unorderable one behave here exactly as they do there.
	if m.EnforceForwardOnlyVersion && before.CurrentVersion != nil && m.Plugin.CurrentVersion != nil &&
		!model.VersionNotRegressed(*before.CurrentVersion, *m.Plugin.CurrentVersion) {
		return nil, ErrVersionRegressed
	}
	now := r.now()
	p := m.Plugin
	if p.Type != before.Type {
		return nil, ErrInvalidRelation
	}
	if err = lockPluginCategory(ctx, tx, p.CategoryID, p.Type); err != nil {
		return nil, err
	}
	// An Update is not a fresh container graph, but a container top (an
	// expert/team, itself is_embedded=0) legitimately owns edges to its OWN
	// embedded children (an expert's bundled skills; a squad's member experts).
	// Resubmitting those live edges on an Update must not trip the embedded-target
	// adoption guard, so exempt the targets this source ALREADY owns. Adopting a
	// FOREIGN graph's embedded child (a target not already ours) stays rejected —
	// a later container reupload would soft-delete it out from under the adopter.
	owned, err := liveRelationTargetSet(ctx, tx, p.ID)
	if err != nil {
		return nil, err
	}
	if err = lockRelationTargets(ctx, tx, scope, p.Type, m.Relations, owned); err != nil {
		return nil, err
	}
	// getOwnedForUpdate already proved existence under lock; RowsAffected reports
	// changed rows, so a byte-identical resubmit must not surface as not found.
	updWhere, updTail := `WHERE plugin_id=? AND owner_uid=? AND space_id=? AND deleted_at IS NULL`, []any{p.ID, scope.CallerUID, scope.SpaceID}
	if scope.Admin {
		updWhere, updTail = `WHERE plugin_id=? AND deleted_at IS NULL`, []any{p.ID}
	}
	// An ordinary save cannot change listing_state — EXCEPT when the caller is
	// changing the declared audience of an already-listed plugin. Writing the new
	// visibility while leaving listing_state='published' would list the plugin to
	// the whole organization with no review, which is the one thing this workflow
	// exists to prevent. Dropping back to 'draft' costs the author nothing (a
	// published PRIVATE plugin was visible only to them) and puts the row back on
	// the normal 发布 path, where the new visibility routes it through review.
	//
	// Set by Service.update, which is the only place that can compare the declared
	// visibility against the persisted one. Its condition is any CHANGE, not a
	// widening: for a tenant the only change reaching here is the widening one
	// (published+space is refused earlier), and for the exempt system admin a
	// narrowing drops the row to draft too, which is the safe direction. For a
	// non-admin caller the decision above (resetListing) overrides the service's
	// hint with the value derived from the LOCKED row.
	listingReset := ``
	if resetListing {
		listingReset = `listing_state='draft',`
	}
	updArgs := append([]any{p.Name, p.Type, p.CategoryID, string(p.Tags), p.Publisher, p.Visibility, p.Icon, p.ToolCount, string(p.Manifest), string(p.Package), jsonColumn(p.AttachmentKeys), p.ManifestHash, p.PluginHash, p.Status, now}, updTail...)
	_, err = tx.ExecContext(ctx, `UPDATE plugins SET plugin_name=?,plugin_type=?,category_id=?,tags_json=?,publisher=?,visibility=?,`+listingReset+`icon=?,tool_count=?,manifest_json=?,plugin_json=?,attachment_keys_json=?,manifest_hash=?,plugin_hash=?,status=?,updated_at=?
`+updWhere, updArgs...)
	if err != nil {
		return nil, wrapped("update", err)
	}
	// Placements filter list pages by their own category copy; keep it in sync
	// with the current-state category on every update — including clearing it to
	// NULL when the plugin's category is cleared — so the scene-scoped market
	// never keeps filtering a plugin under a category it no longer has. (With
	// publish removed, the update owns placement category configuration; the
	// plugins row's own category_id is written unconditionally just above, so the
	// placement simply tracks it.)
	if err = syncDefaultPlacement(ctx, tx, r.id, now, p.ID, p.CategoryID); err != nil {
		return nil, err
	}
	sync, err := syncRelations(ctx, tx, r.id, now, p.ID, m.OperatorID, m.Relations)
	if err != nil {
		return nil, err
	}
	// A save is a version — but a save that changes NOTHING is not. Snapshot when
	// any component a version row records differs from the row under lock: the
	// content hashes, the caller-declared label (written only inside
	// snapshotVersion — the main UPDATE above does not touch current_version), the
	// relation graph (syncRelations reports adds/updates/deletes), the attachment
	// sidecar (the managed object keys are stripped before hashing, so a same-path
	// key swap leaves the hashes equal), or a submitted changelog. Only a
	// byte-identical resubmit with no relation/sidecar change and no changelog is
	// skipped — the anti-bloat no-op — so a client cannot append an unbounded run
	// of identical version rows, while a version-, relation-, sidecar-, or
	// changelog-only save is still recorded and its label/graph/keys/changelog
	// persisted. A skipped no-op keeps its existing current_version_id (seeded
	// from the old row by the service).
	contentUnchanged := p.PluginHash == before.PluginHash && p.ManifestHash == before.ManifestHash
	labelUnchanged := (p.CurrentVersion == nil) == (before.CurrentVersion == nil) &&
		(p.CurrentVersion == nil || *p.CurrentVersion == *before.CurrentVersion)
	relationsChanged := len(sync.Created) > 0 || len(sync.Updated) > 0 || len(sync.Deleted) > 0
	sidecarUnchanged := sameAttachmentKeys(p.AttachmentKeys, before.AttachmentKeys)
	hasChangelog := m.Changelog != nil && *m.Changelog != ""
	if m.SnapshotVersion && (!contentUnchanged || !labelUnchanged || relationsChanged || !sidecarUnchanged || hasChangelog) {
		if sync.NewVersionID, err = snapshotVersion(ctx, tx, r.id, now, p, m.Relations, m.Changelog, m.OperatorID); err != nil {
			return nil, err
		}
	}
	if err = insertAudit(ctx, tx, r.id(), now, p, "update", m, before.PluginHash, p.PluginHash); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return sync, nil
}

// RatingParams carries the trusted operator attribution for a rating-only write.
type RatingParams struct {
	PluginID     string
	Rating       *int
	OperatorID   string
	OperatorName string
	RequestID    string
}

// UpdateRating atomically updates only the nullable administrator rating and
// updated_at, then appends the standard plugin audit event. Content, version
// pointers, manifests, packages, attachment keys, and hashes are never written.
func (r *Repo) UpdateRating(ctx context.Context, scope Scope, p RatingParams) (_ *model.Plugin, err error) {
	defer func() { err = classifyDeadlock(err) }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := getOwnedForUpdate(ctx, tx, scope, p.PluginID)
	if err != nil {
		return nil, err
	}
	now := r.now()
	if _, err = tx.ExecContext(ctx, `UPDATE plugins SET rating=?,updated_at=? WHERE plugin_id=? AND deleted_at IS NULL`, p.Rating, now, p.PluginID); err != nil {
		return nil, wrapped("update rating", err)
	}
	beforeRating := ratingAuditValue(current.Rating)
	current.Rating = p.Rating
	current.UpdatedAt = now
	remark := "rating:" + beforeRating + "->" + ratingAuditValue(p.Rating)
	m := Mutation{OperatorID: p.OperatorID, OperatorName: p.OperatorName, RequestID: p.RequestID, Remark: &remark}
	if err = insertAudit(ctx, tx, r.id(), now, *current, "rate", m, current.PluginHash, current.PluginHash); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return current, nil
}

func ratingAuditValue(rating *int) string {
	if rating == nil {
		return "null"
	}
	return strconv.Itoa(*rating)
}

// Delete soft-deletes an owned Plugin, invalidates its outgoing relation edges,
// and appends an audit event. A live Plugin cannot be deleted while another
// live, active Plugin has a live, active incoming relation to it.
func (r *Repo) Delete(ctx context.Context, scope Scope, pluginID, operatorID, operatorName, requestID string, remark *string) (err error) {
	defer func() { err = classifyDeadlock(err) }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	before, err := getOwnedForUpdate(ctx, tx, scope, pluginID)
	if err != nil {
		return err
	}
	// Re-derive the listed-plugin gate against the LOCKED row. The service refuses
	// published+space from an unlocked read taken several round trips earlier; an
	// ApproveReview that commits between that read and this FOR UPDATE lock can
	// promote the draft to space+published, and without this re-check the soft
	// delete below takes out a plugin the org just listed — exactly what the
	// service-level comment ("the author deliberately cannot do this") promises
	// cannot happen. This is the same locked re-derivation Repo.Update performs via
	// EnforceListingGate for edits (write.go:554-557). System admins are exempt
	// (they are the Delist actor themselves and can remove abusive listed content).
	if !scope.Admin &&
		before.ListingState == model.PluginListingStatePublished && before.Visibility == model.PluginVisibilitySpace {
		return ErrListedRequiresReview
	}
	if err = rejectLiveIncomingRelations(ctx, tx, pluginID); err != nil {
		return err
	}
	now := r.now()
	delWhere, delTail := `WHERE plugin_id=? AND owner_uid=? AND space_id=? AND deleted_at IS NULL`, []any{now, now, pluginID, scope.CallerUID, scope.SpaceID}
	if scope.Admin {
		delWhere, delTail = `WHERE plugin_id=? AND deleted_at IS NULL`, []any{now, now, pluginID}
	}
	res, err := tx.ExecContext(ctx, `UPDATE plugins SET deleted_at=?,updated_at=? `+delWhere, delTail...)
	if err != nil {
		return err
	}
	if err = mustAffect(res); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE plugin_relations SET deleted_at=?,updated_at=?
WHERE source_plugin_id=? AND deleted_at IS NULL`, now, now, pluginID)
	if err != nil {
		return err
	}
	// Cascade onto any open review request, in this transaction. Without it the
	// request stays `pending` forever and NOBODY can settle it — see
	// cancelPendingReviewFor for why every read and every decision path refuses a
	// deleted plugin's request, and why relaxing one of them instead would be dead
	// code. Same cascade DelistPlugin performs, deliberately through the same
	// helper rather than a second idiom.
	//
	// Storage objects are deliberately NOT collected, and the argument is a
	// DIFFERENT one from delist's.
	//
	// Delist keeps its hands off because the author never abandoned the submission:
	// a third party's takedown cancelled it as a side effect. That test does not
	// protect anything here. Delete is the author acting on their own content — the
	// same actor and the same submission that CancelReview destroys objects for —
	// so on who-is-acting alone this belongs on the collecting side of the line.
	//
	// What decides it instead is that this is a SOFT delete. The plugins row, its
	// whole plugin_versions history and its live attachment sidecar all survive on
	// purpose, and nothing in this service ever deletes a plugin's own objects:
	// neither Service.Delete nor the admin delete touches storage at all. So
	// collecting only the pending submission's spill would make this transaction the
	// single place a soft delete performs an irreversible external side effect — on
	// the smallest slice of the objects it is knowingly leaving behind, while the
	// larger leak it sits inside stays untouched.
	//
	// The asymmetry settles it. NOT collecting is recoverable: every row a sweeper
	// would need survives the soft delete, so the same difference (a canceled
	// request's frozen sidecar minus retainedAttachmentKeys, which reads the live
	// sidecar and every version snapshot and is not filtered by deleted_at) can
	// still be computed later, with more context. Collecting now is not —
	// object-storage deletes do not come back — and it would prejudge a question
	// nobody has answered: whether `deleted_at` on a plugin means "recoverable".
	// There is no undelete path in this repository today, but
	// migrations/sql/20260902-00-plugin-listing-state.sql deliberately fails CLOSED
	// against a manual one ("so an accidental undelete does not republish it"),
	// which is not the posture of a codebase that treats a soft-deleted plugin as
	// destroyed.
	//
	// Residual, stated plainly: the canceled submission's spilled objects leak, as
	// do the plugin's own. Reclaiming them is one deliberate decision about
	// plugin-delete object GC — live sidecar, version sidecars and any frozen
	// submission together — not a fragment of it bolted onto this cascade.
	if err = cancelPendingReviewFor(ctx, tx, now, pluginID, operatorID, operatorName, reasonCanceledOnDelete); err != nil {
		return err
	}
	m := Mutation{OperatorID: operatorID, OperatorName: operatorName, RequestID: requestID, Remark: remark}
	if err = insertAudit(ctx, tx, r.id(), now, *before, "delete", m, before.PluginHash, ""); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteGraph soft-deletes a container top (expert or expert_team) together with
// its embedded children (an expert's bundled skills; a squad's member experts and
// their skills) in ONE transaction, so deleting the top never orphans a live,
// unreachable is_embedded row. The top is locked and proved under scope, refused
// if a live plugin still holds an incoming relation to it (matching Delete); the
// embedded child set is then derived INSIDE the transaction from the committed
// graph (collectEmbeddedChildren) — never a caller-supplied pre-parse snapshot —
// so a delete racing a concurrent reupload of the same top serializes on the
// top's FOR UPDATE lock and tears down the children the prior op actually left.
// Only genuine is_embedded descendants are collected — a standalone catalog skill
// merely referenced by the top is not — then the top is soft-deleted with its
// outgoing edges and a delete audit, and each child is soft-deleted through
// softDeleteRebuiltChild, which is idempotent and carries the same operator
// identity. A failure at any step rolls the whole delete back. Connector/skill
// deletes stay on the single-row Delete.
func (r *Repo) DeleteGraph(ctx context.Context, scope Scope, topID string, operatorID, operatorName, requestID string, remark *string) (err error) {
	defer func() { err = classifyDeadlock(err) }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	before, err := getOwnedForUpdate(ctx, tx, scope, topID)
	if err != nil {
		return err
	}
	// Same locked re-derivation as Repo.Delete: a concurrent ApproveReview can
	// promote a draft container to space+published between the service's unlocked
	// read and this FOR UPDATE lock. Graph roots essentially never carry incoming
	// live relations, so rejectLiveIncomingRelations cannot protect them; the
	// listing-gate check is what prevents a published org container from vanishing
	// at its author's discretion.
	if !scope.Admin &&
		before.ListingState == model.PluginListingStatePublished && before.Visibility == model.PluginVisibilitySpace {
		return ErrListedRequiresReview
	}
	if err = rejectLiveIncomingRelations(ctx, tx, topID); err != nil {
		return err
	}
	// Derive the embedded child set under the top's lock, before the top's own
	// outgoing edges are soft-deleted below, so the expert_skill / expert_team_expert
	// relations are still live for the resolution query.
	childIDs, err := collectEmbeddedChildren(ctx, tx, topID, before.Type)
	if err != nil {
		return err
	}
	now := r.now()
	m := Mutation{OperatorID: operatorID, OperatorName: operatorName, RequestID: requestID, Remark: remark}
	delWhere, delTail := `WHERE plugin_id=? AND owner_uid=? AND space_id=? AND deleted_at IS NULL`, []any{now, now, topID, scope.CallerUID, scope.SpaceID}
	if scope.Admin {
		delWhere, delTail = `WHERE plugin_id=? AND deleted_at IS NULL`, []any{now, now, topID}
	}
	res, err := tx.ExecContext(ctx, `UPDATE plugins SET deleted_at=?,updated_at=? `+delWhere, delTail...)
	if err != nil {
		return err
	}
	if err = mustAffect(res); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE plugin_relations SET deleted_at=?,updated_at=?
WHERE source_plugin_id=? AND deleted_at IS NULL`, now, now, topID); err != nil {
		return err
	}
	// The top takes the same cascade as the single-row Delete, including its
	// deliberate refusal to collect the submission's storage objects; the argument
	// is written out there. Each embedded child gets its own inside
	// softDeleteRebuiltChild, so no plugins row this transaction soft-deletes is
	// left with a pending request pointing at it.
	if err = cancelPendingReviewFor(ctx, tx, now, topID, operatorID, operatorName, reasonCanceledOnDelete); err != nil {
		return err
	}
	if err = insertAudit(ctx, tx, r.id(), now, *before, "delete", m, before.PluginHash, ""); err != nil {
		return err
	}
	// Each embedded child is torn down together with the top; softDeleteRebuiltChild
	// is a no-op for a child already gone, so a duplicate ID (a skill shared by two
	// members) stays idempotent.
	for _, id := range childIDs {
		if err = softDeleteRebuiltChild(ctx, tx, r.id, scope, now, id, m); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// collectEmbeddedChildren derives, under the open transaction, the set of a
// locked container top's embedded child plugin IDs from the CURRENT committed
// graph. Callers invoke it AFTER getOwnedForUpdate has taken the top's FOR UPDATE
// lock, so concurrent container ops on the same top serialize on that lock and
// each resolves the child set from the state the prior op committed — never a
// pre-parse snapshot. This is what closes the concurrent-reupload /
// delete-vs-reupload orphan race.
//
// It mirrors the (now removed) service-layer collectContainerChildren semantics
// exactly:
//   - expert: the targets of the top's live expert_skill relations whose target
//     row is is_embedded=1 (a per-parent bundled skill).
//   - expert_team: the targets of the top's live expert_team_expert relations
//     whose member row is is_embedded=1, plus each such member's own live
//     expert_skill targets that are is_embedded=1.
//
// A standalone (is_embedded=0) relation target is never collected, so a shared
// catalog skill the top merely references is never soft-deleted out from under
// every other graph that shares it.
func collectEmbeddedChildren(ctx context.Context, tx *sql.Tx, topID string, topType model.PluginType) ([]string, error) {
	switch topType {
	case model.PluginTypeExpert:
		return embeddedRelationTargets(ctx, tx, topID, "expert_skill")
	case model.PluginTypeExpertTeam:
		members, err := embeddedRelationTargets(ctx, tx, topID, "expert_team_expert")
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, member := range members {
			ids = append(ids, member)
			skills, err := embeddedRelationTargets(ctx, tx, member, "expert_skill")
			if err != nil {
				return nil, err
			}
			ids = append(ids, skills...)
		}
		return ids, nil
	default:
		return nil, nil
	}
}

// embeddedRelationTargets returns the target plugin IDs of source's live
// relations of the given type whose target row is a live, embedded plugin. The
// is_embedded=1 predicate on the joined target row is what excludes standalone
// catalog targets from teardown; deleted_at IS NULL on the relation and
// status=1/deleted_at IS NULL on the target match the committed-graph semantics
// getOwnedForUpdate reads under.
func embeddedRelationTargets(ctx context.Context, tx *sql.Tx, source, relationType string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.target_plugin_id FROM plugin_relations r
JOIN plugins p ON p.plugin_id=r.target_plugin_id
WHERE r.source_plugin_id=? AND r.relation_type=? AND r.deleted_at IS NULL
AND p.is_embedded=1 AND p.status=1 AND p.deleted_at IS NULL
ORDER BY r.sort_order,r.relation_id`, source, relationType)
	if err != nil {
		return nil, wrapped("collect embedded children", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// liveRelationTargetSet returns the set of target plugin IDs the source
// currently holds live relations to. Update uses it as the embedded-target
// exemption set (see lockRelationTargets): a source may resubmit an edge to an
// embedded child it ALREADY owns, but may not adopt a foreign graph's child.
func liveRelationTargetSet(ctx context.Context, tx *sql.Tx, source string) (map[string]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id=? AND deleted_at IS NULL`, source)
	if err != nil {
		return nil, wrapped("load relation targets", err)
	}
	defer rows.Close()
	set := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		set[id] = struct{}{}
	}
	return set, rows.Err()
}

func rejectLiveIncomingRelations(ctx context.Context, tx *sql.Tx, pluginID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT r.relation_id FROM plugin_relations r
JOIN plugins source ON source.plugin_id=r.source_plugin_id
WHERE r.target_plugin_id=? AND r.status=1 AND r.deleted_at IS NULL
AND source.deleted_at IS NULL AND source.status=1
ORDER BY r.relation_id FOR UPDATE`, pluginID)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return ErrConflict
	}
	return rows.Err()
}

func lockPluginCategory(ctx context.Context, tx *sql.Tx, categoryID *string, pluginType model.PluginType) error {
	if categoryID == nil {
		return nil
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT category_id FROM plugin_categories WHERE category_id=? AND status=1 AND deleted_at IS NULL AND JSON_CONTAINS(plugin_types_json,JSON_QUOTE(?),'$') FOR UPDATE`, *categoryID, pluginType).Scan(&id)
	if err == sql.ErrNoRows {
		return ErrInvalidCategory
	}
	return err
}

func getOwnedForUpdate(ctx context.Context, tx *sql.Tx, scope Scope, id string) (*model.Plugin, error) {
	var row *sql.Row
	if scope.Admin {
		row = tx.QueryRowContext(ctx, `SELECT `+pluginColumns+` FROM plugins p WHERE p.plugin_id=? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`, id)
	} else {
		row = tx.QueryRowContext(ctx, `SELECT `+pluginColumns+` FROM plugins p WHERE p.plugin_id=? AND p.owner_uid=? AND p.space_id=? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`, id, scope.CallerUID, scope.SpaceID)
	}
	p, err := scanPlugin(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return p, err
}
func mustAffect(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// lockRelationTargets revalidates every edge at the persistence boundary in the
// mutation transaction. The visibility predicate prevents cross-scope edges,
// and FOR UPDATE prevents a target from being deleted between validation and
// insertion.
//
// inGraph is the set of plugin IDs created/rebuilt inside THIS transaction. An
// is_embedded target is a per-parent copy owned by a single container graph (a
// bundled skill / squad member); a standalone write path must never adopt one as
// a relation target, because a later container reupload soft-deletes it out from
// under the adopter (softDeleteRebuiltChild skips the incoming-relation guard).
// So an embedded target is rejected unless it is an intra-graph edge — the
// container top legitimately wiring its just-created embedded children, whose IDs
// are all in inGraph. The single Create/Update path passes nil (any embedded
// target → ErrInvalidRelation); CreateGraph/RebuildGraph pass every node ID.
func lockRelationTargets(ctx context.Context, tx *sql.Tx, scope Scope, sourceType model.PluginType, relations []model.PluginRelation, inGraph map[string]struct{}) error {
	seen := make(map[string]struct{}, len(relations))
	for _, relation := range relations {
		if relation.TargetPluginID == "" {
			return ErrInvalidRelation
		}
		key := relation.Type + "\x00" + relation.TargetPluginID
		if _, exists := seen[key]; exists {
			return ErrInvalidRelation
		}
		seen[key] = struct{}{}
		relWhere, relArgs := visibilitySQL, []any{relation.TargetPluginID, scope.SpaceID, scope.CallerUID}
		if scope.Admin {
			relWhere, relArgs = "1=1", []any{relation.TargetPluginID}
		}
		row := tx.QueryRowContext(ctx, `SELECT p.plugin_type,p.is_embedded FROM plugins p WHERE p.plugin_id=? AND p.status=1 AND p.deleted_at IS NULL AND `+relWhere+` FOR UPDATE`, relArgs...)
		var targetType model.PluginType
		var embedded bool
		if err := row.Scan(&targetType, &embedded); err != nil {
			if err == sql.ErrNoRows {
				return ErrNotFound
			}
			return err
		}
		if !validPersistedRelation(relation.Type, sourceType, targetType) {
			return ErrInvalidRelation
		}
		if embedded {
			if _, ok := inGraph[relation.TargetPluginID]; !ok {
				return ErrInvalidRelation
			}
		}
	}
	return nil
}

// validPersistedRelation mirrors the octo-plugin-lib endpoint matrix exactly
// (expert_team_expert: expert_team -> expert, expert_skill: expert -> skill,
// expert_connector: expert -> connector) as the last write-path gate.
func validPersistedRelation(relationType string, sourceType, targetType model.PluginType) bool {
	switch relationType {
	case "expert_team_expert":
		return sourceType == model.PluginTypeExpertTeam && targetType == model.PluginTypeExpert
	case "expert_skill":
		return sourceType == model.PluginTypeExpert && targetType == model.PluginTypeSkill
	case "expert_connector":
		return sourceType == model.PluginTypeExpert && targetType == model.PluginTypeConnector
	default:
		return false
	}
}

func insertRelations(ctx context.Context, tx *sql.Tx, newID func() string, now interface{}, source, creator string, rels []model.PluginRelation) ([]string, error) {
	created := make([]string, 0, len(rels))
	for i := range rels {
		if rels[i].ID == "" {
			rels[i].ID = newID()
		}
		if err := insertRelationRow(ctx, tx, now, source, creator, rels[i]); err != nil {
			return nil, err
		}
		created = append(created, rels[i].ID)
	}
	return created, nil
}

func insertRelationRow(ctx context.Context, tx *sql.Tx, now interface{}, source, creator string, x model.PluginRelation) error {
	var data any
	if len(x.Data) > 0 {
		data = string(x.Data)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO plugin_relations (relation_id,source_plugin_id,target_plugin_id,relation_type,sort_order,relation_json,status,created_by,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, x.ID, source, x.TargetPluginID, x.Type, x.SortOrder, data, x.Status, creator, now, now)
	if err != nil {
		return wrapped("replace relations", err)
	}
	return nil
}

// insertPlacements writes the current-state placements a create attaches. The
// auto-placement (tenant and admin create both pass one default visible
// placement) is intentional and must not fail on an unregistered category — a
// plain visible placement is enough to surface the plugin in the market list. A
// caller passing no placements (nil) is a no-op.
func insertPlacements(ctx context.Context, tx *sql.Tx, newID func() string, now interface{}, pluginID string, placements []model.PluginPlacement) error {
	for _, x := range placements {
		id := x.ID
		if id == "" {
			id = newID()
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO plugin_placements (placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)`, id, x.PlacementCode, pluginID, x.CategoryID, x.Visible, x.SortOrder, now, now)
		if err != nil {
			return wrapped("create placements", err)
		}
	}
	return nil
}

// syncRelations reconciles the submitted target state against the live edge set
// under row locks: empty IDs insert, known IDs update when any field differs,
// and live IDs missing from the submission are soft-deleted. A non-empty ID
// that does not match a live edge of this Plugin is rejected.
func syncRelations(ctx context.Context, tx *sql.Tx, newID func() string, now interface{}, source, creator string, desired []model.PluginRelation) (*RelationSync, error) {
	type liveEdge struct {
		target    string
		typ       string
		sortOrder int
		data      sql.NullString
		status    int
	}
	rows, err := tx.QueryContext(ctx, `SELECT relation_id,target_plugin_id,relation_type,sort_order,relation_json,status FROM plugin_relations
WHERE source_plugin_id=? AND deleted_at IS NULL ORDER BY relation_id FOR UPDATE`, source)
	if err != nil {
		return nil, wrapped("load relations", err)
	}
	live := map[string]liveEdge{}
	for rows.Next() {
		var id string
		var edge liveEdge
		if err := rows.Scan(&id, &edge.target, &edge.typ, &edge.sortOrder, &edge.data, &edge.status); err != nil {
			rows.Close()
			return nil, err
		}
		live[id] = edge
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	sync := &RelationSync{Created: []string{}, Updated: []string{}, Deleted: []string{}}
	for i := range desired {
		rel := &desired[i]
		if rel.ID == "" {
			rel.ID = newID()
			if err := insertRelationRow(ctx, tx, now, source, creator, *rel); err != nil {
				return nil, err
			}
			sync.Created = append(sync.Created, rel.ID)
			continue
		}
		edge, ok := live[rel.ID]
		if !ok {
			return nil, ErrInvalidRelation
		}
		delete(live, rel.ID)
		sameData := (len(rel.Data) == 0 && !edge.data.Valid) || (len(rel.Data) > 0 && edge.data.Valid && edge.data.String == string(rel.Data))
		if edge.target == rel.TargetPluginID && edge.typ == rel.Type && edge.sortOrder == rel.SortOrder && edge.status == rel.Status && sameData {
			continue
		}
		var data any
		if len(rel.Data) > 0 {
			data = string(rel.Data)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE plugin_relations SET target_plugin_id=?,relation_type=?,sort_order=?,relation_json=?,status=?,updated_at=?
WHERE relation_id=? AND source_plugin_id=? AND deleted_at IS NULL`, rel.TargetPluginID, rel.Type, rel.SortOrder, data, rel.Status, now, rel.ID, source); err != nil {
			return nil, wrapped("update relation", err)
		}
		sync.Updated = append(sync.Updated, rel.ID)
	}
	leftover := make([]string, 0, len(live))
	for id := range live {
		leftover = append(leftover, id)
	}
	sort.Strings(leftover)
	for _, id := range leftover {
		if _, err := tx.ExecContext(ctx, `UPDATE plugin_relations SET deleted_at=?,updated_at=? WHERE relation_id=? AND source_plugin_id=? AND deleted_at IS NULL`, now, now, id, source); err != nil {
			return nil, wrapped("delete relation", err)
		}
		sync.Deleted = append(sync.Deleted, id)
	}
	sync.Relations = desired
	return sync, nil
}
func insertAudit(ctx context.Context, tx *sql.Tx, auditID string, now interface{}, p model.Plugin, action string, m Mutation, before, after string) error {
	var bh, ah any
	if before != "" {
		bh = before
	}
	if after != "" {
		ah = after
	}
	var manifest, pkg any
	if len(p.Manifest) > 0 {
		manifest = string(p.Manifest)
	}
	if len(p.Package) > 0 {
		pkg = string(p.Package)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO plugin_audit_logs (audit_log_id,plugin_id,action,operator_id,operator_name,request_id,before_hash,after_hash,manifest_snapshot_json,plugin_snapshot_json,remark,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, auditID, p.ID, action, m.OperatorID, m.OperatorName, m.RequestID, bh, ah, manifest, pkg, m.Remark, now)
	return wrapped("append audit", err)
}

// sameAttachmentKeys reports whether two attachment sidecars hold the same
// path→object-key map, tolerant of nil / empty / JSON key ordering. The managed
// keys are stripped before the package is hashed, so a same-path key swap leaves
// plugin_hash/manifest_hash unchanged; the version-snapshot dedup consults this
// so a sidecar-only save still records a version rather than silently moving the
// live row's keys away from the snapshot current_version_id points at.
func sameAttachmentKeys(a, b json.RawMessage) bool {
	ma, mb := decodeSidecarMap(a), decodeSidecarMap(b)
	if len(ma) != len(mb) {
		return false
	}
	for k, v := range ma {
		if bv, ok := mb[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func decodeSidecarMap(raw json.RawMessage) map[string]string {
	m := map[string]string{}
	if len(raw) == 0 {
		return m
	}
	_ = json.Unmarshal(raw, &m)
	return m
}

// defaultPlacementCode is the one placement code the unified plugin surface
// manages. Every market list INNER JOINs it on `visible=1`; publish-era
// multi-scene placements are gone.
const defaultPlacementCode = "default"

// syncDefaultPlacement keeps a plugin listable across a save. Market list pages
// INNER JOIN the "default" placement, and after publish-removal no non-create
// path inserts one — so a plugin created before auto-placement (e.g. an old
// tenant create that never published) would be permanently filtered out on its
// next edit. Insert the default placement when the plugin has none (self-healing,
// carrying the current category), otherwise sync the existing placement's
// category to the current one (including clearing it to NULL). The id is consumed
// only on the insert path. Only the default placement is managed; publish-era
// multi-scene placements are gone.
func syncDefaultPlacement(ctx context.Context, tx *sql.Tx, newID func() string, now interface{}, pluginID string, categoryID *string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plugin_placements WHERE plugin_id=? AND placement_code=?)`, pluginID, defaultPlacementCode).Scan(&exists); err != nil {
		return wrapped("check placement", err)
	}
	if !exists {
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_placements (placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)`,
			newID(), defaultPlacementCode, pluginID, categoryID, true, 0, now, now); err != nil {
			return wrapped("insert default placement", err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE plugin_placements SET category_id=?,updated_at=? WHERE plugin_id=?`, categoryID, now, pluginID); err != nil {
		return wrapped("update placements", err)
	}
	return nil
}

// ensureVisibleDefaultPlacement makes the plugin's default placement exist and be
// visible. Approval is the moment a plugin becomes org-readable, and the market
// list applies TWO independent filters: the visibility predicate AND an INNER
// JOIN on a `visible=1` default placement. Flipping only `plugins.visibility`
// therefore lists nothing for a row whose placement is missing (pre-auto-placement
// legacy) or hidden (publish-era `visible=0`) — and the author cannot repair it,
// because a listed plugin's ordinary write path is 409 `listed_requires_review`.
// So approve self-heals the placement inside the same transaction as the status
// and visibility swap.
//
// It is deliberately idempotent: an already-visible placement is left untouched
// (no `updated_at` churn), a hidden one is flipped, and a missing one is
// inserted. The insert carries `ON DUPLICATE KEY UPDATE visible=1` because the
// unique key is (placement_code, plugin_id, category_key) — a concurrent writer
// that inserted first must not turn approval into a duplicate-key error.
//
// Only `visible` is forced. Category stays whatever the placement already
// carries; syncDefaultPlacement owns category reconciliation on the write path.
func ensureVisibleDefaultPlacement(ctx context.Context, tx *sql.Tx, newID func() string, now interface{}, pluginID string, categoryID *string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM plugin_placements WHERE plugin_id=? AND placement_code=?)`,
		pluginID, defaultPlacementCode).Scan(&exists); err != nil {
		return wrapped("check default placement", err)
	}
	if exists {
		if _, err := tx.ExecContext(ctx,
			`UPDATE plugin_placements SET visible=1,updated_at=? WHERE plugin_id=? AND placement_code=? AND visible=0`,
			now, pluginID, defaultPlacementCode); err != nil {
			return wrapped("reveal default placement", err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO plugin_placements (placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at)
		 VALUES (?,?,?,?,?,?,?,?)
		 ON DUPLICATE KEY UPDATE visible=1,updated_at=VALUES(updated_at)`,
		newID(), defaultPlacementCode, pluginID, categoryID, true, 0, now, now); err != nil {
		return wrapped("insert default placement", err)
	}
	return nil
}
