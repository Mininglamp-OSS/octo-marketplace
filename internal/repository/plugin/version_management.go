package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/go-sql-driver/mysql"
)

var ErrVersionBaselineUnavailable = errors.New("version history migration required")

// WithVersionManagement gates the new publication semantics during rollout.
// Both repository and service are configured at construction, before serving.
func (r *Repo) WithVersionManagement(enabled bool) *Repo { r.versionManagement = enabled; return r }

func versioned(p *model.Plugin) bool { return p.Type != model.PluginTypeConnector }

const managedVersionColumns = `v.version_id,v.plugin_id,v.version,v.declared_version,v.manifest_json,v.plugin_json,v.attachment_keys_json,v.manifest_hash,v.plugin_hash,v.relations_json,v.changelog,v.created_by,v.created_at,v.snapshot_schema_version,v.artifacts_json,
 rel.version_label,rel.released_by,rel.released_at,COALESCE(p.current_version_id=v.version_id,0),COALESCE(p.published_version_id=v.version_id,0)`

func scanManagedVersion(row interface{ Scan(...any) error }) (*model.PluginVersion, error) {
	var v model.PluginVersion
	var declared, changelog, label, by sql.NullString
	var schema sql.NullInt64
	var released sql.NullTime
	var manifest, pkg, keys, relations, artifacts []byte
	if err := row.Scan(&v.ID, &v.PluginID, &v.Version, &declared, &manifest, &pkg, &keys, &v.ManifestHash, &v.PluginHash, &relations, &changelog, &v.CreatedBy, &v.CreatedAt, &schema, &artifacts, &label, &by, &released, &v.IsCurrent, &v.IsPublished); err != nil {
		return nil, err
	}
	v.DeclaredVersion = nullString(declared)
	v.Changelog = nullString(changelog)
	v.Manifest = cloneJSON(manifest)
	v.Package = cloneJSON(pkg)
	v.AttachmentKeys = cloneJSON(keys)
	v.Relations = cloneJSON(relations)
	v.Artifacts = cloneJSON(artifacts)
	if schema.Valid {
		n := int(schema.Int64)
		v.SnapshotSchemaVersion = &n
	}
	if released.Valid {
		v.Release = &model.PluginVersionRelease{PluginID: v.PluginID, VersionID: v.ID, VersionLabel: label.String, ReleasedBy: by.String, ReleasedAt: released.Time}
	}
	return &v, nil
}

// versionAccess is evaluated in the same statement as the version read. A current
// resource's visibility never grants access to an unpublished historical draft.
func versionAccess(scope Scope) (string, []any) {
	if scope.Admin {
		return "1=1", nil
	}
	// visibilitySQL consumes (space_id, owner_uid); the appended owner/Space
	// clause consumes (owner_uid, space_id). Keep this order explicit: a swapped
	// pair would compare a Space ID against an owner UID.
	return visibilitySQL + ` AND ((p.owner_uid=? AND p.space_id=?) OR rel.version_id IS NOT NULL)`, []any{scope.SpaceID, scope.CallerUID, scope.CallerUID, scope.SpaceID}
}

