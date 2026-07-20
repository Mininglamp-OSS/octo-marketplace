// Package repository is the persistence boundary for MCP catalog records. It
// owns all SQL against the mcp_servers table (migrations/sql/20260714-01) and
// nothing above it constructs SQL. Callers pass a fully-resolved caller uid and
// Space id; every query is scoped explicitly (never relying on middleware
// alone, per AGENTS.md), and cross-Space rows are simply invisible to the
// query — the service turns "not visible" into a 404 (doc §4.4).
package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/go-sql-driver/mysql"
)

// ErrNotFound is returned when a lookup finds no row visible to the caller.
// The service maps it to err.marketplace.mcp.not_found.
var ErrNotFound = errors.New("mcp not found")

// ErrNameTaken is returned by Create/Rename when the (owner_uid, space_id,
// name) triple already exists in a live row. The service maps it to
// err.marketplace.mcp.name_taken.
var ErrNameTaken = errors.New("mcp name taken")

// ErrSlugTaken is returned by Create/Update when (space_id, slug) already
// exists in a live row (migration 03). The service maps it to
// err.marketplace.mcp.slug_taken.
var ErrSlugTaken = errors.New("mcp slug taken")

// Repository reads and writes MCP records.
type Repository struct {
	db *sql.DB
}

// New returns a Repository backed by the given database handle.
func New(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// ListFilter carries the resolved visibility scope plus the query params. The
// service builds it; the repository only translates it to SQL.
type ListFilter struct {
	CallerUID    string
	SpaceID      string
	Keyword      string
	Categories   []string
	Tags         []string
	Transports   []string
	Visibilities []string
	Sources      []string
	// CreatedByTypes narrows the result to rows whose created_by_type matches
	// one of the given values (mcp-v1.md §4.2). Nil/empty means no filter.
	CreatedByTypes []string
	Sort           string
	Limit          int
	Offset         int
	// MineOnly restricts the result to rows owned by CallerUID inside SpaceID
	// (GET /mcps/mine, doc §4.3). When false, the visible-set rule applies
	// (GET /mcps, doc §4.2).
	MineOnly bool
	// SystemOnly restricts the result to visibility='system' rows regardless
	// of Space. Used by the admin surface (/admin/api/v1/mcps) which lists
	// platform-provided records across all Spaces. Mutually exclusive with
	// MineOnly; when both are set SystemOnly wins.
	SystemOnly bool
}

// mysqlErrDupEntry is MySQL's duplicate-key error number (ER_DUP_ENTRY). An
// INSERT/UPDATE that violates uq_owner_space_name_live returns it; we map it to
// ErrNameTaken.
const mysqlErrDupEntry = 1062

// mapDupKey converts a MySQL duplicate-key violation on either uniqueness
// index into the corresponding sentinel error. The service maps the sentinel
// to the wire code — name_taken vs slug_taken. Uses the constraint name in
// the MySQL error message to disambiguate:
//   - uq_owner_space_name_live → ErrNameTaken (migration 02)
//   - uq_space_slug_live       → ErrSlugTaken (migration 03)
//
// Any other error passes through unchanged.
func mapDupKey(err error) error {
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) && myErr.Number == mysqlErrDupEntry {
		msg := myErr.Message
		switch {
		case strings.Contains(msg, "uq_space_slug_live"):
			return ErrSlugTaken
		case strings.Contains(msg, "uq_owner_space_name_live"):
			return ErrNameTaken
		default:
			// New unique index without matching branch → surface as name
			// taken by default (name is the older constraint). Log-worthy
			// once we add more; keep the sentinel narrow for now.
			return ErrNameTaken
		}
	}
	return err
}

// Create inserts a new record. Uniqueness of (owner_uid, space_id, name) among
// live rows is enforced by the DB UNIQUE index uq_owner_space_name_live over
// the generated name_live column (migration 20260714-02). A colliding INSERT
// fails with duplicate-key, mapped to ErrNameTaken. This is deadlock-free,
// unlike the prior SELECT ... FOR UPDATE gap-lock recipe (see migration comment
// and TestConcurrentCreateSameName).
func (r *Repository) Create(ctx context.Context, m *model.MCP) error {
	if err := insert(ctx, r.db, m); err != nil {
		return mapDupKey(err)
	}
	return nil
}

