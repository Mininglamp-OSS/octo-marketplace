package expert

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	expertrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/expert"
)

// fakeStore is an in-memory Store for exercising the service rules without a
// database. Tag ids are not modeled — the service keeps Tags as names, so the
// fake stores/returns them verbatim.
type fakeStore struct {
	experts map[string]*model.Expert
	squads  map[string]*model.Squad

	createExpertErr error
	updateExpertErr error
	createSquadErr  error

	listExpertResult []model.Expert
	listSquadResult  []model.Squad
	lastFilter       expertrepo.ListFilter

	// category taxonomy fakes. categoryIDByName resolves a NAME to an id on
	// write; categoryNames resolves ids back to names on read; categoryList
	// backs ListCategoriesWithCount.
	categoryIDByName map[string]string
	categoryNames    map[string]string
	categoryList     []expertrepo.CategoryCount
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		experts: map[string]*model.Expert{},
		squads:  map[string]*model.Squad{},
		categoryIDByName: map[string]string{
			"研发工具": "dev-tools",
			"内容创作": "content-creation",
			"营销策划": "marketing-planning",
		},
		categoryNames: map[string]string{
			"dev-tools":          "研发工具",
			"content-creation":   "内容创作",
			"marketing-planning": "营销策划",
		},
	}
}

func (s *fakeStore) CreateExpert(_ context.Context, m *model.Expert) error {
	if s.createExpertErr != nil {
		return s.createExpertErr
	}
	cp := *m
	s.experts[m.ID] = &cp
	return nil
}

func (s *fakeStore) GetExpertByID(_ context.Context, id string) (*model.Expert, error) {
	m, ok := s.experts[id]
	if !ok {
		return nil, expertrepo.ErrNotFound
	}
	cp := *m
	return &cp, nil
}

func (s *fakeStore) ListExperts(_ context.Context, f expertrepo.ListFilter) ([]model.Expert, int, error) {
	s.lastFilter = f
	return s.listExpertResult, len(s.listExpertResult), nil
}

func (s *fakeStore) UpdateExpert(_ context.Context, m *model.Expert) error {
	if s.updateExpertErr != nil {
		return s.updateExpertErr
	}
	if _, ok := s.experts[m.ID]; !ok {
		return expertrepo.ErrNotFound
	}
	cp := *m
	s.experts[m.ID] = &cp
	return nil
}

func (s *fakeStore) DeleteExpert(_ context.Context, id string, _ time.Time) error {
	if _, ok := s.experts[id]; !ok {
		return expertrepo.ErrNotFound
	}
	delete(s.experts, id)
	return nil
}

func (s *fakeStore) CreateSquad(_ context.Context, m *model.Squad) error {
	if s.createSquadErr != nil {
		return s.createSquadErr
	}
	cp := *m
	s.squads[m.ID] = &cp
	return nil
}

func (s *fakeStore) GetSquadByID(_ context.Context, id string) (*model.Squad, error) {
	m, ok := s.squads[id]
	if !ok {
		return nil, expertrepo.ErrNotFound
	}
	cp := *m
	return &cp, nil
}

func (s *fakeStore) ListSquads(_ context.Context, f expertrepo.ListFilter) ([]model.Squad, int, error) {
	s.lastFilter = f
	return s.listSquadResult, len(s.listSquadResult), nil
}

func (s *fakeStore) UpdateSquad(_ context.Context, m *model.Squad) error {
	if _, ok := s.squads[m.ID]; !ok {
		return expertrepo.ErrNotFound
	}
	cp := *m
	s.squads[m.ID] = &cp
	return nil
}

func (s *fakeStore) DeleteSquad(_ context.Context, id string, _ time.Time) error {
	if _, ok := s.squads[id]; !ok {
		return expertrepo.ErrNotFound
	}
	delete(s.squads, id)
	return nil
}

func (s *fakeStore) ListTags(_ context.Context, _ expertrepo.TagListFilter) ([]model.TagFilter, error) {
	return nil, nil
}

func (s *fakeStore) ResolveFilterTagIDs(_ context.Context, _ string, tags []string) ([][]int64, error) {
	groups := make([][]int64, 0, len(tags))
	for range tags {
		groups = append(groups, []int64{1})
	}
	return groups, nil
}