func (r *Repo) GetManagedVersion(ctx context.Context, scope Scope, pluginID, versionID string) (*model.PluginVersion, error) {
	where, args := versionAccess(scope)
	args = append([]any{pluginID, versionID}, args...)
	v, err := scanManagedVersion(r.db.QueryRowContext(ctx, `SELECT `+managedVersionColumns+` FROM plugin_versions v JOIN plugins p ON p.plugin_id=v.plugin_id LEFT JOIN plugin_version_releases rel ON rel.plugin_id=v.plugin_id AND rel.version_id=v.version_id WHERE v.plugin_id=? AND v.version_id=? AND p.plugin_type<>'connector' AND p.status=1 AND p.deleted_at IS NULL AND `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

func (r *Repo) ListManagedVersions(ctx context.Context, scope Scope, pluginID string, limit, offset int) ([]model.PluginVersion, int64, error) {
	p, err := r.Get(ctx, scope, pluginID)
	if err != nil {
		return nil, 0, err
	}
	if !versioned(p) {
		return nil, 0, ErrNotFound
	}
	where, args := versionAccess(scope)
	from := ` FROM plugin_versions v JOIN plugins p ON p.plugin_id=v.plugin_id LEFT JOIN plugin_version_releases rel ON rel.plugin_id=v.plugin_id AND rel.version_id=v.version_id WHERE v.plugin_id=? AND p.status=1 AND p.deleted_at IS NULL AND ` + where
	args = append([]any{pluginID}, args...)
	var total int64
	if err = r.db.QueryRowContext(ctx, `SELECT COUNT(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	// Lists select no package/manifest bytes. Reuse the scanner with SQL NULLs.
	columns := `v.version_id,v.plugin_id,v.version,v.declared_version,NULL,NULL,NULL,v.manifest_hash,v.plugin_hash,NULL,v.changelog,v.created_by,v.created_at,v.snapshot_schema_version,NULL,rel.version_label,rel.released_by,rel.released_at,COALESCE(p.current_version_id=v.version_id,0),COALESCE(p.published_version_id=v.version_id,0)`
	rows, err := r.db.QueryContext(ctx, `SELECT `+columns+from+` ORDER BY v.created_at DESC,v.version_id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.PluginVersion{}
	for rows.Next() {
		v, err := scanManagedVersion(rows)
		if err != nil {
			return nil, 0, err
		}
		v.PluginType = p.Type
		out = append(out, *v)
	}
	return out, total, rows.Err()
}

// recordRelease runs while the resource is locked. Reusing the same immutable
// version is idempotent; a label can never acquire different content.
func (r *Repo) recordRelease(ctx context.Context, tx *sql.Tx, p *model.Plugin, versionID, label, actor string, now time.Time) error {
	if !versioned(p) {
		return nil
	}
	if err := r.validateReleaseReferences(ctx, tx, *p, versionID); err != nil {
		return err
	}
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT version_id FROM plugin_version_releases WHERE plugin_id=? AND version_label=?`, p.ID, label).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if existing != "" && existing != versionID {
		return ErrLabelTaken
	}
	if existing == "" {
		// Check owner and declared label; never attach a foreign snapshot to a release.
		var declared sql.NullString
		var raw []byte
		if err = tx.QueryRowContext(ctx, `SELECT declared_version,artifacts_json FROM plugin_versions WHERE plugin_id=? AND version_id=?`, p.ID, versionID).Scan(&declared, &raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrConflict
			}
			return err
		}
		if !declared.Valid || declared.String != label || len(raw) == 0 {
			return ErrVersionBaselineUnavailable
		}
		var artifacts []model.PluginVersionArtifact
		if err = json.Unmarshal(raw, &artifacts); err != nil {
			return err
		}
		// Monotonic publication compares releases, never an unrelated newer draft.
		var previous sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT rel.version_label FROM plugins p LEFT JOIN plugin_version_releases rel ON rel.plugin_id=p.plugin_id AND rel.version_id=p.published_version_id WHERE p.plugin_id=?`, p.ID).Scan(&previous)
		if err != nil {
			return err
		}
		if previous.Valid && !model.VersionNotRegressed(previous.String, label) {
			return ErrVersionRegressed
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO plugin_version_releases (plugin_id,version_id,version_label,released_by,released_at) VALUES (?,?,?,?,?)`, p.ID, versionID, label, actor, now)
		if err != nil {
			var me *mysql.MySQLError
			if errors.As(err, &me) && me.Number == 1062 {
				return ErrLabelTaken
			}
			return err
		}
	}
	// The market displays the selected version's manifest name, not the mutable
	// draft name. Read the name from the version being released; p.Name can be a
	// later draft (adoption/historical flows) and must not release or misreserve a
	// different display name.
	var releaseName string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(JSON_UNQUOTE(JSON_EXTRACT(manifest_json,'$.plugin_name')),'') FROM plugin_versions WHERE plugin_id=? AND version_id=?`, p.ID, versionID).Scan(&releaseName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	if strings.TrimSpace(releaseName) == "" {
		releaseName = p.Name
	}
	publishedKey, nameScope, nameErr := pluginNameColumns(releaseName, p.Type, p.SpaceID)
	if nameErr != nil {
		return nameErr
	}
	if publishedKey != nil && !p.IsEmbedded {
		if err := rejectPluginNameCollision(ctx, tx, *p, publishedKey.(string), nameScope); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE plugins SET published_version_id=?,published_name_key=?,name_scope=? WHERE plugin_id=? AND deleted_at IS NULL`, versionID, publishedKey, nameScope, p.ID)
	if err != nil {
		return wrapped("publish release", mapPluginNameTaken(err))
	}
	return nil
}

// PublishManaged publishes the explicitly selected draft. ExpectedVersionID
// prevents an intervening save from publishing content the caller did not see.
func (r *Repo) PublishManaged(ctx context.Context, scope Scope, p PublishParams) (_ *model.Plugin, err error) {
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
	if !versioned(current) || current.IsEmbedded {
		return nil, ErrNotFound
	}
	if !scope.Admin && current.Visibility != model.PluginVisibilityPrivate {
		return nil, ErrConflict
	}
	if current.CurrentVersionID == nil || *current.CurrentVersionID != p.ExpectedVersionID || current.CurrentVersion == nil {
		return nil, ErrConflict
	}
	var pending string
	err = tx.QueryRowContext(ctx, `SELECT review_id FROM plugin_review_requests WHERE plugin_id=? AND status='pending' AND deleted_at IS NULL LIMIT 1`, current.ID).Scan(&pending)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if pending != "" {
		return nil, ErrReviewPending
	}
	if err = r.recordRelease(ctx, tx, current, *current.CurrentVersionID, *current.CurrentVersion, p.OperatorID, r.now()); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE plugins SET listing_state='published',updated_at=? WHERE plugin_id=?`, r.now(), current.ID); err != nil {
		return nil, err
	}
	if err = ensureVisibleDefaultPlacement(ctx, tx, r.id, r.now(), current.ID, current.CategoryID); err != nil {
		return nil, err
	}
	m := Mutation{OperatorID: p.OperatorID, OperatorName: p.OperatorName, RequestID: p.RequestID}
	if err = insertAudit(ctx, tx, r.id(), r.now(), *current, "publish", m, current.PluginHash, current.PluginHash); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	current.ListingState = model.PluginListingStatePublished
	return current, nil
}

// AdoptParams identifies the already-published snapshot a compatibility adoption
// promotes to a formal release.
type AdoptParams struct {
	PluginID          string
	ExpectedVersionID string
	OperatorID        string
	OperatorName      string
	RequestID         string
}

// AdoptPublishedBaseline registers the snapshot of a resource that was published
// before managed releases existed as its formal release. The V1 rollout created
// immutable snapshots without release identities, so without this a listed
// resource can neither be edited (the save path refuses a missing baseline) nor
// re-published (the approved label already occupies the submission label set).
//
// This is NOT a way to publish arbitrary drafts. It requires the current pointer
// to still name the approved snapshot (size/digest inventory present, hashes
// equal to the live row) and, for a Space resource, an approved review request
// over exactly that version and content. A legacy row without that evidence stays
// blocked and belongs to the M1/M3 inventory and migration.
func (r *Repo) AdoptPublishedBaseline(ctx context.Context, scope Scope, p AdoptParams) (_ *model.Plugin, err error) {
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
	if !versioned(current) || current.IsEmbedded {
		return nil, ErrNotFound
	}
	if current.ListingState != model.PluginListingStatePublished {
		return nil, ErrConflict
	}
	if current.CurrentVersionID == nil || *current.CurrentVersionID != p.ExpectedVersionID {
		return nil, ErrConflict
	}
	var published sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT published_version_id FROM plugins WHERE plugin_id=?`, current.ID).Scan(&published); err != nil {
		return nil, err
	}
	if published.Valid {
		// A concurrent adoption or a real release already owns the baseline.
		return nil, ErrConflict
	}
	var (
		declared              sql.NullString
		artifacts             []byte
		manifestHash, pkgHash string
	)
	err = tx.QueryRowContext(ctx,
		`SELECT declared_version,artifacts_json,manifest_hash,plugin_hash
		   FROM plugin_versions WHERE plugin_id=? AND version_id=?`,
		current.ID, p.ExpectedVersionID).Scan(&declared, &artifacts, &manifestHash, &pkgHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrVersionBaselineUnavailable
	}
	if err != nil {
		return nil, err
	}
	// No frozen inventory means the file bytes were never detached from mutable
	// upload keys, and unequal hashes mean the live row is a later unapproved save.
	// Neither is evidence of what was published.
	if !declared.Valid || declared.String == "" || len(artifacts) == 0 ||
		manifestHash != current.ManifestHash || pkgHash != current.PluginHash {
		return nil, ErrVersionBaselineUnavailable
	}
	switch current.Visibility {
	case model.PluginVisibilitySpace:
		if !scope.Admin {
			var approvals int
			err = tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM plugin_review_requests
				  WHERE plugin_id=? AND space_id=? AND status='approved' AND version=? AND
				        manifest_hash=? AND plugin_hash=? AND deleted_at IS NULL`,
				current.ID, scope.SpaceID, declared.String, manifestHash, pkgHash).Scan(&approvals)
			if err != nil {
				return nil, err
			}
			if approvals == 0 {
				return nil, ErrVersionBaselineUnavailable
			}
		}
	case model.PluginVisibilitySystem, model.PluginVisibilityPublic:
		// System rows are published by a marketplace admin; there is no review
		// channel to point at, so the hash equality above is the evidence.
		if !scope.Admin {
			return nil, ErrNotFound
		}
	}
	if err = r.recordRelease(ctx, tx, current, p.ExpectedVersionID, declared.String, p.OperatorID, r.now()); err != nil {
		return nil, err
	}
	if err = ensureVisibleDefaultPlacement(ctx, tx, r.id, r.now(), current.ID, current.CategoryID); err != nil {
		return nil, err
	}
	m := Mutation{OperatorID: p.OperatorID, OperatorName: p.OperatorName, RequestID: p.RequestID}
	if err = insertAudit(ctx, tx, r.id(), r.now(), *current, "publish", m, current.PluginHash, current.PluginHash); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return current, nil
}

// publishedListProjection keeps catalog filters and displayed content on the
// same release. Editing/admin lists retain their explicit current projection.
// publishedVersionJoin attaches the formal release snapshot once. The projection
// below references published_pv instead of running a correlated subquery for
// every projected/WHERE/ORDER BY column.
const publishedVersionJoin = ` LEFT JOIN plugin_versions published_pv ON published_pv.plugin_id=p.plugin_id AND published_pv.version_id=p.published_version_id`

func publishedListProjection(query string) string {
	value := func(column, fallback string) string {
		return `COALESCE(` + column + `,` + fallback + `)`
	}
	return strings.NewReplacer(
		"p.plugin_name", value(`JSON_UNQUOTE(JSON_EXTRACT(published_pv.manifest_json,'$.plugin_name'))`, "p.plugin_name"),
		"p.tags_json", `CASE WHEN p.published_version_id IS NULL THEN p.tags_json ELSE COALESCE(CASE WHEN JSON_TYPE(JSON_EXTRACT(published_pv.manifest_json,'$.labels'))='ARRAY' THEN JSON_EXTRACT(published_pv.manifest_json,'$.labels') ELSE JSON_ARRAY() END,JSON_ARRAY()) END`,
		"p.manifest_json", value("published_pv.manifest_json", "p.manifest_json"),
		"p.manifest_hash", value("published_pv.manifest_hash", "p.manifest_hash"),
		"p.plugin_hash", value("published_pv.plugin_hash", "p.plugin_hash"),
		"p.current_version_id", `COALESCE(p.published_version_id,p.current_version_id)`,
		"p.current_version", value("published_pv.declared_version", "p.current_version"),
	).Replace(query)
}
