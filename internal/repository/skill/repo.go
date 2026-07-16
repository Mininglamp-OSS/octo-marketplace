package skill

import (
	"database/sql"
	"errors"
)

// ErrParseTaskAlreadyConsumed indicates the parse task has already been used.
var ErrParseTaskAlreadyConsumed = errors.New("parse task already consumed")

// Repo provides data access for skills.
type Repo struct {
	db *sql.DB
}

// New creates a new skill repository.
func New(db *sql.DB) *Repo {
	return &Repo{db: db}
}
