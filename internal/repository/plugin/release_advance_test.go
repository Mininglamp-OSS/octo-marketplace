package plugin

import (
	"context"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"testing"
	"time"
)

func TestNewReleaseRejectsNumericallyEqualLabelUnderLock(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT relations_json FROM plugin_versions`).WithArgs("p", "new-version").WillReturnRows(sqlmock.NewRows([]string{"relations_json"}).AddRow([]byte(`[]`)))
	mock.ExpectQuery(`SELECT version_id FROM plugin_version_releases`).WithArgs("p", "1.00.3").WillReturnRows(sqlmock.NewRows([]string{"version_id"}))
	mock.ExpectQuery(`SELECT declared_version,artifacts_json`).WithArgs("p", "new-version").WillReturnRows(sqlmock.NewRows([]string{"declared_version", "artifacts_json"}).AddRow("1.00.3", []byte(`[]`)))
	mock.ExpectQuery(`SELECT rel.version_label FROM plugins`).WithArgs("p").WillReturnRows(sqlmock.NewRows([]string{"version_label"}).AddRow("1.0.3"))
	mock.ExpectRollback()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	p := model.Plugin{ID: "p", Type: model.PluginTypeSkill}
	err = New(db).WithVersionManagement(true).recordRelease(context.Background(), tx, &p, "new-version", "1.00.3", "owner", time.Now())
	if !errors.Is(err, ErrVersionRegressed) {
		t.Fatalf("err=%v", err)
	}
	_ = tx.Rollback()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
