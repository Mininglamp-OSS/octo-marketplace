package migration

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestCloneRowReplacesIdentityAndClearsVersionPointers(t *testing.T) {
	insertErr := errors.New("clone insert failed")
	for _, tc := range []struct {
		name    string
		count   int64
		execErr error
		wantErr error
	}{
		{name: "cloned", count: 1},
		{name: "missing source", count: 0, wantErr: errApply},
		{name: "insert failure", execErr: insertErr, wantErr: insertErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			// Interleave metadata and special columns to catch substitutions or
			// reordering that would copy the source identity or version pointers.
			mock.ExpectQuery(`SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='plugins' AND EXTRA NOT LIKE '%GENERATED%' ORDER BY ORDINAL_POSITION`).
				WillReturnRows(sqlmock.NewRows([]string{"COLUMN_NAME"}).
					AddRow("plugin_name").
					AddRow("plugin_id").
					AddRow("current_version_id").
					AddRow("owner_uid").
					AddRow("published_version_id").
					AddRow("current_version").
					AddRow("created_at")).RowsWillBeClosed()
			insert := mock.ExpectExec("INSERT INTO plugins (`plugin_name`,`plugin_id`,`current_version_id`,`owner_uid`,`published_version_id`,`current_version`,`created_at`) SELECT `plugin_name`,?,NULL,`owner_uid`,NULL,NULL,`created_at` FROM plugins WHERE plugin_id=?").
				WithArgs("clone-id", "source-id")
			if tc.execErr != nil {
				insert.WillReturnError(tc.execErr)
			} else {
				insert.WillReturnResult(sqlmock.NewResult(0, tc.count))
			}
			mock.ExpectRollback()

			err = cloneRow(context.Background(), tx, Node{SourceID: "source-id", Plugin: model.Plugin{ID: "clone-id"}})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("cloneRow error = %v, want %v", err, tc.wantErr)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