func (s *fakeStore) CategoryIDByName(_ context.Context, name string) (string, error) {
	return s.categoryIDByName[strings.TrimSpace(name)], nil
}

func (s *fakeStore) CategoryNamesByIDs(_ context.Context, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if n, ok := s.categoryNames[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

func (s *fakeStore) ListCategoriesWithCount(_ context.Context, _ expertrepo.Entity, _, _ string) ([]expertrepo.CategoryCount, error) {
	return s.categoryList, nil
}

func newService() (*Service, *fakeStore) {
	store := newFakeStore()
	seq := 0
	svc := New(store, nil, func() string {
		seq++
		return "id-" + string(rune('a'+seq))
	})
	svc.now = func() time.Time { return time.Date(2026, 8, 6, 10, 15, 0, 123_000_000, time.UTC) }
	return svc, store
}

var callerA = Caller{UID: "u1", Name: "王决", SpaceID: "space-a"}
var callerB = Caller{UID: "u2", Name: "林澈", SpaceID: "space-b"}

func baseExpert() model.ExpertCreateRequest {
	return model.ExpertCreateRequest{
		Name:        "后端架构师",
		Summary:     "评审服务边界。",
		Category:    "研发工具",
		Tags:        []string{" 架构评审 ", "架构评审", "可靠性", ""},
		Instruction: "你是资深后端架构师。",
		MCPConfig:   `{"mcpServers":{}}`,
		Skills:      []model.SkillWrite{{Name: "架构评审清单"}, {Name: "容量估算模板"}},
	}
}

func TestCreateExpertStampsIdentityAndDerivations(t *testing.T) {
	svc, store := newService()
	detail, err := svc.CreateExpert(context.Background(), callerA, baseExpert())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.experts[detail.ExpertID].OwnerUID != "u1" || store.experts[detail.ExpertID].SpaceID != "space-a" {
		t.Fatalf("identity not stamped: %+v", store.experts[detail.ExpertID])
	}
	if detail.CreatorName != "王决" {
		t.Fatalf("creator_name = %q", detail.CreatorName)
	}
	if detail.Visibility != model.VisibilityPublic {
		t.Fatalf("visibility = %q, want public", detail.Visibility)
	}
	if detail.CreatedByType != model.CreatedByHuman {
		t.Fatalf("created_by_type = %q, want human", detail.CreatedByType)
	}
	if detail.ShortName != "后端" {
		t.Fatalf("short_name = %q, want 后端", detail.ShortName)
	}
	if len(detail.Tags) != 2 || detail.Tags[0] != "架构评审" || detail.Tags[1] != "可靠性" {
		t.Fatalf("tags not normalized: %#v", detail.Tags)
	}
	if detail.CreatedAt != "2026-08-06T10:15:00.123Z" {
		t.Fatalf("created_at = %q", detail.CreatedAt)
	}
	if len(detail.Skills) != 2 {
		t.Fatalf("skills = %#v", detail.Skills)
	}
}

func TestCreateExpertBotProvenance(t *testing.T) {
	svc, _ := newService()
	bot := Caller{UID: "u1", Name: "王决", SpaceID: "space-a", BotUID: "bot_1", BotName: "研发助手"}
	detail, err := svc.CreateExpert(context.Background(), bot, baseExpert())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.CreatedByType != model.CreatedByBot ||
		detail.CreatedByBotUID != "bot_1" || detail.CreatedByBotName != "研发助手" {
		t.Fatalf("bot provenance not stamped: %+v", detail)
	}
}

func TestCreateExpertRejectsSystemVisibility(t *testing.T) {
	svc, _ := newService()
	req := baseExpert()
	req.Visibility = model.VisibilitySystem
	if _, err := svc.CreateExpert(context.Background(), callerA, req); !errors.Is(err, ErrInvalidVisibility) {
		t.Fatalf("err = %v, want ErrInvalidVisibility", err)
	}
}

func TestCreateExpertRequiresNameAndSummary(t *testing.T) {
	svc, _ := newService()
	req := baseExpert()
	req.Name = "   "
	if _, err := svc.CreateExpert(context.Background(), callerA, req); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty name err = %v, want ErrInvalidRequest", err)
	}
}

func TestCreateExpertCategoryMustBeLive(t *testing.T) {
	svc, _ := newService()
	req := baseExpert()
	req.Category = "ghost" // unknown name → resolves to no id
	if _, err := svc.CreateExpert(context.Background(), callerA, req); !errors.Is(err, ErrCategoryNotFound) {
		t.Fatalf("err = %v, want ErrCategoryNotFound", err)
	}
	req.Category = ""
	if _, err := svc.CreateExpert(context.Background(), callerA, req); !errors.Is(err, ErrCategoryNotFound) {
		t.Fatalf("empty category err = %v, want ErrCategoryNotFound", err)
	}
}

// TestCreateExpertResolvesCategoryNameToID verifies the wire carries the NAME
// while storage carries the resolved id (doc §5).
func TestCreateExpertResolvesCategoryNameToID(t *testing.T) {
	svc, store := newService()
	detail, err := svc.CreateExpert(context.Background(), callerA, baseExpert())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Category != "研发工具" {
		t.Fatalf("response category = %q, want NAME 研发工具", detail.Category)
	}
	if got := store.experts[detail.ExpertID].Category; got != "dev-tools" {
		t.Fatalf("stored category = %q, want id dev-tools", got)
	}
}

// TestGetExpertResolvesCategoryIDToName verifies a stored id is surfaced as the
// category NAME on read.
func TestGetExpertResolvesCategoryIDToName(t *testing.T) {
	svc, store := newService()
	store.experts["e1"] = &model.Expert{
		ID: "e1", Name: "x", OwnerUID: "u1", SpaceID: "space-a",
		Visibility: model.VisibilityPublic, Category: "content-creation",
	}
	detail, err := svc.GetExpert(context.Background(), callerA, "e1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Category != "内容创作" {
		t.Fatalf("read category = %q, want NAME 内容创作", detail.Category)
	}
}

// TestListCategories maps repo counts onto the wire item shape.
func TestListCategories(t *testing.T) {
	svc, store := newService()
	store.categoryList = []expertrepo.CategoryCount{
		{ID: "marketing-planning", Name: "营销策划", Count: 3},
		{ID: "dev-tools", Name: "研发工具", Count: 0},
	}
	items, err := svc.ListCategories(context.Background(), callerA, expertrepo.EntityExpert)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].ExpertCategoryID != "marketing-planning" || items[0].Name != "营销策划" || items[0].Count != 3 {
		t.Fatalf("item[0] = %+v", items[0])
	}
	if items[1].Count != 0 {
		t.Fatalf("zero-count category must still be returned: %+v", items[1])
	}
}

