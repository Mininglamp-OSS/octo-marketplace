package plugin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/go-sql-driver/mysql"
)

// PlacementListFilter controls the cross-Space admin placement table.
type PlacementListFilter struct {
	SceneCode string
	Type      model.PluginType
	Keyword   string
	Limit     int
	Offset    int
}

// PlacementBatchItem supplies the server-generated relationship ID for one
// Plugin when a batch operation needs to create a missing placement.
type PlacementBatchItem struct {
	PlacementID string
	PluginID    string
	InputIndex  int
}

// BatchItemError identifies the original request item that caused a business
// failure after items were reordered for deterministic locking.
type BatchItemError struct {
	Index int
	Err   error
}

func (e *BatchItemError) Error() string { return fmt.Sprintf("batch item %d: %v", e.Index, e.Err) }
func (e *BatchItemError) Unwrap() error { return e.Err }

// ListScenes returns every configured scene and live usage counts.
func (r *Repo) ListScenes(ctx context.Context) ([]model.PluginScene, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT s.scene_id,s.scene_code,s.name,s.description,s.sort_order,s.created_at,s.updated_at,
 COUNT(DISTINCT CASE WHEN p.status=1 AND p.deleted_at IS NULL AND p.is_embedded=0 THEN pp.plugin_id END) AS plugin_count,
 COUNT(DISTINCT cp.category_id) AS category_count
FROM plugin_scenes s
LEFT JOIN plugin_placements pp ON pp.placement_code=s.scene_code
LEFT JOIN plugins p ON p.plugin_id=pp.plugin_id
LEFT JOIN plugin_category_placements cp ON cp.placement_code=s.scene_code AND cp.visible=1
GROUP BY s.scene_id,s.scene_code,s.name,s.description,s.sort_order,s.created_at,s.updated_at
ORDER BY s.sort_order,s.scene_code`)
	if err != nil {
		return nil, wrapped("list plugin scenes", err)
	}
	defer rows.Close()
	var out []model.PluginScene
	for rows.Next() {
		var scene model.PluginScene
		if err := rows.Scan(&scene.ID, &scene.Code, &scene.Name, &scene.Description, &scene.SortOrder, &scene.CreatedAt, &scene.UpdatedAt, &scene.PluginCount, &scene.CategoryCount); err != nil {
			return nil, err
		}
		out = append(out, scene)
	}
	return out, rows.Err()
}

func (r *Repo) GetScene(ctx context.Context, sceneID string) (*model.PluginScene, error) {
	var scene model.PluginScene
	err := r.db.QueryRowContext(ctx, `SELECT scene_id,scene_code,name,description,sort_order,created_at,updated_at
FROM plugin_scenes WHERE scene_id=?`, sceneID).Scan(&scene.ID, &scene.Code, &scene.Name, &scene.Description, &scene.SortOrder, &scene.CreatedAt, &scene.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, wrapped("get plugin scene", err)
	}
	return &scene, nil
}

func (r *Repo) CreateScene(ctx context.Context, scene model.PluginScene) error {
	now := r.now()
	_, err := r.db.ExecContext(ctx, `INSERT INTO plugin_scenes(scene_id,scene_code,name,description,sort_order,created_at,updated_at)
VALUES(?,?,?,?,?,?,?)`, scene.ID, scene.Code, scene.Name, scene.Description, scene.SortOrder, now, now)
	if isDuplicateKey(err) {
		return ErrConflict
	}
	if err != nil {
		return wrapped("create plugin scene", err)
	}
	return nil
}

func (r *Repo) UpdateScene(ctx context.Context, scene model.PluginScene) error {
	res, err := r.db.ExecContext(ctx, `UPDATE plugin_scenes SET name=?,description=?,sort_order=?,updated_at=? WHERE scene_id=?`, scene.Name, scene.Description, scene.SortOrder, r.now(), scene.ID)
	if err != nil {
		return wrapped("update plugin scene", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return wrapped("update plugin scene", err)
	}
	if n == 0 {
		// MySQL reports zero affected rows for a no-op update. Distinguish it from
		// a missing scene without making callers manufacture a different payload.
		if _, getErr := r.GetScene(ctx, scene.ID); getErr != nil {
			return getErr
		}
	}
	return nil
}

func (r *Repo) DeleteScene(ctx context.Context, sceneID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var code string
	err = tx.QueryRowContext(ctx, `SELECT scene_code FROM plugin_scenes WHERE scene_id=? FOR UPDATE`, sceneID).Scan(&code)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return wrapped("lock plugin scene", err)
	}
	if code == "default" {
		return ErrConflict
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM plugin_placements WHERE placement_code=?) +
 (SELECT COUNT(*) FROM plugin_category_placements WHERE placement_code=?)`, code, code).Scan(&count); err != nil {
		return wrapped("count plugin scene references", err)
	}
	if count > 0 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_scenes WHERE scene_id=?`, sceneID); err != nil {
		return wrapped("delete plugin scene", err)
	}
	return tx.Commit()
}

// ListPlacements returns Plugin-to-scene relationships for the admin console.
func (r *Repo) ListPlacements(ctx context.Context, f PlacementListFilter) ([]model.AdminPluginPlacement, int64, error) {
	from := ` FROM plugin_placements pp
