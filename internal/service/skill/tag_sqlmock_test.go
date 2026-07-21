package skill

import (
	"database/sql"
	"strconv"

	"github.com/DATA-DOG/go-sqlmock"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
)

func expectResolveOrCreateTagIDs(mock sqlmock.Sqlmock, spaceID, createdBy string, tags []string, ids []int64) {
	for i, tag := range tags {
		id := ids[i]
		mock.ExpectQuery("SELECT id").
			WithArgs(tag, skillrepo.GlobalTagSpaceID, spaceID, skillrepo.GlobalTagSpaceID).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectExec("INSERT INTO skill_tags").
			WithArgs(spaceID, tag, createdBy).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("SELECT id").
			WithArgs(tag, skillrepo.GlobalTagSpaceID, spaceID, skillrepo.GlobalTagSpaceID).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(id))
	}
}

func expectResolveTagNames(mock sqlmock.Sqlmock, ids []int64, names []string) {
	rows := sqlmock.NewRows([]string{"id", "name"})
	for i, id := range ids {
		rows.AddRow(id, names[i])
	}
	mock.ExpectQuery("SELECT id, name").
		WillReturnRows(rows)
}

func tagIDJSON(ids ...int64) []byte {
	out := "["
	for i, id := range ids {
		if i > 0 {
			out += ","
		}
		out += strconv.FormatInt(id, 10)
	}
	out += "]"
	return []byte(out)
}
