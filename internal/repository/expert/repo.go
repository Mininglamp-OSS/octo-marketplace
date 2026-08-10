// Package expert is the persistence boundary for the Expert Marketplace: the
// experts (专家 / single agents) and expert_squads (专家团 / teams) tables plus
// the shared per-Space expert_tags dictionary. It owns all SQL for those tables
// (migration 20260806-00) and nothing above it constructs SQL. Callers pass a
// fully-resolved caller uid and Space id; every query is scoped explicitly and
// cross-Space rows are simply invisible — the service turns "not visible" into
// a 404 (doc §4.4).
package expert

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// ErrNotFound is returned when a lookup finds no live row. The service maps it
// to err.marketplace.expert.not_found.
var ErrNotFound = errors.New("expert not found")

// ErrNameTaken is returned by Create/Update when the (owner_uid, space_id,
// name) triple already exists in a live row. The service maps it to
// err.marketplace.expert.name_taken.
var ErrNameTaken = errors.New("expert name taken")

const mysqlErrDupEntry = 1062

// mapDuplicateName converts a MySQL duplicate-key violation on either entity's
// name uniqueness index into ErrNameTaken. The index names are
// uq_expert_owner_space_name_live (experts) and uq_squad_owner_space_name_live
// (expert_squads). Any other error passes through unchanged.
func mapDuplicateName(err error) error {
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) && myErr.Number == mysqlErrDupEntry &&
		(strings.Contains(myErr.Message, "uq_expert_owner_space_name_live") ||
			strings.Contains(myErr.Message, "uq_squad_owner_space_name_live")) {
		return ErrNameTaken
	}
	return err
}

// Repo provides data access for experts, squads, and the shared tag
// dictionary.
type Repo struct {
	db *sql.DB
}

// New creates a new expert repository.
func New(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// Entity selects which table the tag aggregation reads from (doc §4.13).
type Entity string

const (
	EntityExpert Entity = "expert"
	EntitySquad  Entity = "squad"
)

// nullableString maps the empty-string convention onto SQL NULL. Used for
// space_id (NULL only for system rows) and the bot provenance uid/name on
// human-created rows.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// escapeLike neutralizes MySQL LIKE wildcards in user keywords so a literal
// substring match is performed.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return s
}

func marshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

func unmarshalInto(raw []byte, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// execer is satisfied by both *sql.DB and *sql.Tx so inserts/updates can run
// standalone or inside a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}
