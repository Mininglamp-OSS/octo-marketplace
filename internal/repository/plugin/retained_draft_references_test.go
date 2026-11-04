package plugin

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestDraftRetentionOnlyExemptsExistingExactPins(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	mock.ExpectQuery("SELECT relation_type,target_plugin_id,target_version_id FROM plugin_relations").WithArgs("root").WillReturnRows(sqlmock.NewRows([]string{"relation_type", "target_plugin_id", "target_version_id"}).AddRow("expert_skill", "skill", "released"))
	retained, changed := "released", "new-version"
	edge := model.PluginRelation{SourcePluginID: "root", SourcePluginType: model.PluginTypeExpert, TargetPluginID: "skill", TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", TargetVersionID: &retained, Status: 1}
	alternate := edge
	alternate.TargetVersionID = &changed
	got, err := retainedDraftReferences(context.Background(), tx, model.Plugin{ID: "root", Type: model.PluginTypeExpert}, []model.PluginRelation{edge}, true)
	if err != nil || len(got) != 0 {
		t.Fatalf("retained=%+v err=%v", got, err)
	}
	mock.ExpectQuery("SELECT relation_type,target_plugin_id,target_version_id FROM plugin_relations").WithArgs("root").WillReturnRows(sqlmock.NewRows([]string{"relation_type", "target_plugin_id", "target_version_id"}).AddRow("expert_skill", "skill", "released"))
	got, err = retainedDraftReferences(context.Background(), tx, model.Plugin{ID: "root", Type: model.PluginTypeExpert}, []model.PluginRelation{alternate}, true)
	if err != nil || len(got) != 1 || *got[0].TargetVersionID != changed {
		t.Fatalf("changed=%+v err=%v", got, err)
	}
	// Retaining a pin must not bypass structural validation, even without any
	// need to access the hidden target itself.
	bad := edge
	bad.Type = "expert_team_expert"
	if _, err = retainedDraftReferences(context.Background(), tx, model.Plugin{ID: "root", Type: model.PluginTypeExpert}, []model.PluginRelation{bad}, true); err == nil {
		t.Fatal("invalid relation type retained")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
