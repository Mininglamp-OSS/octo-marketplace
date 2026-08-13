package expert

import (
	"context"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	expertrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/expert"
)

// adminReq builds a minimal valid system-expert create request.
func adminExpertReq(name string) model.ExpertCreateRequest {
	return model.ExpertCreateRequest{
		Name:        name,
		Summary:     "s",
		Category:    "研发工具",
		Instruction: "do things",
		MCPConfig:   `{"mcpServers":{}}`,
	}
}

// adminSquadReq builds a minimal valid system-squad create request.
func adminSquadReq(name string) model.SquadCreateRequest {
	return model.SquadCreateRequest{
		Name:     name,
		Summary:  "s",
		Category: "研发工具",
		Members: []model.SquadMemberInput{
			{Name: "组长", Role: "leader", IsLeader: true, Instruction: "lead"},
		},
	}
}

func TestCreateSystemExpert_StampsSystemAndCrossSpace(t *testing.T) {
	svc, store := newService()
	caller := Caller{UID: "admin-1", Name: "Admin"}

	detail, err := svc.CreateSystemExpert(context.Background(), caller, adminExpertReq("后端架构师"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if detail.Visibility != model.VisibilitySystem {
		t.Fatalf("visibility = %q, want system", detail.Visibility)
	}
	if detail.Category != "研发工具" {
		t.Fatalf("category = %q, want the resolved name", detail.Category)
	}
	stored := store.experts[detail.ExpertID]
	if stored == nil || stored.SpaceID != "" || stored.Visibility != model.VisibilitySystem {
		t.Fatalf("stored row not system/cross-space: %+v", stored)
	}
	if stored.OwnerUID != "admin-1" {
		t.Fatalf("owner = %q, want admin-1", stored.OwnerUID)
	}
}

func TestCreateSystemExpert_RejectsDuplicateName(t *testing.T) {
	svc, _ := newService()
	caller := Caller{UID: "admin-1", Name: "Admin"}
	if _, err := svc.CreateSystemExpert(context.Background(), caller, adminExpertReq("dup")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := svc.CreateSystemExpert(context.Background(), caller, adminExpertReq("dup"))
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("second create err = %v, want ErrNameTaken", err)
	}
}

// The SystemExpertNameExists pre-check is only a fast path: when a concurrent
// admin write races past it, the uq_expert_system_name_live unique index fires
// at insert time and the repo surfaces ErrNameTaken. The service must map that
// to its own ErrNameTaken (409 DUPLICATE), not a 500.
func TestCreateSystemExpert_MapsIndexDuplicateOnRace(t *testing.T) {
	svc, store := newService()
	store.createExpertErr = expertrepo.ErrNameTaken
	_, err := svc.CreateSystemExpert(context.Background(), Caller{UID: "admin-1"}, adminExpertReq("racy"))
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("err = %v, want ErrNameTaken", err)
	}
}

func TestUpdateSystemExpert_MapsIndexDuplicateOnRace(t *testing.T) {
	svc, store := newService()
	created, err := svc.CreateSystemExpert(context.Background(), Caller{UID: "admin-1"}, adminExpertReq("orig"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	store.updateSystemExpertErr = expertrepo.ErrNameTaken
	newName := "racy"
	_, err = svc.UpdateSystemExpert(context.Background(), created.ExpertID, model.ExpertPatchRequest{Name: &newName})
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("err = %v, want ErrNameTaken", err)
	}
}

func TestCreateSystemSquad_MapsIndexDuplicateOnRace(t *testing.T) {
	svc, store := newService()
	store.createSquadErr = expertrepo.ErrNameTaken
	_, err := svc.CreateSystemSquad(context.Background(), Caller{UID: "admin-1"}, adminSquadReq("racy"))
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("err = %v, want ErrNameTaken", err)
	}
}

func TestUpdateSystemSquad_MapsIndexDuplicateOnRace(t *testing.T) {
	svc, store := newService()
	created, err := svc.CreateSystemSquad(context.Background(), Caller{UID: "admin-1"}, adminSquadReq("orig"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	store.updateSystemSquadErr = expertrepo.ErrNameTaken
	newName := "racy"
	_, err = svc.UpdateSystemSquad(context.Background(), created.SquadID, model.SquadPatchRequest{Name: &newName})
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("err = %v, want ErrNameTaken", err)
	}
}

// Admin create stamps visibility=system; a client-sent visibility must be
// rejected with 400 rather than silently ignored (doc §9).
func TestCreateSystemExpert_RejectsClientVisibility(t *testing.T) {
	svc, _ := newService()
	req := adminExpertReq("专家")
	req.Visibility = model.VisibilityPrivate
	_, err := svc.CreateSystemExpert(context.Background(), Caller{UID: "admin-1"}, req)
	if !errors.Is(err, ErrVisibilityNotAllowed) {
		t.Fatalf("err = %v, want ErrVisibilityNotAllowed", err)
	}
}

func TestCreateSystemSquad_RejectsClientVisibility(t *testing.T) {
	svc, _ := newService()
	req := adminSquadReq("小队")
	req.Visibility = model.VisibilitySystem
	_, err := svc.CreateSystemSquad(context.Background(), Caller{UID: "admin-1"}, req)
	if !errors.Is(err, ErrVisibilityNotAllowed) {
		t.Fatalf("err = %v, want ErrVisibilityNotAllowed", err)
	}
}

func TestGetSystemExpert_NonSystemIsNotFound(t *testing.T) {
	svc, store := newService()
	// A public row must be invisible to the admin get-by-id path.
	store.experts["pub-1"] = &model.Expert{ID: "pub-1", Name: "n", Visibility: model.VisibilityPublic}
	if _, err := svc.GetSystemExpert(context.Background(), "pub-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get public via admin = %v, want ErrNotFound", err)
	}
}

func TestUpdateAndDeleteSystemExpert(t *testing.T) {
	svc, _ := newService()
	caller := Caller{UID: "admin-1", Name: "Admin"}
	created, err := svc.CreateSystemExpert(context.Background(), caller, adminExpertReq("orig"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	newName := "renamed"
	updated, err := svc.UpdateSystemExpert(context.Background(), created.ExpertID, model.ExpertPatchRequest{Name: &newName})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "renamed" || updated.Visibility != model.VisibilitySystem {
		t.Fatalf("update result = %+v", updated)
	}
	if err := svc.DeleteSystemExpert(context.Background(), created.ExpertID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.GetSystemExpert(context.Background(), created.ExpertID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}

func TestListSystemExperts_UsesSystemOnlyFilter(t *testing.T) {
	svc, store := newService()
	store.listExpertResult = []model.Expert{{ID: "e1", Name: "n", Visibility: model.VisibilitySystem}}
	if _, err := svc.ListSystemExperts(context.Background(), ListParams{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !store.lastFilter.SystemOnly {
		t.Fatalf("filter.SystemOnly = false, want true")
	}
	if store.lastFilter.MineOnly {
		t.Fatalf("filter.MineOnly = true, want false")
	}
}

func TestSystemSquadRoundTrip(t *testing.T) {
	svc, _ := newService()
	caller := Caller{UID: "admin-1", Name: "Admin"}
	req := model.SquadCreateRequest{
		Name:     "小队",
		Summary:  "s",
		Category: "研发工具",
		Members: []model.SquadMemberInput{
			{Name: "组长", Role: "leader", IsLeader: true, Instruction: "lead"},
			{Name: "执行", Role: "ic", Instruction: "do"},
		},
	}
	detail, err := svc.CreateSystemSquad(context.Background(), caller, req)
	if err != nil {
		t.Fatalf("create squad: %v", err)
	}
	if detail.Visibility != model.VisibilitySystem || len(detail.Members) != 2 {
		t.Fatalf("squad detail = %+v", detail)
	}
	if _, err := svc.GetSystemSquad(context.Background(), detail.SquadID); err != nil {
		t.Fatalf("get squad: %v", err)
	}
}

func TestAdminCategoryCRUD(t *testing.T) {
	svc, _ := newService()
	ctx := context.Background()

	created, err := svc.CreateCategory(ctx, "自动化", "Bot", 7)
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	if created.ExpertCategoryID == "" || created.Name != "自动化" {
		t.Fatalf("created = %+v", created)
	}
	if _, err := svc.CreateCategory(ctx, "自动化", "", 0); !errors.Is(err, ErrCategoryNameTaken) {
		t.Fatalf("dup category = %v, want ErrCategoryNameTaken", err)
	}
	if _, err := svc.UpdateCategory(ctx, created.ExpertCategoryID, "自动化2", "", 1); err != nil {
		t.Fatalf("update category: %v", err)
	}
	items, err := svc.ListAdminCategories(ctx)
	if err != nil || len(items) != 1 || items[0].Name != "自动化2" {
		t.Fatalf("list = %+v err=%v", items, err)
	}
	if _, err := svc.DeleteCategory(ctx, created.ExpertCategoryID); err != nil {
		t.Fatalf("delete category: %v", err)
	}
}
