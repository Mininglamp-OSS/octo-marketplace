package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// The detail_graph caps live in the repository package, but the shape they must
// clear is defined here: container import is the writer that mints squad graphs,
// and a maximum-size legal import must remain renderable rather than 413 forever.
// This pins the two together, since the repository can only mirror these limits.
func TestGraphCapsClearContainerImportCeiling(t *testing.T) {
	// Skills dedupe only by (file,name), so members declaring distinct names each
	// mint their own embedded skill node: members + members*skills children, and
	// one edge per child (every embedded child has exactly one parent).
	maxChildren := containerMaxMembers * (1 + containerMaxSkills)
	if got := pluginrepo.MaxGraphNodes(); got < maxChildren {
		t.Fatalf("node cap %d is below the container import ceiling %d: a maximum-size squad would 413 forever", got, maxChildren)
	}
	if got := pluginrepo.MaxGraphEdges(); got < maxChildren {
		t.Fatalf("edge cap %d is below the container import ceiling %d: a maximum-size squad would 413 forever", got, maxChildren)
	}
}

func TestDetailGraph_ValidatesCaller(t *testing.T) {
	svc := fixedService(&fakeStore{})
	if _, err := svc.DetailGraph(context.Background(), Caller{}, "id"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty caller = %v", err)
	}
}

func TestDetailGraph_MapsNotFoundAndTooLarge(t *testing.T) {
	svc := fixedService(&fakeStore{err: pluginrepo.ErrNotFound})
	if _, err := svc.DetailGraph(context.Background(), testCaller, "plug-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("not found = %v", err)
	}
	svc = fixedService(&fakeStore{err: pluginrepo.ErrGraphTooLarge})
	if _, err := svc.DetailGraph(context.Background(), testCaller, "plug-1"); !errors.Is(err, ErrGraphTooLarge) {
		t.Fatalf("too large = %v", err)
	}
}

// The closure is returned flat and unaggregated: DetailGraph must not derive a
// member_count onto any node. The relation matrix never admits an expert_team
// as a relation target, so a related node is never a team, and the root's
// response projection carries no member_count field — a client counts
// expert_team_expert edges in the returned slice instead.
func TestDetailGraph_ReturnsClosureWithoutDerivedMemberCounts(t *testing.T) {
	team := &model.Plugin{ID: "team-1", Type: model.PluginTypeExpertTeam}
	m1 := &model.Plugin{ID: "m1", Type: model.PluginTypeExpert}
	m2 := &model.Plugin{ID: "m2", Type: model.PluginTypeExpert}
	s1 := &model.Plugin{ID: "s1", Type: model.PluginTypeSkill}
	rels := []model.PluginRelation{
		{ID: "r1", SourcePluginID: "team-1", TargetPluginID: "m1", Type: "expert_team_expert", TargetPluginType: model.PluginTypeExpert},
		{ID: "r2", SourcePluginID: "team-1", TargetPluginID: "m2", Type: "expert_team_expert", TargetPluginType: model.PluginTypeExpert},
		{ID: "r3", SourcePluginID: "m1", TargetPluginID: "s1", Type: "expert_skill", TargetPluginType: model.PluginTypeSkill},
	}
	svc := fixedService(&fakeStore{detailGraphRoot: team, detailGraphRels: rels, detailGraphRelated: []*model.Plugin{m1, m2, s1}})
	out, err := svc.DetailGraph(context.Background(), testCaller, "team-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Relations) != 3 || len(out.Related) != 3 {
		t.Fatalf("closure = %d rels / %d related, want 3/3", len(out.Relations), len(out.Related))
	}
	if out.Plugin.MemberCount != 0 {
		t.Fatalf("root.MemberCount = %d, want 0 (no field on the wire to carry it)", out.Plugin.MemberCount)
	}
	for _, n := range out.Related {
		if n.MemberCount != 0 {
			t.Fatalf("related %s carries MemberCount %d, want 0", n.ID, n.MemberCount)
		}
	}
}

func TestDetailGraph_RootNotFound(t *testing.T) {
	svc := fixedService(&fakeStore{})
	if _, err := svc.DetailGraph(context.Background(), testCaller, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
