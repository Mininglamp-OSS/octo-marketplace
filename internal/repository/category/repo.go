package category

import "database/sql"

// Repo provides data access for categories.
type Repo struct {
	db *sql.DB
}

// New creates a new category repository.
func New(db *sql.DB) *Repo {
	return &Repo{db: db}
}
