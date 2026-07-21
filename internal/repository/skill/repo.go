package skill

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// ErrParseTaskAlreadyConsumed indicates the parse task has already been used.
var ErrParseTaskAlreadyConsumed = errors.New("parse task already consumed")

// ErrNameTaken indicates that an owner already has a skill with the same name
// in the same Space.
var ErrNameTaken = errors.New("skill name taken")

// ErrSkillNotFound indicates the target live Skill row was not found.
var ErrSkillNotFound = errors.New("skill not found")

const mysqlErrDupEntry = 1062

func mapDuplicateName(err error) error {
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) && myErr.Number == mysqlErrDupEntry &&
		strings.Contains(myErr.Message, "uq_skill_owner_space_name") {
		return ErrNameTaken
	}
	return err
}

// Repo provides data access for skills.
type Repo struct {
	db *sql.DB
}

// New creates a new skill repository.
func New(db *sql.DB) *Repo {
	return &Repo{db: db}
}
