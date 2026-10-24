package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestLegacyInventoryDoesNotInventPublicationOrExposeContent(t *testing.T) {
	b, e := os.ReadFile("testdata/legacy.json")
	if e != nil {
		t.Fatal(e)
	}
	var in Input
	if e = json.Unmarshal(b, &in); e != nil {
		t.Fatal(e)
	}
	r := Build(in)
	if r.Counts["listed_publication_unknown"] != 2 || r.Counts["historical_dependency_unpinned"] != 1 || r.Counts["storage_object_not_checked"] != 1 {
		t.Fatalf("findings: %+v", r.Counts)
	}
	if len(r.SameNameGroups) != 1 || len(r.SameNameGroups[0]) != 2 {
		t.Fatal("different owners with same name must remain candidates")
	}
	if len(r.Resources[1].Parents) != 1 || r.Resources[1].Parents[0] != "expert-a" {
		t.Fatal("embedded ownership edge missing")
	}
	out, _ := json.Marshal(r)
	for _, secret := range []string{"secret-path", "private-object-key", "sha256:placeholder", "search"} {
		if strings.Contains(string(out), secret) {
			t.Fatalf("unsafe report: %s", secret)
		}
	}
}
func TestPointersAndDeletedTargetsAreNotTreatedAsValid(t *testing.T) {
	r := Build(Input{Plugins: []Plugin{{ID: "a", Type: "expert", Current: "vb", Published: "va", Listing: "published"}, {ID: "b", Type: "skill", Deleted: true, Embedded: true, Owner: "x"}}, Versions: []Version{{ID: "vb", PluginID: "b", Schema: 1, Artifacts: json.RawMessage(`[]`), Relations: json.RawMessage(`[]`)}}, Relations: []Relation{{ID: "r", Source: "a", Target: "b"}}, Releases: []Release{{PluginID: "a", VersionID: "vb"}}})
	for _, code := range []string{"current_snapshot_mismatch", "published_release_missing", "release_snapshot_mismatch", "relation_to_deleted_resource", "embedded_ownership_mismatch"} {
		if r.Counts[code] != 1 {
			t.Errorf("%s = %d", code, r.Counts[code])
		}
	}
}
func TestNoPlacementInferenceAndConnectorNeedsNoVersion(t *testing.T) {
	r := Build(Input{Plugins: []Plugin{{ID: "c", Type: "connector", Owner: "u", Listing: "published", Attachments: json.RawMessage(`[]`)}}})
	if len(r.Findings) != 0 || r.Resources[0].Embedded {
		t.Fatalf("unexpected: %+v", r)
	}
}
func TestReadRollbackAndNullJSON(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectBegin()
	m.ExpectQuery("SELECT plugin_id, plugin_name").WithArgs(11).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "type", "owner", "space", "visibility", "listing", "embedded", "deleted", "current", "published", "attachments", "keys"}).AddRow("a", "name", "skill", "u", "s", "private", "draft", false, false, "", "", nil, nil))
	m.ExpectQuery("SELECT version_id").WithArgs(11).WillReturnRows(sqlmock.NewRows([]string{"id", "p", "schema", "attachments", "keys", "artifacts", "relations"}).AddRow("v1", "a", 0, nil, nil, nil, nil).AddRow("v2", "a", 1, "[]", []byte(`{}`), []byte(`[]`), "[]"))
	m.ExpectQuery("SELECT relation_id").WithArgs(11).WillReturnRows(sqlmock.NewRows([]string{"id", "source", "target", "type"}))
	m.ExpectQuery("SELECT plugin_id, version_id FROM plugin_version_releases").WithArgs(11).WillReturnRows(sqlmock.NewRows([]string{"p", "v"}))
	m.ExpectRollback()
	in, e := Read(context.Background(), db, 10)
	if e != nil || len(in.Plugins) != 1 {
		t.Fatalf("%+v %v", in, e)
	}
	if in.Plugins[0].Attachments != nil || in.Plugins[0].Keys != nil || len(in.Versions) != 2 || in.Versions[0].Artifacts != nil || string(in.Versions[1].Attachments) != "[]" || string(in.Versions[1].Relations) != "[]" {
		t.Fatalf("NULL and string/byte JSON must preserve their meaning: %+v", in)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestReadFailureRedactsDriverError(t *testing.T) {
	db, m, _ := sqlmock.New()
	defer db.Close()
	m.ExpectBegin()
	m.ExpectQuery("SELECT plugin_id").WithArgs(2).WillReturnError(errors.New("password=secret internal-db:3306"))
	m.ExpectRollback()
	in, e := Read(context.Background(), db, 1)
	if e == nil || strings.Contains(e.Error(), "secret") || len(in.Plugins) != 0 {
		t.Fatalf("unsafe error %v", e)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestReadLimitFailsWithoutPartialInventory(t *testing.T) {
	db, m, _ := sqlmock.New()
	defer db.Close()
	m.ExpectBegin()
	rows := sqlmock.NewRows([]string{"id", "name", "type", "owner", "space", "visibility", "listing", "embedded", "deleted", "current", "published", "attachments", "keys"})
	for _, id := range []string{"a", "b"} {
		rows.AddRow(id, "n", "skill", "u", "s", "private", "draft", false, false, "", "", nil, nil)
	}
	m.ExpectQuery("SELECT plugin_id").WithArgs(2).WillReturnRows(rows)
	m.ExpectRollback()
	in, e := Read(context.Background(), db, 1)
	if e == nil || len(in.Plugins) != 0 {
		t.Fatal("partial report must not be returned")
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestDistributionAccountsForEveryVisibilityLifecycleAndIdentity(t *testing.T) {
	var plugins []Plugin
	for _, visibility := range []string{"private", "space", "system"} {
		for _, listing := range []string{"draft", "published", "delisted"} {
			for _, embedded := range []bool{false, true} {
				for _, deleted := range []bool{false, true} {
					plugins = append(plugins, Plugin{ID: fmt.Sprint(len(plugins)), Visibility: visibility, Listing: listing, Embedded: embedded, Deleted: deleted})
				}
			}
		}
	}
	got := Build(Input{Plugins: plugins}).Distribution
	if len(got) != 36 {
		t.Fatalf("distribution cells = %d", len(got))
	}
	for _, cell := range got {
		if cell.Count != 1 {
			t.Fatalf("unexpected cell %+v", cell)
		}
	}
	for i, j := 0, len(plugins)-1; i < j; i, j = i+1, j-1 {
		plugins[i], plugins[j] = plugins[j], plugins[i]
	}
	if !reflect.DeepEqual(got, Build(Input{Plugins: plugins}).Distribution) {
		t.Fatal("distribution depends on input order")
	}
}

func TestDistributionKeepsLegacyAndUnknownExplicit(t *testing.T) {
	got := Build(Input{Plugins: []Plugin{
		{Visibility: "public", Listing: "published"},
		{Visibility: "unrecognized-secret-value", Listing: "invalid"},
		{Visibility: "", Listing: ""},
	}}).Distribution
	want := []Distribution{
		{Visibility: "legacy_public", Listing: "published", Identity: "top_level", Count: 1},
		{Visibility: "unknown", Listing: "unknown", Identity: "top_level", Count: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("distribution: %+v", got)
	}
	if empty := Build(Input{}).Distribution; empty == nil || len(empty) != 0 {
		t.Fatal("empty distribution must serialize as []")
	}
}
