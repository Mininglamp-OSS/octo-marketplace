package plugin

import (
	"context"
	"database/sql/driver"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

func TestLegacyNameColumnsUsesDraftReservations(t *testing.T) {
	for _, kind := range []string{"skill", "expert", "expert_team"} {
		draft, published, scope, err := legacyNameColumns(plugRow{name: "Ａlpha", typ: kind, space: "space-a", versionID: "v1"})
		if err != nil || draft != "alpha" || published != nil || scope != "space-a" {
			t.Fatalf("%s: draft=%v published=%v scope=%q err=%v", kind, draft, published, scope, err)
		}
	}
}

func TestHistoricalTeamApplyWritesReservationAndRollsBackConflict(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("conflict=%t", conflict), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			x := plugRow{id: "team", name: "Ａlpha", typ: "expert_team", space: "space-a", tags: "[]", manifest: "{}", pkg: "{}", status: 1, created: time.Unix(1, 0), updated: time.Unix(1, 0)}
			args := make([]driver.Value, 28)
			for i := range args {
				args[i] = sqlmock.AnyArg()
			}
			args[0], args[1], args[2], args[25], args[26], args[27] = "team", "Ａlpha", "expert_team", "alpha", nil, "space-a"
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM plugins WHERE plugin_id=\\? AND").WithArgs(args...).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
			mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM plugins WHERE plugin_id=\\?").WithArgs("team").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
			insert := mock.ExpectExec("INSERT INTO plugins\\(").WithArgs(args...)
			if conflict {
				insert.WillReturnError(&mysql.MySQLError{Number: 1062, Message: "duplicate reservation"})
				mock.ExpectRollback()
			} else {
				insert.WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM plugin_audit_logs").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
				mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM plugin_audit_logs").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
				mock.ExpectExec("INSERT INTO plugin_audit_logs").WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectCommit()
			}
			err = New(db).apply(context.Background(), plan{plugins: []plugRow{x}}, &Report{})
			if (err != nil) != conflict {
				t.Fatalf("apply error=%v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHistoricalTeamVerifyRejectsMissingOrDifferentReservation(t *testing.T) {
	for _, key := range []any{nil, "wrong", "alpha"} {
		t.Run(fmt.Sprint(key), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			x := plugRow{id: "team", name: "Alpha", typ: "expert_team", space: "space-a", phash: "hash"}
			mock.ExpectQuery("SELECT plugin_type,is_embedded,plugin_hash,name_key").WithArgs("team").WillReturnRows(sqlmock.NewRows([]string{"type", "embedded", "hash", "name_key", "published_name_key", "name_scope"}).AddRow("expert_team", 0, "hash", key, nil, "space-a"))
			mock.ExpectQuery("SELECT plugin_id,action,operator_id").WillReturnRows(sqlmock.NewRows([]string{"plugin_id", "action", "operator_id", "operator_name", "request_id", "after_hash", "manifest", "pkg"}))
			counts, _, _, err := New(db).verify(context.Background(), plan{plugins: []plugRow{x}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if key == "alpha" {
				want = 0
			}
			if counts.Conflicts != want {
				t.Fatalf("conflicts=%d want %d", counts.Conflicts, want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
