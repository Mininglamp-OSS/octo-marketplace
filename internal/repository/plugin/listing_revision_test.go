package plugin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestPublishPluginRejectsStaleDraftRevisionBeforeWrites(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE .* FOR UPDATE`).
		WithArgs("connector", "owner", "space").
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow(
			"connector", "Saved in another tab", model.PluginTypeConnector, 0, nil, []byte(`[]`), "", "owner", "space",
			model.PluginVisibilityPrivate, model.PluginListingStateDraft, "Owner", "human", nil, nil, "", 0, nil,
			[]byte(`{}`), []byte(`{}`), nil, "manifest-hash", "package-hash", nil, nil, 1, now, now, nil,
		))
	mock.ExpectRollback()
	got, err := New(db).PublishPlugin(context.Background(), Scope{CallerUID: "owner", SpaceID: "space"}, PublishParams{
		PluginID: "connector", ExpectedDraftRevision: "earlier-draft-revision",
	})
	if !errors.Is(err, ErrConflict) || got != nil {
		t.Fatalf("publication = %+v, %v; want nil, ErrConflict", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
