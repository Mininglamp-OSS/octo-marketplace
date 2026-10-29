package migration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestApplyRejectsInvalidFinalRelationBeforeAnySQL(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n := Node{Plugin: model.Plugin{ID: "expert", Type: model.PluginTypeExpert}, Relations: []model.PluginRelation{{Type: "expert_connector", SourcePluginID: "expert", TargetPluginID: "connector", TargetPluginType: model.PluginTypeConnector, Status: 1, Data: json.RawMessage(`{"unknown":true}`)}}}
	_, err = Apply(context.Background(), db, Plan{ID: "run", SnapshotID: "snapshot", Nodes: []Node{n}}, DatabaseIdentity{Name: "db", ServerUUID: "server"})
	if err != ErrIssues {
		t.Fatalf("want final-contract rejection before database access, got %v", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFinalContractRejectsReservedContextInAllRepresentations(t *testing.T) {
	for _, carrier := range []string{"files", "package", "artifacts", "sidecar"} {
		t.Run(carrier, func(t *testing.T) {
			n := Node{Plugin: model.Plugin{ID: "expert", Type: model.PluginTypeExpert}}
			switch carrier {
			case "files":
				n.Files = []File{{Path: "./expert/context.json"}}
			case "package":
				n.Plugin.Package = json.RawMessage(`{"attachments":[{"path":"expert/context.json"}]}`)
			case "artifacts":
				n.Artifacts = json.RawMessage(`[{"path":"EXPERT/context.json"}]`)
			case "sidecar":
				n.Plugin.AttachmentKeys = json.RawMessage(`{"expert/context.json":"key"}`)
			}
			p := Plan{ID: "run", SnapshotID: "snapshot", Nodes: []Node{n}}
			if len(validateFinalContracts(p)) == 0 {
				t.Fatal("reserved path accepted")
			}
			if _, err := plannedObjects(p); err != ErrIssues {
				t.Fatalf("publication accepted reserved path: %v", err)
			}
			if _, err := Apply(context.Background(), nil, p, DatabaseIdentity{}); err != ErrIssues {
				t.Fatalf("apply accepted reserved path: %v", err)
			}
		})
	}
}

func TestFinalContractRejectsRewrittenGraphTampering(t *testing.T) {
	for _, kind := range []string{"missing target", "wrong type", "inactive target", "cross scope", "wrong source", "wrong pin"} {
		t.Run(kind, func(t *testing.T) {
			p, err := Build(legacyReferenceInput(false), Policy{})
			if err != nil {
				t.Fatal(err)
			}
			for i := range p.Nodes {
				if len(p.Nodes[i].Relations) == 0 {
					continue
				}
				edge := &p.Nodes[i].Relations[0]
				target := -1
				for j := range p.Nodes {
					if p.Nodes[j].Plugin.ID == edge.TargetPluginID {
						target = j
					}
				}
				if target < 0 {
					t.Fatal("fixture has no target")
				}
				switch kind {
				case "missing target":
					edge.TargetPluginID = "not-in-plan"
				case "wrong type":
					p.Nodes[target].Plugin.Type = model.PluginTypeConnector
				case "inactive target":
					p.Nodes[target].Plugin.Status = 0
				case "cross scope":
					p.Nodes[i].Plugin.Visibility = model.PluginVisibilitySystem
					p.Nodes[target].Plugin.Visibility = model.PluginVisibilitySpace
					space := "foreign"
					p.Nodes[target].Plugin.SpaceID = &space
				case "wrong source":
					edge.SourcePluginID = "foreign"
				case "wrong pin":
					version := "foreign-version"
					edge.TargetVersionID = &version
				}
				break
			}
			if _, err = Apply(context.Background(), nil, p, DatabaseIdentity{}); err != ErrIssues {
				t.Fatalf("tampered graph reached DB: %v", err)
			}
		})
	}
}

func TestCategorySnapshotRequiredAtPersistenceBoundary(t *testing.T) {
	if verifyCategorySnapshot(context.Background(), nil, Plan{}) == nil {
		t.Fatal("missing category capture accepted")
	}
}
