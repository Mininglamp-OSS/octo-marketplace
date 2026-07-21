package skill

import (
	"errors"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestMapDuplicateName(t *testing.T) {
	duplicateName := &mysql.MySQLError{
		Number:  mysqlErrDupEntry,
		Message: "Duplicate entry 'owner-space-name' for key 'skills.uq_skill_owner_space_name_live'",
	}
	if !errors.Is(mapDuplicateName(duplicateName), ErrNameTaken) {
		t.Fatal("skill name constraint violation must map to ErrNameTaken")
	}

	duplicateID := &mysql.MySQLError{
		Number:  mysqlErrDupEntry,
		Message: "Duplicate entry 'id' for key 'skills.PRIMARY'",
	}
	if errors.Is(mapDuplicateName(duplicateID), ErrNameTaken) {
		t.Fatal("unrelated unique constraint must not map to ErrNameTaken")
	}
}
