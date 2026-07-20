package category

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// ErrCategoryNameTaken indicates that a category with the same name already exists.
var ErrCategoryNameTaken = errors.New("category name taken")

const mysqlErrDupEntry = 1062

func mapCategoryDuplicateName(err error) error {
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) && myErr.Number == mysqlErrDupEntry &&
		(strings.Contains(myErr.Message, "uk_categories_name") ||
			strings.Contains(myErr.Message, "uk_categories_name_live")) {
		return ErrCategoryNameTaken
	}
	return err
}

// Repo provides data access for categories.
type Repo struct {
	db *sql.DB
}

// New creates a new category repository.
func New(db *sql.DB) *Repo {
	return &Repo{db: db}
}