// Update applies a full row replacement of the mutable columns for an existing
// live record. A rename that collides with another live row owned by the same
// caller in the same Space violates the unique index and returns ErrNameTaken.
// The caller is expected to have already loaded the record and verified
// ownership.
func (r *Repository) Update(ctx context.Context, m *model.MCP) error {
	if err := update(ctx, r.db, m); err != nil {
		return mapDupKey(err)
	}
	return nil
}

// GetByID loads a single live record by id, ignoring visibility. The service
// applies the visibility rule (doc §4.4) after loading so it can distinguish
// owner from non-owner and choose the right error.
func (r *Repository) GetByID(ctx context.Context, id string) (*model.MCP, error) {
	const q = `SELECT ` + columns + ` FROM mcp_servers WHERE id = ? AND deleted_at IS NULL`
	row := r.db.QueryRowContext(ctx, q, id)
	m, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// SystemNameExists reports whether a live visibility=system row shares the
// given name (case-sensitive). exceptID lets Update reject renames onto a
// sibling while ignoring itself; pass "" for Create. This exists because the
// (owner_uid, space_id, name_live) UNIQUE index does NOT block duplicates for
// system rows — MySQL treats NULL space_id as distinct, so two system rows
// with the same name would slip through the DB constraint. Callers must fence
// this check with a service-level guard on the admin Create/Update path.
func (r *Repository) SystemNameExists(ctx context.Context, name, exceptID string) (bool, error) {
	const q = `SELECT 1 FROM mcp_servers WHERE visibility = 'system' AND name = ? AND deleted_at IS NULL AND id <> ? LIMIT 1`
	var one int
	err := r.db.QueryRowContext(ctx, q, name, exceptID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// SystemSlugExists is the slug twin of SystemNameExists, guarding against
// duplicate slugs among live visibility=system rows for the same reason
// (NULL space_id defeats the UNIQUE index on system rows).
func (r *Repository) SystemSlugExists(ctx context.Context, slug, exceptID string) (bool, error) {
	const q = `SELECT 1 FROM mcp_servers WHERE visibility = 'system' AND slug = ? AND deleted_at IS NULL AND id <> ? LIMIT 1`
	var one int
	err := r.db.QueryRowContext(ctx, q, slug, exceptID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// SoftDelete marks a record deleted. It is a no-op-safe update scoped to the
// owner inside their Space; ownership must be checked by the service first.
func (r *Repository) SoftDelete(ctx context.Context, id string, now time.Time) error {
	const q = `UPDATE mcp_servers SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, now, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns the page of records matching the filter plus the total count
// (before pagination) and the per-category counts over the same filtered set
// (doc §4.2). Ordering is created_at DESC.
func (r *Repository) List(ctx context.Context, f ListFilter) ([]model.MCP, int, []model.CategoryFilter, error) {
	where, args := f.buildWhere()

	total, err := r.count(ctx, where, args)
	if err != nil {
		return nil, 0, nil, err
	}

	cats, err := r.categoryCounts(ctx, where, args, total)
	if err != nil {
		return nil, 0, nil, err
	}

	pageWhere := where
	pageArgs := append([]any{}, args...)
	orderBy := "created_at DESC, id DESC"
	if f.Sort == "relevance" && strings.TrimSpace(f.Keyword) != "" {
		orderBy, args = relevanceOrder(f.Keyword)
		pageArgs = append(pageArgs, args...)
	}
	q := `SELECT ` + columns + ` FROM mcp_servers WHERE ` + pageWhere +
		` ORDER BY ` + orderBy + ` LIMIT ? OFFSET ?`
	pageArgs = append(pageArgs, f.Limit, f.Offset)

	rows, err := r.db.QueryContext(ctx, q, pageArgs...)
	if err != nil {
		return nil, 0, nil, err
	}
	defer rows.Close()

	items := make([]model.MCP, 0, f.Limit)
	for rows.Next() {
		m, scanErr := scanRow(rows)
		if scanErr != nil {
			return nil, 0, nil, scanErr
		}
		items = append(items, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, nil, err
	}
	return items, total, cats, nil
}

// relevanceOrder is the single ranking contract mirrored by service.enrichListItem.
// Every searchable field participates with the same weight and stable tie-breakers.
// JSON columns are matched case-insensitively via LOWER(CAST(...)) LIKE with a
// lowercased keyword so the SQL ranking agrees with the Go-side substring check
// (which lowercases both sides). The tools OR-group is wrapped in COALESCE so
// JSON_EXTRACT returning NULL on an empty tools_json (`'[]'` → SQL NULL) does
// not collapse the whole additive score to NULL and bury exact-name matches.
func relevanceOrder(keyword string) (string, []any) {
	like := "%" + escapeLike(strings.ToLower(strings.TrimSpace(keyword))) + "%"
	order := `((name LIKE ?) * 8 + (slogan LIKE ?) * 2 + (category LIKE ?) * 3 + ` +
		`(LOWER(CAST(tags_json AS CHAR)) LIKE ?) * 6 + ` +
		`COALESCE(LOWER(CAST(JSON_EXTRACT(tools_json, '$[*].name') AS CHAR)) LIKE ? OR ` +
		`LOWER(CAST(JSON_EXTRACT(tools_json, '$[*].description') AS CHAR)) LIKE ?, 0) * 7 + ` +
		`(LOWER(CAST(usage_examples_json AS CHAR)) LIKE ?) + (creator_name LIKE ?)) DESC, updated_at DESC, id DESC`
	return order, []any{like, like, like, like, like, like, like, like}
}

func (r *Repository) count(ctx context.Context, where string, args []any) (int, error) {
	var total int
	q := `SELECT COUNT(*) FROM mcp_servers WHERE ` + where
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

// categoryCounts groups over the same filtered set as the item page and always
// prepends the reserved {key:"all", count:total} pill (doc §4.2). Counts
// respect the keyword filter because it is part of `where`.
func (r *Repository) categoryCounts(ctx context.Context, where string, args []any, total int) ([]model.CategoryFilter, error) {
	q := `SELECT category, COUNT(*) FROM mcp_servers WHERE ` + where + ` GROUP BY category ORDER BY category`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cats := []model.CategoryFilter{{Key: model.CategoryKeyAll, Count: total}}
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return nil, err
		}
		cats = append(cats, model.CategoryFilter{Key: key, Count: count})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return cats, nil
}

// buildWhere composes the visibility-scoped predicate. It always includes
// deleted_at IS NULL. The visible-set rule (doc §4.2/§4.4) is:
//
//	system  OR  (space_id = caller_space AND (public OR owner = caller))
//
// The mine rule (doc §4.3) is owner = caller AND space_id = caller_space.
func (f ListFilter) buildWhere() (string, []any) {
	var clauses []string
	var args []any

	if f.SystemOnly {
		clauses = append(clauses, "visibility = 'system'")
	} else if f.MineOnly {
		clauses = append(clauses, "owner_uid = ? AND space_id = ?")
		args = append(args, f.CallerUID, f.SpaceID)
	} else {
		clauses = append(clauses,
			"(visibility = 'system' OR (space_id = ? AND (visibility = 'public' OR owner_uid = ?)))")
		args = append(args, f.SpaceID, f.CallerUID)
	}

	clauses = append(clauses, "deleted_at IS NULL")

	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		clauses = append(clauses, `(name LIKE ? OR slogan LIKE ? OR category LIKE ? OR `+
			`LOWER(CAST(tags_json AS CHAR)) LIKE ? OR `+
			`LOWER(CAST(JSON_EXTRACT(tools_json, '$[*].name') AS CHAR)) LIKE ? OR `+
			`LOWER(CAST(JSON_EXTRACT(tools_json, '$[*].description') AS CHAR)) LIKE ? OR `+
			`LOWER(CAST(usage_examples_json AS CHAR)) LIKE ? OR creator_name LIKE ?)`)
		like := "%" + escapeLike(strings.ToLower(kw)) + "%"
		args = append(args, like, like, like, like, like, like, like, like)
	}

	appendIn := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		marks := make([]string, len(values))
		for i, value := range values {
			marks[i] = "?"
			args = append(args, value)
		}
		clauses = append(clauses, column+" IN ("+strings.Join(marks, ",")+")")
	}
	appendIn("category", f.Categories)
	appendIn("transport", f.Transports)
	appendIn("visibility", f.Visibilities)
	appendIn("created_by_type", f.CreatedByTypes)
	if len(f.Sources) > 0 {
		parts := make([]string, 0, len(f.Sources))
		for _, source := range f.Sources {
			switch source {
			case "system":
				parts = append(parts, "visibility = 'system'")
			case "mine":
				parts = append(parts, "owner_uid = ? AND visibility <> 'system'")
				args = append(args, f.CallerUID)
			case "space":
				parts = append(parts, "visibility <> 'system' AND space_id = ? AND owner_uid <> ?")
				args = append(args, f.SpaceID, f.CallerUID)
			}
		}
		if len(parts) > 0 {
			clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
		}
	}
	if len(f.Tags) > 0 {
		parts := make([]string, 0, len(f.Tags))
		for _, tag := range f.Tags {
			parts = append(parts, "JSON_CONTAINS(tags_json, JSON_QUOTE(?))")
			args = append(args, tag)
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}

	return strings.Join(clauses, " AND "), args
}

// escapeLike neutralizes MySQL LIKE wildcards in user keywords so a literal
// substring match is performed. The default escape character is backslash.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return s
}

// execer is satisfied by both *sql.DB and *sql.Tx, so insert/update run either
// standalone or inside a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insert(ctx context.Context, ex execer, m *model.MCP) error {
	cols, err := marshalColumns(m)
	if err != nil {
		return err
	}
	const q = `INSERT INTO mcp_servers
	  (id, name, slug, slogan, category, icon, icon_version, tags_json, tools_json, usage_examples_json,
	   faqs_json, notes_json, visibility, owner_uid, space_id, creator_name,
	   created_by_type, created_by_bot_uid, created_by_bot_name,
	   transport, config_json, created_at, updated_at, deleted_at)
	  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`
	_, err = ex.ExecContext(ctx, q,
		m.ID, m.Name, m.Slug, m.Slogan, m.Category, m.Icon, m.IconVersion,
		cols.tags, cols.tools, cols.usage, cols.faqs, cols.notes,
		string(m.Visibility), m.OwnerUID, nullableString(m.SpaceID), m.CreatorName,
		string(m.CreatedByType),
		nullableString(m.CreatedByBotUID), nullableString(m.CreatedByBotName),
		string(m.Transport), cols.config, m.CreatedAt, m.UpdatedAt,
	)
	return err
}

func update(ctx context.Context, ex execer, m *model.MCP) error {
	cols, err := marshalColumns(m)
	if err != nil {
		return err
	}
	const q = `UPDATE mcp_servers SET
	  name = ?, slug = ?, slogan = ?, category = ?, icon = ?, icon_version = ?, tags_json = ?, tools_json = ?,
	  usage_examples_json = ?, faqs_json = ?, notes_json = ?, visibility = ?,
	  transport = ?, config_json = ?, updated_at = ?
	  WHERE id = ? AND deleted_at IS NULL`
	res, err := ex.ExecContext(ctx, q,
		m.Name, m.Slug, m.Slogan, m.Category, m.Icon, m.IconVersion,
		cols.tags, cols.tools, cols.usage, cols.faqs, cols.notes,
		string(m.Visibility), string(m.Transport), cols.config, m.UpdatedAt,
		m.ID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// nullableString maps the empty-string convention onto SQL NULL. Used for
// every optional VARCHAR column where "" is the caller's way of saying "not
// applicable": space_id on system rows (which are cross-Space), and the
// bot provenance triple's uid/name on human-created rows.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const columns = `id, name, slug, slogan, category, icon, icon_version, tags_json, tools_json,
	usage_examples_json, faqs_json, notes_json, visibility, owner_uid, space_id,
	creator_name, created_by_type, created_by_bot_uid, created_by_bot_name,
	transport, config_json, created_at, updated_at, deleted_at`

type marshaledColumns struct {
	tags   []byte
	tools  []byte
	usage  []byte
	faqs   []byte
	notes  []byte
	config []byte
}

func marshalColumns(m *model.MCP) (marshaledColumns, error) {
	var c marshaledColumns
	var err error
	if c.tags, err = marshalJSON(nonNilStrings(m.Tags)); err != nil {
		return c, fmt.Errorf("marshal tags: %w", err)
	}
	if c.tools, err = marshalJSON(nonNilTools(m.Tools)); err != nil {
		return c, fmt.Errorf("marshal tools: %w", err)
	}
	if c.usage, err = marshalJSON(nonNilStrings(m.UsageExamples)); err != nil {
		return c, fmt.Errorf("marshal usage examples: %w", err)
	}
	if c.faqs, err = marshalJSON(nonNilFAQs(m.FAQs)); err != nil {
		return c, fmt.Errorf("marshal faqs: %w", err)
	}
	if c.notes, err = marshalJSON(nonNilStrings(m.Notes)); err != nil {
		return c, fmt.Errorf("marshal notes: %w", err)
	}
	if c.config, err = marshalJSON(m.Connection); err != nil {
		return c, fmt.Errorf("marshal config: %w", err)
	}
	return c, nil
}

func marshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRow(s rowScanner) (*model.MCP, error) {
	var (
		m                model.MCP
		tags             []byte
		tools            []byte
		usage            []byte
		faqs             []byte
		notes            []byte
		config           []byte
		spaceID          sql.NullString
		visibility       string
		createdByType    string
		createdByBotUID  sql.NullString
		createdByBotName sql.NullString
		transport        string
		deletedAt        sql.NullTime
	)
	if err := s.Scan(
		&m.ID, &m.Name, &m.Slug, &m.Slogan, &m.Category, &m.Icon, &m.IconVersion,
		&tags, &tools, &usage, &faqs, &notes,
		&visibility, &m.OwnerUID, &spaceID, &m.CreatorName,
		&createdByType, &createdByBotUID, &createdByBotName,
		&transport, &config, &m.CreatedAt, &m.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}

	m.Visibility = model.Visibility(visibility)
	m.Transport = model.Transport(transport)
	m.CreatedByType = model.CreatedByType(createdByType)
	if createdByBotUID.Valid {
		m.CreatedByBotUID = createdByBotUID.String
	}
	if createdByBotName.Valid {
		m.CreatedByBotName = createdByBotName.String
	}
	if spaceID.Valid {
		m.SpaceID = spaceID.String
	}
	if deletedAt.Valid {
		t := deletedAt.Time
		m.DeletedAt = &t
	}

	if err := unmarshalInto(tags, &m.Tags); err != nil {
		return nil, fmt.Errorf("unmarshal tags: %w", err)
	}
	if err := unmarshalInto(tools, &m.Tools); err != nil {
		return nil, fmt.Errorf("unmarshal tools: %w", err)
	}
	if err := unmarshalInto(usage, &m.UsageExamples); err != nil {
		return nil, fmt.Errorf("unmarshal usage examples: %w", err)
	}
	if err := unmarshalInto(faqs, &m.FAQs); err != nil {
		return nil, fmt.Errorf("unmarshal faqs: %w", err)
	}
	if err := unmarshalInto(notes, &m.Notes); err != nil {
		return nil, fmt.Errorf("unmarshal notes: %w", err)
	}
	if err := unmarshalInto(config, &m.Connection); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &m, nil
}

func unmarshalInto(raw []byte, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// nonNil* helpers mirror the model package so persisted JSON columns are always
// arrays, never null, keeping reads stable.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilTools(t []model.Tool) []model.Tool {
	if t == nil {
		return []model.Tool{}
	}
	return t
}

func nonNilFAQs(f []model.FAQ) []model.FAQ {
	if f == nil {
		return []model.FAQ{}
	}
	return f
}