JOIN plugin_scenes s ON s.scene_code=pp.placement_code
JOIN plugins p ON p.plugin_id=pp.plugin_id AND p.status=1 AND p.deleted_at IS NULL AND p.is_embedded=0
LEFT JOIN plugin_categories c ON c.category_id=pp.category_id AND c.status=1 AND c.deleted_at IS NULL`
	where := ` WHERE 1=1`
	args := []any{}
	if f.SceneCode != "" {
		where += ` AND pp.placement_code=?`
		args = append(args, f.SceneCode)
	}
	if f.Type != "" {
		where += ` AND p.plugin_type=?`
		args = append(args, f.Type)
	}
	if f.Keyword != "" {
		where += ` AND p.plugin_name LIKE ? ESCAPE '!'`
		args = append(args, "%"+escapeLike(f.Keyword)+"%")
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*)`+from+where, args...).Scan(&total); err != nil {
		return nil, 0, wrapped("count plugin placements", err)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	queryArgs := append(append([]any{}, args...), limit, max(f.Offset, 0))
	rows, err := r.db.QueryContext(ctx, `SELECT pp.placement_id,s.scene_id,s.scene_code,s.name,
p.plugin_id,p.plugin_name,p.plugin_type,pp.category_id,c.name,pp.visible,pp.sort_order,pp.created_at,pp.updated_at`+
		from+where+` ORDER BY s.sort_order,pp.sort_order,p.plugin_name,p.plugin_id LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, wrapped("list plugin placements", err)
	}
	defer rows.Close()
	var out []model.AdminPluginPlacement
	for rows.Next() {
		var item model.AdminPluginPlacement
		if err := rows.Scan(&item.ID, &item.SceneID, &item.SceneCode, &item.SceneName, &item.PluginID, &item.PluginName, &item.PluginType, &item.CategoryID, &item.CategoryName, &item.IsVisible, &item.SortOrder, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, item)
	}
	return out, total, rows.Err()
}

func (r *Repo) GetPlacement(ctx context.Context, placementID string) (*model.AdminPluginPlacement, error) {
	var item model.AdminPluginPlacement
	err := r.db.QueryRowContext(ctx, `SELECT pp.placement_id,s.scene_id,s.scene_code,s.name,
p.plugin_id,p.plugin_name,p.plugin_type,pp.category_id,c.name,pp.visible,pp.sort_order,pp.created_at,pp.updated_at
FROM plugin_placements pp
JOIN plugin_scenes s ON s.scene_code=pp.placement_code
JOIN plugins p ON p.plugin_id=pp.plugin_id AND p.status=1 AND p.deleted_at IS NULL AND p.is_embedded=0
LEFT JOIN plugin_categories c ON c.category_id=pp.category_id AND c.status=1 AND c.deleted_at IS NULL
WHERE pp.placement_id=?`, placementID).Scan(&item.ID, &item.SceneID, &item.SceneCode, &item.SceneName, &item.PluginID, &item.PluginName, &item.PluginType, &item.CategoryID, &item.CategoryName, &item.IsVisible, &item.SortOrder, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, wrapped("get plugin placement", err)
	}
	return &item, nil
}

// CreatePlacement locks the Plugin so concurrent placement creates for the same
// Plugin serialize, validates the scene, and inherits the Plugin's category.
func (r *Repo) CreatePlacement(ctx context.Context, placementID, sceneCode, pluginID string, visible bool, sortOrder int) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var lockedSceneCode string
	if err := tx.QueryRowContext(ctx, `SELECT scene_code FROM plugin_scenes WHERE scene_code=? FOR UPDATE`, sceneCode).Scan(&lockedSceneCode); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return wrapped("check plugin scene", err)
	}
	var categoryID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT category_id FROM plugins WHERE plugin_id=? AND status=1 AND deleted_at IS NULL AND is_embedded=0 FOR UPDATE`, pluginID).Scan(&categoryID); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return wrapped("lock plugin for placement", err)
	}
	var duplicate bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plugin_placements WHERE plugin_id=? AND placement_code=?)`, pluginID, sceneCode).Scan(&duplicate); err != nil {
		return wrapped("check plugin placement", err)
	}
	if duplicate {
		return ErrConflict
	}
	var category any
	if categoryID.Valid {
		category = categoryID.String
	}
	now := r.now()
	_, err = tx.ExecContext(ctx, `INSERT INTO plugin_placements(placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?)`, placementID, sceneCode, pluginID, category, visible, sortOrder, now, now)
	if isDuplicateKey(err) {
		return ErrConflict
	}
	if err != nil {
		return wrapped("create plugin placement", err)
	}
	return tx.Commit()
}

func (r *Repo) UpdatePlacement(ctx context.Context, placementID string, visible bool, sortOrder int) error {
	var sceneCode string
	err := r.db.QueryRowContext(ctx, `SELECT placement_code FROM plugin_placements WHERE placement_id=?`, placementID).Scan(&sceneCode)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return wrapped("get plugin placement", err)
	}
	if sceneCode == "default" && !visible {
		return ErrConflict
	}
	res, err := r.db.ExecContext(ctx, `UPDATE plugin_placements SET visible=?,sort_order=?,updated_at=? WHERE placement_id=?`, visible, sortOrder, r.now(), placementID)
	if err != nil {
		return wrapped("update plugin placement", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return wrapped("update plugin placement", err)
	}
	if n == 0 {
		if _, getErr := r.GetPlacement(ctx, placementID); getErr != nil {
			return getErr
		}
	}
	return nil
}

func (r *Repo) DeletePlacement(ctx context.Context, placementID string) error {
	var sceneCode string
	err := r.db.QueryRowContext(ctx, `SELECT placement_code FROM plugin_placements WHERE placement_id=?`, placementID).Scan(&sceneCode)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return wrapped("get plugin placement", err)
	}
	if sceneCode == "default" {
		return ErrConflict
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM plugin_placements WHERE placement_id=?`, placementID)
	if err != nil {
		return wrapped("delete plugin placement", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return wrapped("delete plugin placement", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// BatchSetPlacements atomically adds, updates, or removes one scene placement
// for every supplied Plugin. The scene is always locked; add/update operations
// also lock Plugin rows before changing their placements.
func (r *Repo) BatchSetPlacements(ctx context.Context, sceneCode string, items []PlacementBatchItem, isPlaced, isVisible bool, sortOrder int) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var lockedSceneCode string
	if err := tx.QueryRowContext(ctx, `SELECT scene_code FROM plugin_scenes WHERE scene_code=? FOR UPDATE`, sceneCode).Scan(&lockedSceneCode); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return wrapped("lock plugin scene", err)
	}
	if !isPlaced && sceneCode == "default" {
		return ErrConflict
	}

	for _, item := range items {
		if !isPlaced {
			if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_placements WHERE placement_code=? AND plugin_id=?`, sceneCode, item.PluginID); err != nil {
				return wrapped("remove plugin placement", err)
			}
			continue
		}

		var categoryID sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT category_id FROM plugins WHERE plugin_id=? AND status=1 AND deleted_at IS NULL AND is_embedded=0 FOR UPDATE`, item.PluginID).Scan(&categoryID); errors.Is(err, sql.ErrNoRows) {
			return &BatchItemError{Index: item.InputIndex, Err: ErrNotFound}
		} else if err != nil {
			return wrapped("lock plugin for placement", err)
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plugin_placements WHERE plugin_id=? AND placement_code=?)`, item.PluginID, sceneCode).Scan(&exists); err != nil {
			return wrapped("check plugin placement", err)
		}
		var category any
		if categoryID.Valid {
			category = categoryID.String
		}
		now := r.now()
		if exists {
			if _, err := tx.ExecContext(ctx, `UPDATE plugin_placements SET category_id=?,visible=?,sort_order=?,updated_at=? WHERE placement_code=? AND plugin_id=?`, category, isVisible, sortOrder, now, sceneCode, item.PluginID); err != nil {
				return wrapped("update plugin placement", err)
			}
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO plugin_placements(placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?)`, item.PlacementID, sceneCode, item.PluginID, category, isVisible, sortOrder, now, now)
		if isDuplicateKey(err) {
			return &BatchItemError{Index: item.InputIndex, Err: ErrConflict}
		}
		if err != nil {
			return wrapped("create plugin placement", err)
		}
	}
	return tx.Commit()
}

func isDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