func TestCreateExpertMCPConfigValidation(t *testing.T) {
	svc, _ := newService()
	req := baseExpert()
	req.MCPConfig = "{not valid json"
	if _, err := svc.CreateExpert(context.Background(), callerA, req); !errors.Is(err, ErrInvalidMCPConfig) {
		t.Fatalf("malformed mcp_config err = %v, want ErrInvalidMCPConfig", err)
	}
	req.MCPConfig = "{\"a\":\"" + strings.Repeat("x", model.MaxMCPConfigBytes) + "\"}"
	if _, err := svc.CreateExpert(context.Background(), callerA, req); !errors.Is(err, ErrInvalidMCPConfig) {
		t.Fatalf("oversize mcp_config err = %v, want ErrInvalidMCPConfig", err)
	}
	// Empty mcp_config is allowed.
	req.MCPConfig = ""
	if _, err := svc.CreateExpert(context.Background(), callerA, req); err != nil {
		t.Fatalf("empty mcp_config should be allowed, got %v", err)
	}
}

func TestCreateExpertNameTaken(t *testing.T) {
	svc, store := newService()
	store.createExpertErr = expertrepo.ErrNameTaken
	if _, err := svc.CreateExpert(context.Background(), callerA, baseExpert()); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("err = %v, want ErrNameTaken", err)
	}
}

