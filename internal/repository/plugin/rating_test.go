package plugin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/go-sql-driver/mysql"
)

func TestUpdateRatingChangesOnlyRatingAndAudits(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 4, 1, 2, 3, 0, time.UTC)
	r := New(db)
	r.now = func() time.Time { return now }
	r.id = func() string { return "audit-rating" }
	rating := 5

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .*p\.rating.* FROM plugins p WHERE p\.plugin_id=\? .* FOR UPDATE`).
		WithArgs("plugin-1").
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("plugin-1", "Plugin", model.PluginTypeSkill, 0, nil, []byte(`[]`), "pub", "owner", "space", model.PluginVisibilitySpace, model.PluginListingStatePublished, "Creator", "human", nil, nil, "icon", 0, 2, []byte(`{"m":1}`), []byte(`{"p":1}`), nil, "sha256:m", "sha256:p", "version-id", "1.0.0", 1, now.Add(-time.Hour), now.Add(-time.Hour), nil))
	mock.ExpectExec(`UPDATE plugins SET rating=\?,updated_at=\? WHERE plugin_id=\? AND deleted_at IS NULL`).
		WithArgs(&rating, now, "plugin-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).
		WithArgs("audit-rating", "plugin-1", "rate", "admin", "Root", "request-1", "sha256:p", "sha256:p", `{"m":1}`, `{"p":1}`, "rating:2->5", now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	got, err := r.UpdateRating(context.Background(), Scope{CallerUID: "admin", Admin: true}, RatingParams{
		PluginID: "plugin-1", Rating: &rating, OperatorID: "admin", OperatorName: "Root", RequestID: "request-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Rating == nil || *got.Rating != 5 || got.PluginHash != "sha256:p" || got.CurrentVersionID == nil || *got.CurrentVersionID != "version-id" {
		t.Fatalf("unexpected plugin: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRatingClassifiesLockConflicts(t *testing.T) {
	for _, number := range []uint16{1205, 1213} {
		t.Run((&mysql.MySQLError{Number: number}).Error(), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			r := New(db)
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT .* FROM plugins`).WithArgs("plugin-1").
				WillReturnError(&mysql.MySQLError{Number: number, Message: "lock conflict"})
			mock.ExpectRollback()

			_, err = r.UpdateRating(context.Background(), Scope{Admin: true}, RatingParams{PluginID: "plugin-1"})
			if !errors.Is(err, ErrDeadlock) {
				t.Fatalf("error = %v, want ErrDeadlock", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpdateRatingRollsBackWhenAuditFails(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 4, 1, 2, 3, 0, time.UTC)
	r := New(db)
	r.now = func() time.Time { return now }
	r.id = func() string { return "audit-rating" }

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p\.plugin_id=\? .* FOR UPDATE`).WithArgs("plugin-1").
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("plugin-1", "Plugin", model.PluginTypeSkill, 0, nil, []byte(`[]`), "pub", "owner", "space", model.PluginVisibilitySpace, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, 4, []byte(`{}`), []byte(`{}`), nil, "sha256:m", "sha256:p", nil, nil, 1, now, now, nil))
	mock.ExpectExec(`UPDATE plugins SET rating=\?,updated_at=\?`).WithArgs(nil, now, "plugin-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnError(errors.New("audit unavailable"))
	mock.ExpectRollback()

	_, err = r.UpdateRating(context.Background(), Scope{Admin: true}, RatingParams{PluginID: "plugin-1"})
	if err == nil {
		t.Fatal("expected audit error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
