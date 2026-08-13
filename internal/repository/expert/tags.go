package expert

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// GlobalTagSpaceID is the shared tag bucket used by platform (system) rows. The
// expert_tags.space_id column is NOT NULL, so an empty string is used instead
// of SQL NULL — the same convention as skill_tags.
const GlobalTagSpaceID = ""

// TagListFilter drives the aggregated tag suggestions (doc §4.13). Entity
// selects the table to aggregate over; the visibility scope mirrors the list
// endpoints.
type TagListFilter struct {
	Entity    Entity
	CallerUID string
	SpaceID   string
	Query     string
	Limit     int
	MineOnly  bool
}

// ListTags aggregates tag names from records visible to the caller in the
// current Space, ordered by descending row count with alphabetical tie-break
// (doc §4.13). Tags are stored on each row as a JSON array of ids referencing
// expert_tags; this query unnests the ids, joins the dictionary for names, and
// groups. Entity selects experts vs expert_squads.
func (r *Repo) ListTags(ctx context.Context, f TagListFilter) ([]model.TagFilter, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	table := "experts"
	if f.Entity == EntitySquad {
		table = "expert_squads"
	}

	var visibilityClause string
	var args []any
	if f.MineOnly {
		visibilityClause = "e.owner_uid = ? AND e.space_id = ?"
		args = append(args, f.CallerUID, f.SpaceID)
	} else {
		visibilityClause = "e.visibility = 'system' OR (e.space_id = ? AND (e.visibility = 'public' OR e.owner_uid = ?))"
		args = append(args, f.SpaceID, f.CallerUID)
	}

	kwClause := ""
	if kw := strings.TrimSpace(f.Query); kw != "" {
		kwClause = " AND et.name LIKE ?"
		args = append(args, "%"+escapeLike(kw)+"%")
	}
	args = append(args, limit)

	q := `SELECT et.name, COUNT(*) AS cnt
		FROM ` + table + ` e
		JOIN JSON_TABLE(
			IFNULL(e.tags, JSON_ARRAY()),
			'$[*]' COLUMNS (tag_id BIGINT PATH '$')
		) AS jt
		JOIN expert_tags et ON et.id = jt.tag_id
		WHERE e.deleted_at IS NULL
		  AND (` + visibilityClause + `)
		  AND et.name <> ''` + kwClause + `
		GROUP BY et.name
		ORDER BY cnt DESC, et.name ASC
		LIMIT ?`

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list expert tags: %w", err)
	}
	defer rows.Close()

	tags := make([]model.TagFilter, 0, limit)
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return nil, err
		}
		tags = append(tags, model.TagFilter{Name: name, Count: count})
	}
	return tags, rows.Err()
}

// tagExec is the subset of DB/Tx needed by the dictionary helpers.
type tagExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// resolveOrCreateTagIDs upserts each normalized name into expert_tags for the
// caller's Space and returns the resulting ids, de-duplicated and order-
// preserving.
func resolveOrCreateTagIDs(ctx context.Context, ex tagExec, spaceID, createdBy string, tags []string) ([]int64, error) {
	ids := make([]int64, 0, len(tags))
	seen := make(map[int64]struct{}, len(tags))
	for _, tag := range normalizeTags(tags) {
		id, err := resolveTagID(ctx, ex, spaceID, tag)
		if err != nil {
			return nil, err
		}
		if id == 0 {
			id, err = insertTag(ctx, ex, spaceID, createdBy, tag)
			if err != nil {
				return nil, err
			}
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

// resolveTagID returns the id of a tag name visible to the Space (Space-local
// preferred over global), or 0 when absent.
func resolveTagID(ctx context.Context, ex tagExec, spaceID, tag string) (int64, error) {
	var id int64
	err := ex.QueryRowContext(ctx, `
		SELECT id
		FROM expert_tags
		WHERE name = ? AND space_id IN (?, ?)
		ORDER BY CASE WHEN space_id = ? THEN 0 ELSE 1 END
		LIMIT 1
	`, tag, GlobalTagSpaceID, spaceID, spaceID).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// insertTag inserts a new dictionary entry (or touches an existing one) and
// returns its id. expert_tags carries no column defaults, so the timestamps
// are stamped here.
func insertTag(ctx context.Context, ex tagExec, spaceID, createdBy, tag string) (int64, error) {
	now := time.Now().UTC()
	if _, err := ex.ExecContext(ctx, `
		INSERT INTO expert_tags (space_id, name, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE updated_at = VALUES(updated_at)
	`, spaceID, tag, createdBy, now, now); err != nil {
		return 0, err
	}
	return resolveTagID(ctx, ex, spaceID, tag)
}

// tagIDsToRaw marshals a tag-id slice to a JSON array. nil stays nil (no
// change); empty renders as [].
func tagIDsToRaw(ids []int64) (json.RawMessage, error) {
	if ids == nil {
		return nil, nil
	}
	if len(ids) == 0 {
		return json.RawMessage(`[]`), nil
	}
	out, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// ResolveTagNames maps a set of tag ids back to their names.
func (r *Repo) ResolveTagNames(ctx context.Context, ids []int64) (map[int64]string, error) {
	names := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return names, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name
		FROM expert_tags
		WHERE id IN (`+strings.Join(placeholders, ",")+`)
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("resolve tag names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

// ResolveFilterTagIDs resolves each filter tag NAME to the id(s) visible in the
// Space. Returns one id-group per matched name (order preserved); unmatched
// names are dropped. The service AND-combines the groups.
func (r *Repo) ResolveFilterTagIDs(ctx context.Context, spaceID string, tags []string) ([][]int64, error) {
	groups := make([][]int64, 0, len(tags))
	for _, tag := range normalizeTags(tags) {
		conditions := "name = ?"
		args := []any{tag}
		if spaceID != GlobalTagSpaceID {
			conditions += " AND space_id IN (?, ?)"
			args = append(args, GlobalTagSpaceID, spaceID)
		}
		args = append(args, spaceID)
		rows, err := r.db.QueryContext(ctx, `
			SELECT id
			FROM expert_tags
			WHERE `+conditions+`
			ORDER BY CASE WHEN space_id = ? THEN 0 ELSE 1 END
		`, args...)
		if err != nil {
			return nil, err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		if len(ids) > 0 {
			groups = append(groups, ids)
		}
	}
	return groups, nil
}

// normalizeTags trims, drops empties, and de-duplicates preserving order.
func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}