func TestGetExpertCrossSpacePublicIsNotFound(t *testing.T) {
	svc, store := newService()
	// A public expert owned by u2 in space-b.
	store.experts["e1"] = &model.Expert{
		ID: "e1", Name: "x", OwnerUID: "u2", SpaceID: "space-b", Visibility: model.VisibilityPublic,
	}
	if _, err := svc.GetExpert(context.Background(), callerA, "e1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-space public read err = %v, want ErrNotFound (never Forbidden)", err)
	}
}

func TestGetExpertPrivateOfAnotherUserIsNotFound(t *testing.T) {
	svc, store := newService()
	// Private expert owned by u2 in the SAME space as caller.
	store.experts["e1"] = &model.Expert{
		ID: "e1", Name: "x", OwnerUID: "u2", SpaceID: "space-a", Visibility: model.VisibilityPrivate,
	}
	if _, err := svc.GetExpert(context.Background(), callerA, "e1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private-of-another err = %v, want ErrNotFound", err)
	}
}

func TestGetExpertSystemVisibleAcrossSpace(t *testing.T) {
	svc, store := newService()
	store.experts["e1"] = &model.Expert{
		ID: "e1", Name: "x", OwnerUID: "u9", SpaceID: "", Visibility: model.VisibilitySystem,
	}
	if _, err := svc.GetExpert(context.Background(), callerA, "e1"); err != nil {
		t.Fatalf("system record should be visible, got %v", err)
	}
}

func TestPatchExpertVisibleNotOwnedIsForbidden(t *testing.T) {
	svc, store := newService()
	// Public expert owned by u2 in caller's space → visible but not owned.
	store.experts["e1"] = &model.Expert{
		ID: "e1", Name: "x", Summary: "y", OwnerUID: "u2", SpaceID: "space-a", Visibility: model.VisibilityPublic,
	}
	newName := "renamed"
	_, err := svc.PatchExpert(context.Background(), callerA, "e1", model.ExpertPatchRequest{Name: &newName})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("patch not-owned err = %v, want ErrForbidden", err)
	}
}

func TestDeleteExpertCrossSpaceIsNotFound(t *testing.T) {
	svc, store := newService()
	store.experts["e1"] = &model.Expert{
		ID: "e1", OwnerUID: "u2", SpaceID: "space-b", Visibility: model.VisibilityPublic,
	}
	if err := svc.DeleteExpert(context.Background(), callerA, "e1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-space delete err = %v, want ErrNotFound", err)
	}
}

func TestPatchExpertOwnerSucceeds(t *testing.T) {
	svc, store := newService()
	store.experts["e1"] = &model.Expert{
		ID: "e1", Name: "old", Summary: "s", OwnerUID: "u1", SpaceID: "space-a", Visibility: model.VisibilityPublic,
	}
	newName := "新名字"
	detail, err := svc.PatchExpert(context.Background(), callerA, "e1", model.ExpertPatchRequest{Name: &newName})
	if err != nil {
		t.Fatalf("owner patch failed: %v", err)
	}
	if detail.Name != "新名字" || detail.ShortName != "新名" {
		t.Fatalf("patch result wrong: name=%q short=%q", detail.Name, detail.ShortName)
	}
}

// ─── Squad tests ─────────────────────────────────────────────────────────────

func baseSquad() model.SquadCreateRequest {
	return model.SquadCreateRequest{
		Name:     "软件研发交付团",
		Summary:  "从需求到测试的协作链路。",
		Category: "研发工具",
		Tags:     []string{"需求分析", "前后端开发"},
		Strategies: []string{
			"技术负责人先澄清需求。",
		},
		Dependencies: model.SquadDependencies{Blocking: []string{"git-mcp"}, Recommended: []string{"GPT-5.2"}},
		Permission:   "读取工作区文件",
		Members: []model.SquadMemberInput{
			{Name: "技术负责人", Role: "拆解方案", IsLeader: true, Instruction: "你是……", MCPConfig: `{"mcpServers":{}}`, Skills: []model.SkillWrite{{Name: "架构评审清单"}}},
			{Name: "后端工程师", Role: "实现服务", Instruction: "你是后端工程师。", Skills: []model.SkillWrite{{Name: "Go"}}},
		},
	}
}

