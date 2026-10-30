package plugin

import (
	"context"
	"database/sql/driver"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestOwnerListKeepsDraftAndPublishedBaselinesSeparate(t *testing.T) {
	for _, tc := range []struct {
		name               string
		publishedID, label any
	}{
		{"published", "release-3", "1.0.3"}, {"never-published", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, _ := sqlmock.New()
			defer db.Close()
			sc := Scope{CallerUID: "owner", SpaceID: "space"}
			filter := ListFilter{Mine: true, IncludeReviewState: true}
			from, where, args := buildListQuery(sc, filter)
			from += ` LEFT JOIN plugin_version_releases owner_release ON owner_release.plugin_id=p.plugin_id AND owner_release.version_id=p.published_version_id AND p.plugin_type<>'connector'`
			vals := make([]driver.Value, len(args))
			for i, v := range args {
				vals[i] = v
			}
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(DISTINCT p.plugin_id) ` + from + where)).WithArgs(vals...).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			columns := pluginTestColumns()
			columns = append(append([]string{}, columns[:19]...), columns[21:]...)
			columns = append(columns, "view_count", "install_count", "download_count", "has_pending_review", "review_id", "review_status", "published_version_id", "published_version")
			now := time.Now()
			rows := sqlmock.NewRows(columns).AddRow("p", "Skill", model.PluginTypeSkill, 0, nil, []byte(`[]`), "", "owner", "space", model.PluginVisibilitySpace, model.PluginListingStatePublished, "Owner", "human", nil, nil, "", 0, nil, []byte(`{}`), "mh", "ph", "draft-0", "1.0.0", 1, now, now, nil, 0, 0, 0, false, nil, nil, tc.publishedID, tc.label)
			mock.ExpectQuery(`SELECT .*owner_release.version_id,owner_release.version_label.*p.owner_uid=\? AND p.space_id=\?.*LIMIT \? OFFSET \?`).WithArgs(append(vals, 20, 0)...).WillReturnRows(rows)
			items, _, err := New(db).WithVersionManagement(true).List(context.Background(), sc, filter)
			if err != nil {
				t.Fatal(err)
			}
			p := items[0]
			if *p.CurrentVersion != "1.0.0" || *p.CurrentVersionID != "draft-0" {
				t.Fatalf("draft mutated: %+v", p)
			}
			if tc.label == nil {
				if p.PublishedVersion != nil || p.PublishedVersionID != nil {
					t.Fatal("invented release")
				}
			} else if *p.PublishedVersion != "1.0.3" || *p.PublishedVersionID != "release-3" {
				t.Fatalf("wrong release: %+v", p)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOwnerReleaseMetadataKeepsOwnerAndSpacePredicates(t *testing.T) {
	for _, sc := range []Scope{{CallerUID: "other", SpaceID: "space"}, {CallerUID: "owner", SpaceID: "other-space"}} {
		db, mock, _ := sqlmock.New()
		mock.ExpectQuery(`SELECT COUNT.*p.owner_uid=\? AND p.space_id=\?`).WithArgs(sc.SpaceID, sc.CallerUID, sc.CallerUID, sc.SpaceID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(`SELECT .*owner_release.version_label.*p.owner_uid=\? AND p.space_id=\?`).WithArgs(sc.SpaceID, sc.CallerUID, sc.CallerUID, sc.SpaceID, 20, 0).WillReturnRows(sqlmock.NewRows([]string{"empty"}))
		rows, total, err := New(db).WithVersionManagement(true).List(context.Background(), sc, ListFilter{Mine: true})
		if err != nil || total != 0 || len(rows) != 0 {
			t.Fatalf("scoped result: %v %d %v", rows, total, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
}
