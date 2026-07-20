package category

import (
	"errors"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestMapCategoryDuplicateNameLiveIndex(t *testing.T) {
	err := &mysql.MySQLError{
		Number:  mysqlErrDupEntry,
		Message: "Duplicate entry 'Developer' for key 'categories.uk_categories_name_live'",
	}
	if got := mapCategoryDuplicateName(err); !errors.Is(got, ErrCategoryNameTaken) {
		t.Fatalf("got %v, want ErrCategoryNameTaken", got)
	}
}

func TestMapCategoryDuplicateNameLegacyIndex(t *testing.T) {
	err := &mysql.MySQLError{
		Number:  mysqlErrDupEntry,
		Message: "Duplicate entry 'Developer' for key 'categories.uk_categories_name'",
	}
	if got := mapCategoryDuplicateName(err); !errors.Is(got, ErrCategoryNameTaken) {
		t.Fatalf("got %v, want ErrCategoryNameTaken", got)
	}
}