func TestCreateSquadDefaultsAndOrder(t *testing.T) {
	svc, store := newService()
	detail, err := svc.CreateSquad(context.Background(), callerA, baseSquad())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stored := store.squads[detail.SquadID]
	if len(stored.Members) != 2 {
		t.Fatalf("member count = %d", len(stored.Members))
	}
	// Order preserved, full ExpertSpec round-trips.
	if detail.Members[0].Name != "技术负责人" || detail.Members[1].Name != "后端工程师" {
		t.Fatalf("member order not preserved: %+v", detail.Members)
	}
	if detail.Members[0].Instruction != "你是……" || detail.Members[0].MCPConfig != `{"mcpServers":{}}` {
		t.Fatalf("member 0 spec lost: %+v", detail.Members[0])
	}
	if len(detail.Members[0].Skills) != 1 || detail.Members[0].Skills[0].Name != "架构评审清单" {
		t.Fatalf("member 0 skills lost: %+v", detail.Members[0].Skills)
	}
	// Defaults: member_key / template_id filled.
	if detail.Members[1].MemberKey != "member_02" {
		t.Fatalf("member_key default = %q, want member_02", detail.Members[1].MemberKey)
	}
	if !strings.HasPrefix(detail.Members[1].TemplateID, "expert-"+detail.SquadID+"-") {
		t.Fatalf("template_id default = %q", detail.Members[1].TemplateID)
	}
	// Leader derived from flagged member.
	if detail.Leader != "技术负责人" {
		t.Fatalf("leader = %q, want 技术负责人", detail.Leader)
	}
}

func TestCreateSquadRequiresMembers(t *testing.T) {
	svc, _ := newService()
	req := baseSquad()
	req.Members = nil
	if _, err := svc.CreateSquad(context.Background(), callerA, req); !errors.Is(err, ErrInvalidMembers) {
		t.Fatalf("no members err = %v, want ErrInvalidMembers", err)
	}
}

func TestCreateSquadMemberMissingRole(t *testing.T) {
	svc, _ := newService()
	req := baseSquad()
	req.Members[1].Role = "  "
	if _, err := svc.CreateSquad(context.Background(), callerA, req); !errors.Is(err, ErrInvalidMembers) {
		t.Fatalf("missing role err = %v, want ErrInvalidMembers", err)
	}
}

func TestCreateSquadMemberBadMCPConfig(t *testing.T) {
	svc, _ := newService()
	req := baseSquad()
	req.Members[0].MCPConfig = "{bad"
	if _, err := svc.CreateSquad(context.Background(), callerA, req); !errors.Is(err, ErrInvalidMembers) {
		t.Fatalf("bad member mcp_config err = %v, want ErrInvalidMembers", err)
	}
}

func TestCreateSquadNoLeaderFlagsFirst(t *testing.T) {
	svc, _ := newService()
	req := baseSquad()
	req.Members[0].IsLeader = false
	req.Leader = ""
	detail, err := svc.CreateSquad(context.Background(), callerA, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !detail.Members[0].IsLeader {
		t.Fatalf("first member should be flagged leader when none supplied")
	}
	if detail.Leader != "技术负责人" {
		t.Fatalf("leader = %q, want first member name", detail.Leader)
	}
}

func TestPatchSquadMembersFullReplace(t *testing.T) {
	svc, store := newService()
	created, err := svc.CreateSquad(context.Background(), callerA, baseSquad())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	replacement := []model.SquadMemberInput{{Name: "唯一成员", Role: "全栈", Instruction: "你是全栈工程师。"}}
	detail, err := svc.PatchSquad(context.Background(), callerA, created.SquadID, model.SquadPatchRequest{Members: &replacement})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if len(detail.Members) != 1 || detail.Members[0].Name != "唯一成员" {
		t.Fatalf("members not full-replaced: %+v", detail.Members)
	}
	if !detail.Members[0].IsLeader || detail.Leader != "唯一成员" {
		t.Fatalf("leader not re-derived: leader=%q member=%+v", detail.Leader, detail.Members[0])
	}
	if len(store.squads[created.SquadID].Members) != 1 {
		t.Fatalf("store not updated to full-replaced set")
	}
}

func TestGetSquadCrossSpacePublicIsNotFound(t *testing.T) {
	svc, store := newService()
	store.squads["s1"] = &model.Squad{ID: "s1", OwnerUID: "u2", SpaceID: "space-b", Visibility: model.VisibilityPublic}
	if _, err := svc.GetSquad(context.Background(), callerA, "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-space squad err = %v, want ErrNotFound", err)
	}
}

