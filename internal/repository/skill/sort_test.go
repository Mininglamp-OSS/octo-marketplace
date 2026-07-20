package skill

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestListSortComprehensive(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()

	// comprehensive sort → COUNT query + data query with OFFSET
	mock.ExpectQuery("SELECT COUNT").
		WithArgs("space-1", "user-1", "space-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectQuery("ORDER BY .+COALESCE.+download_count.+ \\* 5").
		WithArgs("space-1", "user-1", "space-1", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
			"description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at", "resolved_version", "version_storage", "view_count", "download_count",
		}).AddRow(
			"s1", "Skill 1", "Skill 1", "", "", "",
			"desc", "cat-1", []byte(`[]`),
			"user-1", "Alice", "space-1", "space", "1.0.0",
			"", "f.zip", "url", int64(100), "sha", now, now, "1.0.0", "", int64(10), int64(5),
		))

	result, err := New(db).List(context.Background(), ListFilter{
		SpaceID: "space-1",
		UserID:  "user-1",
		Limit:   20,
		Sort:    SortComprehensive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 {
		t.Errorf("Total = %d, want 1", result.Total)
	}
	if len(result.Items) != 1 {
		t.Fatalf("Items count = %d, want 1", len(result.Items))
	}
	if result.Items[0].ViewCount != 10 {
		t.Errorf("ViewCount = %d, want 10", result.Items[0].ViewCount)
	}
	if result.Items[0].DownloadCount != 5 {
		t.Errorf("DownloadCount = %d, want 5", result.Items[0].DownloadCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListSortLatestUsesCursor(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()

	// latest sort → cursor-based pagination (no COUNT query)
	mock.ExpectQuery("ORDER BY s\\.created_at DESC, s\\.id DESC").
		WithArgs("space-1", "user-1", "space-1", 21). // limit+1
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
			"description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at", "resolved_version", "version_storage", "view_count", "download_count",
		}).AddRow(
			"s1", "Skill 1", "Skill 1", "", "", "",
			"desc", "cat-1", []byte(`[]`),
			"user-1", "Alice", "space-1", "space", "1.0.0",
			"", "f.zip", "url", int64(100), "sha", now, now, "1.0.0", "", int64(0), int64(0),
		))

	result, err := New(db).List(context.Background(), ListFilter{
		SpaceID: "space-1",
		UserID:  "user-1",
		Limit:   20,
		Sort:    SortLatest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.NextCursor != nil {
		t.Errorf("NextCursor should be nil when fewer results than limit+1")
	}
	if len(result.Items) != 1 {
		t.Fatalf("Items count = %d, want 1", len(result.Items))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListSortDownloads(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()

	mock.ExpectQuery("SELECT COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	mock.ExpectQuery("ORDER BY COALESCE.+download_count.+ DESC").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
			"description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at", "resolved_version", "version_storage", "view_count", "download_count",
		}).
			AddRow("s1", "Skill 1", "Skill 1", "", "", "",
				"desc", "cat-1", []byte(`[]`),
				"user-1", "Alice", "space-1", "space", "1.0.0",
				"", "f.zip", "url", int64(100), "sha", now, now, "1.0.0", "", int64(0), int64(50)).
			AddRow("s2", "Skill 2", "Skill 2", "", "", "",
				"desc", "cat-1", []byte(`[]`),
				"user-2", "Bob", "space-1", "public", "1.0.0",
				"", "f.zip", "url", int64(100), "sha", now, now, "1.0.0", "", int64(0), int64(10)))

	result, err := New(db).List(context.Background(), ListFilter{
		SpaceID: "space-1",
		UserID:  "user-1",
		Limit:   20,
		Sort:    SortDownloads,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 {
		t.Errorf("Total = %d, want 2", result.Total)
	}
	if len(result.Items) != 2 {
		t.Fatalf("Items count = %d, want 2", len(result.Items))
	}
	if result.Items[0].DownloadCount != 50 {
		t.Errorf("first item DownloadCount = %d, want 50", result.Items[0].DownloadCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListSortViews(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()

	mock.ExpectQuery("SELECT COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectQuery("ORDER BY COALESCE.+view_count.+ DESC").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
			"description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at", "resolved_version", "version_storage", "view_count", "download_count",
		}).AddRow(
			"s1", "Skill 1", "Skill 1", "", "", "",
			"desc", "cat-1", []byte(`[]`),
			"user-1", "Alice", "space-1", "space", "1.0.0",
			"", "f.zip", "url", int64(100), "sha", now, now, "1.0.0", "", int64(100), int64(0),
		))

	result, err := New(db).List(context.Background(), ListFilter{
		SpaceID: "space-1",
		UserID:  "user-1",
		Limit:   20,
		Sort:    SortViews,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].ViewCount != 100 {
		t.Errorf("ViewCount = %d, want 100", result.Items[0].ViewCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListOffsetPagination(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()

	mock.ExpectQuery("SELECT COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(30))

	// Expect LIMIT ? OFFSET ? with 10, 10
	mock.ExpectQuery("LIMIT .+ OFFSET").
		WithArgs("space-1", "user-1", "space-1", 10, 10).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
			"description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at", "resolved_version", "version_storage", "view_count", "download_count",
		}).AddRow(
			"s11", "Skill 11", "Skill 11", "", "", "",
			"desc", "cat-1", []byte(`[]`),
			"user-1", "Alice", "space-1", "space", "1.0.0",
			"", "f.zip", "url", int64(100), "sha", now, now, "1.0.0", "", int64(0), int64(0),
		))

	result, err := New(db).List(context.Background(), ListFilter{
		SpaceID: "space-1",
		UserID:  "user-1",
		Limit:   10,
		Offset:  10,
		Sort:    SortComprehensive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 30 {
		t.Errorf("Total = %d, want 30", result.Total)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListLimitMax50(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	// Even with limit=100, should be capped to 50
	mock.ExpectQuery("LIMIT .+ OFFSET").
		WithArgs("space-1", "user-1", "space-1", 50, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
			"description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at", "resolved_version", "version_storage", "view_count", "download_count",
		}))

	_, err = New(db).List(context.Background(), ListFilter{
		SpaceID: "space-1",
		UserID:  "user-1",
		Limit:   100,
		Sort:    SortComprehensive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetByIDWithMetrics(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT .+ FROM skills .+ LEFT JOIN resource_metrics").
		WithArgs("skill-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
			"description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at", "resolved_version", "version_storage", "view_count", "download_count",
		}).AddRow(
			"skill-1", "Test", "Test", "", "", "",
			"desc", "cat-1", []byte(`[]`),
			"user-1", "Alice", "space-1", "public", "1.0.0",
			"", "f.zip", "url", int64(100), "sha", now, now, "1.0.0", "", int64(42), int64(7),
		))

	row, err := New(db).GetByID(context.Background(), "skill-1")
	if err != nil {
		t.Fatal(err)
	}
	if row == nil {
		t.Fatal("expected non-nil row")
	}
	if row.ViewCount != 42 {
		t.Errorf("ViewCount = %d, want 42", row.ViewCount)
	}
	if row.DownloadCount != 7 {
		t.Errorf("DownloadCount = %d, want 7", row.DownloadCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetByIDNoMetricsRow(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	// When there's no matching row in resource_metrics, COALESCE returns 0
	mock.ExpectQuery("SELECT .+ FROM skills .+ LEFT JOIN resource_metrics").
		WithArgs("skill-new").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
			"description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at", "resolved_version", "version_storage", "view_count", "download_count",
		}).AddRow(
			"skill-new", "New", "New", "", "", "",
			"desc", "cat-1", []byte(`[]`),
			"user-1", "Alice", "space-1", "public", "1.0.0",
			"", "f.zip", "url", int64(100), "sha", now, now, "1.0.0", "", int64(0), int64(0),
		))

	row, err := New(db).GetByID(context.Background(), "skill-new")
	if err != nil {
		t.Fatal(err)
	}
	if row == nil {
		t.Fatal("expected non-nil row")
	}
	if row.ViewCount != 0 {
		t.Errorf("ViewCount = %d, want 0", row.ViewCount)
	}
	if row.DownloadCount != 0 {
		t.Errorf("DownloadCount = %d, want 0", row.DownloadCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