func TestListMineFilterScoping(t *testing.T) {
	svc, store := newService()
	_, _ = svc.ListExpertsMine(context.Background(), callerB, ListParams{})
	if !store.lastFilter.MineOnly || store.lastFilter.CallerUID != "u2" || store.lastFilter.SpaceID != "space-b" {
		t.Fatalf("mine filter not scoped: %+v", store.lastFilter)
	}
}

// A system row is globally visible AND may carry an owner_uid equal to a real
// caller; the public mutation paths must still refuse to touch it (system rows
// are admin-managed). These four assert the system exclusion on top of the
// ownership check.

func TestPatchExpertSystemRowForbiddenEvenForOwner(t *testing.T) {
	svc, store := newService()
	store.experts["e1"] = &model.Expert{
		ID: "e1", Name: "x", Summary: "y", OwnerUID: "u1", SpaceID: "", Visibility: model.VisibilitySystem,
	}
	newName := "renamed"
	if _, err := svc.PatchExpert(context.Background(), callerA, "e1", model.ExpertPatchRequest{Name: &newName}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("patch system expert err = %v, want ErrForbidden", err)
	}
}

func TestDeleteExpertSystemRowForbiddenEvenForOwner(t *testing.T) {
	svc, store := newService()
	store.experts["e1"] = &model.Expert{
		ID: "e1", Name: "x", OwnerUID: "u1", SpaceID: "", Visibility: model.VisibilitySystem,
	}
	if err := svc.DeleteExpert(context.Background(), callerA, "e1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delete system expert err = %v, want ErrForbidden", err)
	}
}

func TestPatchSquadSystemRowForbiddenEvenForOwner(t *testing.T) {
	svc, store := newService()
	store.squads["s1"] = &model.Squad{
		ID: "s1", Name: "x", Summary: "y", OwnerUID: "u1", SpaceID: "", Visibility: model.VisibilitySystem,
	}
	newName := "renamed"
	if _, err := svc.PatchSquad(context.Background(), callerA, "s1", model.SquadPatchRequest{Name: &newName}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("patch system squad err = %v, want ErrForbidden", err)
	}
}

func TestDeleteSquadSystemRowForbiddenEvenForOwner(t *testing.T) {
	svc, store := newService()
	store.squads["s1"] = &model.Squad{
		ID: "s1", OwnerUID: "u1", SpaceID: "", Visibility: model.VisibilitySystem,
	}
	if err := svc.DeleteSquad(context.Background(), callerA, "s1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delete system squad err = %v, want ErrForbidden", err)
	}
}

func TestCreateExpertTagCountLimit(t *testing.T) {
	svc, _ := newService()
	tags := make([]string, 0, model.MaxExpertTags+1)
	for i := 0; i <= model.MaxExpertTags; i++ {
		tags = append(tags, string(rune('a'+i)))
	}
	_, err := svc.CreateExpert(context.Background(), callerA, model.ExpertCreateRequest{
		Name: "n", Summary: "s", Category: "研发工具", Instruction: "i", Tags: tags,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("tag count over limit err = %v, want ErrInvalidRequest", err)
	}
}

func TestCreateExpertTagLengthLimit(t *testing.T) {
	svc, _ := newService()
	long := strings.Repeat("a", model.MaxExpertTagNameLen+1)
	_, err := svc.CreateExpert(context.Background(), callerA, model.ExpertCreateRequest{
		Name: "n", Summary: "s", Category: "研发工具", Instruction: "i", Tags: []string{long},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("tag length over limit err = %v, want ErrInvalidRequest", err)
	}
}
