package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/repository"
)

// fakeStore is an in-memory Store for exercising the service rules without a
// database. It records the last created/updated record and can inject errors.
type fakeStore struct {
	records map[string]*model.MCP

	createErr error
	updateErr error

	created *model.MCP
	updated *model.MCP
	deleted string

	lastFilter repository.ListFilter
	listResult []model.MCP
	listTotal  int
	listCats   []model.CategoryFilter
}

func newFakeStore() *fakeStore {
	return &fakeStore{records: map[string]*model.MCP{}}
}

func (s *fakeStore) Create(_ context.Context, m *model.MCP) error {
	if s.createErr != nil {
		return s.createErr
	}
	cp := *m
	s.created = &cp
	s.records[m.ID] = &cp
	return nil
}

func (s *fakeStore) Update(_ context.Context, m *model.MCP) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	cp := *m
	s.updated = &cp
	s.records[m.ID] = &cp
	return nil
}

func (s *fakeStore) GetByID(_ context.Context, id string) (*model.MCP, error) {
	m, ok := s.records[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *m
	return &cp, nil
}

func (s *fakeStore) SoftDelete(_ context.Context, id string, _ time.Time) error {
	if _, ok := s.records[id]; !ok {
		return repository.ErrNotFound
	}
	s.deleted = id
	delete(s.records, id)
	return nil
}

func (s *fakeStore) List(_ context.Context, f repository.ListFilter) ([]model.MCP, int, []model.CategoryFilter, error) {
	s.lastFilter = f
	return s.listResult, s.listTotal, s.listCats, nil
}

// SystemNameExists / SystemSlugExists back the admin uniqueness pre-check.
// Scans the in-memory records for live visibility=system rows that share the
// name/slug, mirroring the repository query in Store.
func (s *fakeStore) SystemNameExists(_ context.Context, name, exceptID string) (bool, error) {
	for id, r := range s.records {
		if id == exceptID {
			continue
		}
		if r.Visibility == model.VisibilitySystem && r.Name == name && r.DeletedAt == nil {
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeStore) SystemSlugExists(_ context.Context, slug, exceptID string) (bool, error) {
	for id, r := range s.records {
		if id == exceptID {
			continue
		}
		if r.Visibility == model.VisibilitySystem && r.Slug == slug && r.DeletedAt == nil {
			return true, nil
		}
	}
	return false, nil
}

func fixedClock(svc *Service) {
	svc.now = func() time.Time { return time.Date(2026, 7, 14, 18, 30, 12, 123_000_000, time.UTC) }
}

var caller = Caller{UID: "u1", Name: "李世超", SpaceID: "space-a"}

func baseCreate() model.CreateRequest {
	return model.CreateRequest{
		Name:       "GitHub MCP",
		Category:   "dev",
		Transport:  model.TransportStreamableHTTP,
		URL:        "https://mcp.example.com/github",
		AuthType:   "bearer",
		Visibility: model.VisibilityPublic,
		Tools:      []model.Tool{{Name: "list_repositories", Description: "列出仓库"}},
	}
}

func TestCreateStampsIdentityAndMapsToDetail(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	fixedClock(svc)

	req := baseCreate()
	req.Tags = []string{" 官方 ", "官方", "热门", ""} // trim + dedupe + drop empty

	detail, apiErr := svc.Create(context.Background(), caller, req)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}

	// Identity is server-stamped, never from the body.
	if store.created.OwnerUID != "u1" || store.created.SpaceID != "space-a" {
		t.Fatalf("owner/space not stamped: %+v", store.created)
	}
	if detail.CreatorName != "李世超" {
		t.Fatalf("creatorName = %q, want 李世超", detail.CreatorName)
	}
	// A plain user-token create resolves to CreatedByHuman with the two Bot
	// fields empty (issue #894).
	if detail.CreatedByType != model.CreatedByHuman {
		t.Fatalf("createdByType = %q, want human", detail.CreatedByType)
	}
	if detail.CreatedByBotUID != "" || detail.CreatedByBotName != "" {
		t.Fatalf("bot fields should be empty on human create: %+v", detail)
	}
	// Tags normalized.
	if len(detail.Tags) != 2 || detail.Tags[0] != "官方" || detail.Tags[1] != "热门" {
		t.Fatalf("tags not normalized: %#v", detail.Tags)
	}
	// Flat -> nested mapping.
	if detail.QuickStart.Transport != model.TransportStreamableHTTP ||
		detail.QuickStart.URL != "https://mcp.example.com/github" ||
		detail.QuickStart.ServerName != "GitHub MCP" {
		t.Fatalf("quickStart mapping wrong: %+v", detail.QuickStart)
	}
	if detail.ToolCount != 1 {
		t.Fatalf("toolCount = %d, want 1", detail.ToolCount)
	}
	// Timestamps in RFC3339 ms.
	if detail.CreatedAt != "2026-07-14T18:30:12.123Z" {
		t.Fatalf("createdAt = %q", detail.CreatedAt)
	}
}

// TestCreateStampsBotProvenance verifies that when the request rode in on a
// Bot token — expressed at the service boundary by a Caller with non-empty
// BotUID / BotName — the persisted row carries CreatedByType=bot and the two
// snapshot fields. Owner identity is still stamped from the Caller (BotIdentity
// collapses into the owner Identity in middleware), so the badge does not
// change who owns the row.
func TestCreateStampsBotProvenance(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	fixedClock(svc)
	botCaller := caller
	botCaller.BotUID = "bot_01HXYZ"
	botCaller.BotName = "GitHub Autoposter"

	detail, apiErr := svc.Create(context.Background(), botCaller, baseCreate())
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if detail.CreatedByType != model.CreatedByBot {
		t.Fatalf("createdByType = %q, want bot", detail.CreatedByType)
	}
	if detail.CreatedByBotUID != "bot_01HXYZ" || detail.CreatedByBotName != "GitHub Autoposter" {
		t.Fatalf("bot fields not stamped: %+v", detail)
	}
	if store.created.OwnerUID != "u1" || store.created.CreatorName != "李世超" {
		t.Fatalf("owner still stamped from Caller, not Bot: %+v", store.created)
	}
}

func TestCreateRejectsSystemVisibility(t *testing.T) {
	svc := New(newFakeStore())
	req := baseCreate()
	req.Visibility = model.VisibilitySystem
	_, apiErr := svc.Create(context.Background(), caller, req)
	if apiErr == nil || apiErr.Code != apierr.CodeInvalidVisibility {
		t.Fatalf("expected invalid_visibility, got %v", apiErr)
	}
}

func TestCreateRejectsInvalidTransport(t *testing.T) {
	svc := New(newFakeStore())
	req := baseCreate()
	req.Transport = "grpc"
	_, apiErr := svc.Create(context.Background(), caller, req)
	if apiErr == nil || apiErr.Code != apierr.CodeInvalidTransport {
		t.Fatalf("expected invalid_transport, got %v", apiErr)
	}
}

func TestCreateRequiresName(t *testing.T) {
	svc := New(newFakeStore())
	req := baseCreate()
	req.Name = "   "
	_, apiErr := svc.Create(context.Background(), caller, req)
	if apiErr == nil || apiErr.Code != apierr.CodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %v", apiErr)
	}
}

// --- Secret redaction: positive AND negative (Acceptance) ---

func TestCreateSentinelAndBlankSecretsAccepted(t *testing.T) {
	store := newFakeStore()
	svc := New(store)

	req := baseCreate()
	req.Transport = model.TransportStdio
	req.Command = "npx"
	req.URL = ""
	req.Env = map[string]string{
		"GITHUB_TOKEN": model.SecretPlaceholderSentinel,
		"API_KEY":      "",
		"REGION":       "us-east-1", // non-secret passes through
	}
	req.EnvUserSupplied = []string{"GITHUB_TOKEN", "API_KEY"}
	req.Headers = map[string]string{
		"Authorization": model.SecretPlaceholderSentinel,
		"X-Trace":       "web",
	}
	req.HeadersUserSupplied = []string{"Authorization"}

	detail, apiErr := svc.Create(context.Background(), caller, req)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	// User-supplied keys survive as empty strings (backend accepts sentinel/blank).
	if store.created.Connection.Env["GITHUB_TOKEN"] != "" {
		t.Fatalf("GITHUB_TOKEN not blanked: %q", store.created.Connection.Env["GITHUB_TOKEN"])
	}
	if store.created.Connection.Headers["Authorization"] != "" {
		t.Fatalf("Authorization not blanked: %q", store.created.Connection.Headers["Authorization"])
	}
	if store.created.Connection.Env["REGION"] != "us-east-1" {
		t.Fatalf("non-secret REGION altered: %q", store.created.Connection.Env["REGION"])
	}
	// user_supplied lists round-trip on the response so the frontend can
	// rebuild its per-row toggle state.
	if got := detail.QuickStart.EnvUserSupplied; len(got) != 2 {
		t.Fatalf("env_user_supplied not echoed: %#v", got)
	}
	if got := detail.QuickStart.HeadersUserSupplied; len(got) != 1 || got[0] != "Authorization" {
		t.Fatalf("headers_user_supplied not echoed: %#v", got)
	}
	// Response never re-surfaces a value for a user-supplied key.
	if detail.QuickStart.Env["GITHUB_TOKEN"] != "" {
		t.Fatalf("response leaked GITHUB_TOKEN: %q", detail.QuickStart.Env["GITHUB_TOKEN"])
	}
	if detail.QuickStart.Headers["X-Trace"] != "web" {
		t.Fatalf("non-secret header dropped: %#v", detail.QuickStart.Headers)
	}
}

func TestCreateAcceptsPlaintextOnUserSuppliedKey(t *testing.T) {
	// Post §5.1-relaxation: user-supplied keys carry a real value verbatim
	// so the owner can pre-fill their own edit form later. Non-owner
	// blanking (detailForCaller / §5.3) is the sole guardrail keeping the
	// value out of consumer-facing responses.
	store := newFakeStore()
	svc := New(store)

	req := baseCreate()
	req.Transport = model.TransportStdio
	req.Command = "npx"
	req.URL = ""
	req.Env = map[string]string{"GITHUB_TOKEN": "ghp_realTokenPastedByAuthor"}
	req.EnvUserSupplied = []string{"GITHUB_TOKEN"}

	_, apiErr := svc.Create(context.Background(), caller, req)
	if apiErr != nil {
		t.Fatalf("expected accept, got apiErr = %v", apiErr)
	}
	if store.created == nil {
		t.Fatalf("record not persisted")
	}
	if store.created.Connection.Env["GITHUB_TOKEN"] != "ghp_realTokenPastedByAuthor" {
		t.Fatalf(
			"user-supplied value not preserved: %q",
			store.created.Connection.Env["GITHUB_TOKEN"],
		)
	}
}

func TestCreatePublicAcceptsSharedSecretValue(t *testing.T) {
	// Post-relaxation: rule 2 (public_secret_disallowed) is removed. A shared
	// secret-shaped value on a public record is now persisted verbatim. The
	// value is still owner-only via detailForCaller (§5.3) blanking — no
	// consumer sees it through the API.
	store := newFakeStore()
	svc := New(store)
	req := baseCreate() // Visibility public
	req.Headers = map[string]string{"Authorization": "Bearer sk-live-abc"}
	if _, apiErr := svc.Create(context.Background(), caller, req); apiErr != nil {
		t.Fatalf("public shared secret should be accepted, got %v", apiErr)
	}
	if store.created == nil {
		t.Fatalf("record not persisted")
	}
	if got := store.created.Connection.Headers["Authorization"]; got != "Bearer sk-live-abc" {
		t.Fatalf("Authorization not preserved: %q", got)
	}
}

func TestCreatePrivateAcceptsSharedSecretValue(t *testing.T) {
	// Same request shape, private visibility → allowed. This is the whole
	// point of the toggle model: a private MCP is essentially a personal
	// config file and can persist a real credential.
	store := newFakeStore()
	svc := New(store)
	req := baseCreate()
	req.Visibility = model.VisibilityPrivate
	req.Headers = map[string]string{"Authorization": "Bearer sk-live-abc"}
	if _, apiErr := svc.Create(context.Background(), caller, req); apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if got := store.created.Connection.Headers["Authorization"]; got != "Bearer sk-live-abc" {
		t.Fatalf("private shared secret was not persisted verbatim: %q", got)
	}
}

// --- Visibility / cross-Space (Acceptance) ---

func seed(store *fakeStore, m model.MCP) {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
		m.UpdatedAt = m.CreatedAt
	}
	store.records[m.ID] = &m
}

func TestGetCrossSpacePublicIsNotFound(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{ID: "x", Name: "B's MCP", Visibility: model.VisibilityPublic, OwnerUID: "u2", SpaceID: "space-b"})

	_, apiErr := svc.Get(context.Background(), caller, "x") // caller is in space-a
	if apiErr == nil || apiErr.Code != apierr.CodeNotFound {
		t.Fatalf("expected not_found for cross-space public, got %v", apiErr)
	}
}

func TestGetPrivateOfAnotherUserIsNotFound(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{ID: "x", Visibility: model.VisibilityPrivate, OwnerUID: "u2", SpaceID: "space-a"})

	_, apiErr := svc.Get(context.Background(), caller, "x")
	if apiErr == nil || apiErr.Code != apierr.CodeNotFound {
		t.Fatalf("expected not_found for other's private, got %v", apiErr)
	}
}

func TestGetPublicPeerBlanksConnectionValues(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{
		ID:         "x",
		Name:       "Peer MCP",
		Visibility: model.VisibilityPublic,
		OwnerUID:   "u2",
		SpaceID:    "space-a",
		Transport:  model.TransportStreamableHTTP,
		Connection: model.Connection{
			URL:     "https://mcp.example.com",
			Env:     map[string]string{"REGION": "us-east-1", "GOOGLE_APPLICATION_CREDENTIALS_JSON": ""},
			Headers: map[string]string{"X-Trace": "web"},
		},
	})

	detail, apiErr := svc.Get(context.Background(), caller, "x")
	if apiErr != nil {
		t.Fatalf("public peer should be visible, got %v", apiErr)
	}
	if detail.QuickStart.URL != "https://mcp.example.com" {
		t.Fatalf("url should still be visible, got %+v", detail.QuickStart)
	}
	if got := detail.QuickStart.Env["REGION"]; got != "" {
		t.Fatalf("env value should be blanked for non-owner, got %q", got)
	}
	if got := detail.QuickStart.Env["GOOGLE_APPLICATION_CREDENTIALS_JSON"]; got != "" {
		t.Fatalf("secret-shaped env key should be blanked for non-owner, got %q", got)
	}
	if got := detail.QuickStart.Headers["X-Trace"]; got != "" {
		t.Fatalf("header value should be blanked for non-owner, got %q", got)
	}
}

func TestGetUserSuppliedValuePreservedForOwnerBlankedForPeer(t *testing.T) {
	// Post-§5.1-relaxation defense line: owner sees the value they persisted
	// under a *_user_supplied key on their own edit; a peer (non-owner) sees
	// it blanked by detailForCaller. Regressing the blanking here would leak
	// author tokens to consumers, so lock the invariant with an explicit test.
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{
		ID:         "x",
		Name:       "Owner-only reference value",
		Visibility: model.VisibilityPublic,
		OwnerUID:   "u1", // matches `caller` (the default fake owner in this file)
		SpaceID:    "space-a",
		Transport:  model.TransportStreamableHTTP,
		Connection: model.Connection{
			URL:                 "https://mcp.example.com",
			Headers:             map[string]string{"Authorization": "Bearer author-token"},
			HeadersUserSupplied: []string{"Authorization"},
		},
	})

	// Owner sees the value verbatim.
	ownerDetail, apiErr := svc.Get(context.Background(), caller, "x")
	if apiErr != nil {
		t.Fatalf("owner Get failed: %v", apiErr)
	}
	if got := ownerDetail.QuickStart.Headers["Authorization"]; got != "Bearer author-token" {
		t.Fatalf("owner should see user-supplied value verbatim, got %q", got)
	}
	if !contains(ownerDetail.QuickStart.HeadersUserSupplied, "Authorization") {
		t.Fatalf("owner should see the user-supplied array, got %#v", ownerDetail.QuickStart.HeadersUserSupplied)
	}

	// Peer (different UID, same space, public visibility) sees blank.
	peer := Caller{UID: "u-peer", SpaceID: "space-a"}
	peerDetail, apiErr := svc.Get(context.Background(), peer, "x")
	if apiErr != nil {
		t.Fatalf("peer Get failed: %v", apiErr)
	}
	if got := peerDetail.QuickStart.Headers["Authorization"]; got != "" {
		t.Fatalf(
			"peer must NOT see the user-supplied value — this is the sole "+
				"defense line for author tokens, got %q",
			got,
		)
	}
	// The key + user-supplied array are still visible; only the VALUE is
	// blanked. Peer needs the key so their frontend renders "Authorization:
	// <TOKEN>" in the copy-paste snippet.
	if _, ok := peerDetail.QuickStart.Headers["Authorization"]; !ok {
		t.Fatalf("peer should still see the header KEY, got %#v", peerDetail.QuickStart.Headers)
	}
	if !contains(peerDetail.QuickStart.HeadersUserSupplied, "Authorization") {
		t.Fatalf("peer should still see the user-supplied array, got %#v", peerDetail.QuickStart.HeadersUserSupplied)
	}
}

func TestGetPublicPeerBlanksSharedSecretValue(t *testing.T) {
	// Post-rule-2-removal: a public MCP may persist a real secret-shaped value
	// under a NON user-supplied key (author decided to publish a "shared"
	// value). The invariant is that a non-owner still gets it blanked by
	// detailForCaller — this is the sole defense line for the author's token.
	// Regressing the blanking here would leak the token to every consumer.
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{
		ID:         "shared",
		Name:       "Shared secret on public",
		Visibility: model.VisibilityPublic,
		OwnerUID:   "u1",
		SpaceID:    "space-a",
		Transport:  model.TransportStreamableHTTP,
		Connection: model.Connection{
			URL:     "https://mcp.example.com",
			Headers: map[string]string{"Authorization": "Bearer team-shared-token"},
			// Note: NOT in HeadersUserSupplied. Author chose to share it.
		},
	})

	// Owner sees the real value.
	ownerDetail, apiErr := svc.Get(context.Background(), caller, "shared")
	if apiErr != nil {
		t.Fatalf("owner Get failed: %v", apiErr)
	}
	if got := ownerDetail.QuickStart.Headers["Authorization"]; got != "Bearer team-shared-token" {
		t.Fatalf("owner should see shared value verbatim, got %q", got)
	}

	// Peer sees blank — the defense line.
	peer := Caller{UID: "u-peer", SpaceID: "space-a"}
	peerDetail, apiErr := svc.Get(context.Background(), peer, "shared")
	if apiErr != nil {
		t.Fatalf("peer Get failed: %v", apiErr)
	}
	if got := peerDetail.QuickStart.Headers["Authorization"]; got != "" {
		t.Fatalf(
			"peer must NOT see the shared value on a public record — this is "+
				"the sole defense line after rule 2 was removed, got %q",
			got,
		)
	}
	// Key must still be visible so the consumer's copy-paste snippet
	// shows "Authorization: " and they know they need to supply it.
	if _, ok := peerDetail.QuickStart.Headers["Authorization"]; !ok {
		t.Fatalf("peer should still see the header KEY, got %#v", peerDetail.QuickStart.Headers)
	}
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

func TestGetSystemVisibleAcrossSpaces(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{ID: "sys", Name: "Platform MCP", Visibility: model.VisibilitySystem, OwnerUID: "admin"})

	detail, apiErr := svc.Get(context.Background(), caller, "sys")
	if apiErr != nil {
		t.Fatalf("system record should be visible, got %v", apiErr)
	}
	if detail.ID != "sys" {
		t.Fatalf("wrong record: %+v", detail)
	}
}

func TestPatchCrossSpaceIsNotFoundNotForbidden(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{ID: "x", Visibility: model.VisibilityPublic, OwnerUID: "u2", SpaceID: "space-b"})

	name := "hijacked"
	_, apiErr := svc.Patch(context.Background(), caller, "x", model.PatchRequest{Name: &name})
	if apiErr == nil || apiErr.Code != apierr.CodeNotFound {
		t.Fatalf("expected not_found (no existence leak), got %v", apiErr)
	}
}

func TestPatchVisibleButNotOwnedIsForbidden(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	// Public record in caller's own Space, owned by someone else: visible, but
	// mutation must be forbidden.
	seed(store, model.MCP{ID: "x", Visibility: model.VisibilityPublic, OwnerUID: "u2", SpaceID: "space-a"})

	name := "hijacked"
	_, apiErr := svc.Patch(context.Background(), caller, "x", model.PatchRequest{Name: &name})
	if apiErr == nil || apiErr.Code != apierr.CodeForbidden {
		t.Fatalf("expected forbidden, got %v", apiErr)
	}
}

func TestDeleteCrossSpaceIsNotFound(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{ID: "x", Visibility: model.VisibilityPublic, OwnerUID: "u2", SpaceID: "space-b"})

	apiErr := svc.Delete(context.Background(), caller, "x")
	if apiErr == nil || apiErr.Code != apierr.CodeNotFound {
		t.Fatalf("expected not_found, got %v", apiErr)
	}
	if store.deleted != "" {
		t.Fatalf("deleted a cross-space record: %q", store.deleted)
	}
}

func TestOwnerCanPatchAndDelete(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	fixedClock(svc)
	seed(store, model.MCP{ID: "own", Name: "Mine", Visibility: model.VisibilityPrivate, OwnerUID: "u1", SpaceID: "space-a", Transport: model.TransportStdio})

	newName := "Renamed"
	detail, apiErr := svc.Patch(context.Background(), caller, "own", model.PatchRequest{Name: &newName})
	if apiErr != nil {
		t.Fatalf("owner patch failed: %v", apiErr)
	}
	if detail.Name != "Renamed" || detail.QuickStart.ServerName != "Renamed" {
		t.Fatalf("rename not applied: %+v", detail)
	}

	if apiErr := svc.Delete(context.Background(), caller, "own"); apiErr != nil {
		t.Fatalf("owner delete failed: %v", apiErr)
	}
	if store.deleted != "own" {
		t.Fatalf("delete did not target the record: %q", store.deleted)
	}
}

// TestPatchVisibilityFlipAcceptsPersistedSecrets covers a private→public
// promotion carrying a previously-persisted shared secret. Post-relaxation
// (rules 1 and 2 removed) the flip is accepted: the value stays in the DB
// and is blanked to non-owners by detailForCaller (§5.3). The prior
// guardrail intentionally rejected this flip — the test is preserved with
// inverted expectations to lock in the new posture.
func TestPatchVisibilityFlipAcceptsPersistedSecrets(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{
		ID:         "own",
		Name:       "Private → Public",
		Visibility: model.VisibilityPrivate,
		OwnerUID:   "u1",
		SpaceID:    "space-a",
		Transport:  model.TransportStreamableHTTP,
		Connection: model.Connection{
			URL:     "https://x",
			Headers: map[string]string{"Authorization": "Bearer real-token"},
		},
	})

	newVis := model.VisibilityPublic
	if _, apiErr := svc.Patch(context.Background(), caller, "own", model.PatchRequest{
		Visibility: &newVis,
	}); apiErr != nil {
		t.Fatalf("visibility flip should be accepted, got %v", apiErr)
	}
	got := store.records["own"]
	if got.Visibility != model.VisibilityPublic {
		t.Fatalf("visibility not applied: %q", got.Visibility)
	}
	if got.Connection.Headers["Authorization"] != "Bearer real-token" {
		t.Fatalf("shared secret altered on flip: %q", got.Connection.Headers["Authorization"])
	}
}

// TestPatchVisibilityFlipHonoursUserSupplied confirms the same flip is
// accepted when the sensitive header is user-supplied (stored verbatim
// for the owner, blanked to non-owners by detailForCaller — §5.3).
func TestPatchVisibilityFlipHonoursUserSupplied(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{
		ID:         "own",
		Name:       "Safe flip",
		Visibility: model.VisibilityPrivate,
		OwnerUID:   "u1",
		SpaceID:    "space-a",
		Transport:  model.TransportStreamableHTTP,
		Connection: model.Connection{
			URL:                 "https://x",
			Headers:             map[string]string{"Authorization": ""},
			HeadersUserSupplied: []string{"Authorization"},
		},
	})
	newVis := model.VisibilityPublic
	if _, apiErr := svc.Patch(context.Background(), caller, "own", model.PatchRequest{
		Visibility: &newVis,
	}); apiErr != nil {
		t.Fatalf("safe visibility flip rejected: %v", apiErr)
	}
}

// TestPatchSystemRowAcceptsSharedSecret exercises the system path. Post-
// relaxation (rule 2 removed), a system record accepts a shared bearer
// on PATCH; consumer reads still see it blanked by detailForCaller.
func TestPatchSystemRowAcceptsSharedSecret(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{
		ID:         "sys",
		Name:       "System MCP",
		Visibility: model.VisibilitySystem,
		OwnerUID:   "u1",
		SpaceID:    "", // system rows are cross-Space
		Transport:  model.TransportStreamableHTTP,
		Connection: model.Connection{URL: "https://x"},
	})
	newHeaders := map[string]string{"Authorization": "Bearer real"}
	if _, apiErr := svc.UpdateSystem(context.Background(), "sys", model.PatchRequest{
		Headers: &newHeaders,
	}); apiErr != nil {
		t.Fatalf("shared secret on system row should be accepted, got %v", apiErr)
	}
	if store.records["sys"].Connection.Headers["Authorization"] != "Bearer real" {
		t.Fatalf(
			"headers not applied: %q",
			store.records["sys"].Connection.Headers["Authorization"],
		)
	}
}

// --- List / mine (Acceptance) ---

func TestListMineSetsOwnerScopedFilter(t *testing.T) {
	store := newFakeStore()
	store.listResult = []model.MCP{{ID: "a", OwnerUID: "u1", SpaceID: "space-a"}}
	store.listTotal = 1
	svc := New(store)

	_, apiErr := svc.ListMine(context.Background(), caller, ListParams{})
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if !store.lastFilter.MineOnly {
		t.Fatalf("mine filter not set")
	}
	if store.lastFilter.CallerUID != "u1" || store.lastFilter.SpaceID != "space-a" {
		t.Fatalf("mine filter scope wrong: %+v", store.lastFilter)
	}
}

func TestListDefaultsAndClampsPagination(t *testing.T) {
	store := newFakeStore()
	svc := New(store)

	_, _ = svc.List(context.Background(), caller, ListParams{Limit: 0, Offset: -5})
	if store.lastFilter.Limit != defaultLimit || store.lastFilter.Offset != 0 {
		t.Fatalf("defaults wrong: limit=%d offset=%d", store.lastFilter.Limit, store.lastFilter.Offset)
	}

	_, _ = svc.List(context.Background(), caller, ListParams{Limit: 5000})
	if store.lastFilter.Limit != maxLimit {
		t.Fatalf("limit not clamped: %d", store.lastFilter.Limit)
	}
	if store.lastFilter.MineOnly {
		t.Fatalf("List must not set MineOnly")
	}
}

func TestListFallsBackToAllCategoryWhenStoreReturnsNone(t *testing.T) {
	store := newFakeStore()
	store.listTotal = 3
	store.listCats = nil
	svc := New(store)

	resp, apiErr := svc.List(context.Background(), caller, ListParams{})
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if len(resp.Categories) != 1 || resp.Categories[0].Key != model.CategoryKeyAll || resp.Categories[0].Count != 3 {
		t.Fatalf("category fallback wrong: %#v", resp.Categories)
	}
	// Items must be a non-nil slice for JSON stability.
	if resp.Items == nil {
		t.Fatalf("items should be non-nil")
	}
}

// --- Uniqueness surfaced from the store ---

func TestCreateNameTakenMapsTo409(t *testing.T) {
	store := newFakeStore()
	store.createErr = repository.ErrNameTaken
	svc := New(store)

	_, apiErr := svc.Create(context.Background(), caller, baseCreate())
	if apiErr == nil || apiErr.Code != apierr.CodeNameTaken {
		t.Fatalf("expected name_taken, got %v", apiErr)
	}
}

func TestCreateUnknownStoreErrorMapsTo500(t *testing.T) {
	store := newFakeStore()
	store.createErr = errors.New("connection refused")
	svc := New(store)

	_, apiErr := svc.Create(context.Background(), caller, baseCreate())
	if apiErr == nil || apiErr.Code != apierr.CodeInternal {
		t.Fatalf("expected internal, got %v", apiErr)
	}
	// The internal cause must not leak into the client message.
	if apiErr.Message == "connection refused" {
		t.Fatalf("internal cause leaked to client message")
	}
}

// --- Admin surface (CreateSystem / ListSystem) ---

// adminCaller mirrors what middleware.AdminAuthenticator stamps into the
// context: a synthetic identity with SpaceID="" (system rows are
// cross-Space and carry space_id=NULL in the DB).
var adminCaller = Caller{UID: "platform-admin", Name: "Platform", SpaceID: ""}

func TestCreateSystemForcesVisibilityAndClearsSpace(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	fixedClock(svc)

	req := baseCreate()
	req.Visibility = model.VisibilityPublic // client tries to override; must be ignored

	detail, apiErr := svc.CreateSystem(context.Background(), adminCaller, req)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if detail.Visibility != model.VisibilitySystem {
		t.Fatalf("visibility should be forced to system, got %q", detail.Visibility)
	}
	if store.created.SpaceID != "" {
		t.Fatalf("system row must carry empty SpaceID (NULL in DB), got %q", store.created.SpaceID)
	}
	if store.created.OwnerUID != "platform-admin" || detail.CreatorName != "Platform" {
		t.Fatalf("admin identity not stamped: owner=%q creator=%q", store.created.OwnerUID, detail.CreatorName)
	}
}

func TestCreateSystemAcceptsSharedSecretValue(t *testing.T) {
	// Post-relaxation: rule 2 (public_secret_disallowed) is removed. Admin
	// creation of a system MCP with a secret-shaped shared env value is
	// accepted; the value is stored verbatim and blanked to non-owners via
	// detailForCaller (§5.3).
	store := newFakeStore()
	svc := New(store)
	req := baseCreate()
	req.Env = map[string]string{"GITHUB_TOKEN": "ghp_realTokenViaAdmin"}

	if _, apiErr := svc.CreateSystem(context.Background(), adminCaller, req); apiErr != nil {
		t.Fatalf("system create with shared secret should be accepted, got %v", apiErr)
	}
	if store.created == nil {
		t.Fatalf("record not persisted")
	}
	if got := store.created.Connection.Env["GITHUB_TOKEN"]; got != "ghp_realTokenViaAdmin" {
		t.Fatalf("env not preserved: %q", got)
	}
}

func TestCreateSystemRejectsInvalidTransport(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	req := baseCreate()
	req.Transport = "smtp" // not a valid MCP transport

	_, apiErr := svc.CreateSystem(context.Background(), adminCaller, req)
	if apiErr == nil || apiErr.Code != apierr.CodeInvalidTransport {
		t.Fatalf("expected invalid_transport, got %v", apiErr)
	}
}

func TestCreateSystemRequiresName(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	req := baseCreate()
	req.Name = "   "

	_, apiErr := svc.CreateSystem(context.Background(), adminCaller, req)
	if apiErr == nil || apiErr.Code != apierr.CodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %v", apiErr)
	}
}

func TestCreateSystemMapsNameTakenTo409(t *testing.T) {
	// Belt-and-braces: if store.Create ever surfaces ErrNameTaken (e.g. a
	// future migration adds a partial index that fires on system rows too),
	// the service must still translate it to the wire code. The primary
	// guard for system-row dupes now lives at the service layer — see
	// TestCreateSystemPreChecksNameDupe below.
	store := newFakeStore()
	store.createErr = repository.ErrNameTaken
	svc := New(store)

	_, apiErr := svc.CreateSystem(context.Background(), adminCaller, baseCreate())
	if apiErr == nil || apiErr.Code != apierr.CodeNameTaken {
		t.Fatalf("expected name_taken, got %v", apiErr)
	}
}

func TestCreateSystemPreChecksNameDupe(t *testing.T) {
	// The DB UNIQUE index (owner_uid, space_id, name_live) does NOT fire
	// for system rows because space_id is NULL and MySQL treats NULLs as
	// distinct. The service-level pre-check must catch the dupe on its
	// own; otherwise two rows with the same name slip through.
	store := newFakeStore()
	svc := New(store)
	if _, apiErr := svc.CreateSystem(context.Background(), adminCaller, baseCreate()); apiErr != nil {
		t.Fatalf("first create should succeed: %v", apiErr)
	}
	_, apiErr := svc.CreateSystem(context.Background(), adminCaller, baseCreate())
	if apiErr == nil || apiErr.Code != apierr.CodeNameTaken {
		t.Fatalf("second create with same name should return name_taken, got %v", apiErr)
	}
}

func TestCreateSystemPreChecksSlugDupe(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	req1 := baseCreate()
	req1.Name = "System MCP One"
	req1.Slug = "shared-slug"
	if _, apiErr := svc.CreateSystem(context.Background(), adminCaller, req1); apiErr != nil {
		t.Fatalf("first create should succeed: %v", apiErr)
	}
	req2 := baseCreate()
	req2.Name = "System MCP Two"
	req2.Slug = "shared-slug"
	_, apiErr := svc.CreateSystem(context.Background(), adminCaller, req2)
	if apiErr == nil || apiErr.Code != apierr.CodeSlugTaken {
		t.Fatalf("second create with same slug should return slug_taken, got %v", apiErr)
	}
}

func TestUpdateSystemAllowsNoopRename(t *testing.T) {
	// PATCH that keeps the same name must not self-collide.
	store := newFakeStore()
	svc := New(store)
	created, apiErr := svc.CreateSystem(context.Background(), adminCaller, baseCreate())
	if apiErr != nil {
		t.Fatalf("create failed: %v", apiErr)
	}
	name := created.Name
	req := model.PatchRequest{Name: &name}
	if _, apiErr := svc.UpdateSystem(context.Background(), created.ID, req); apiErr != nil {
		t.Fatalf("no-op rename should succeed, got %v", apiErr)
	}
}

func TestUpdateSystemRejectsRenameOntoSibling(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	req1 := baseCreate()
	req1.Name = "Sibling A"
	req1.Slug = "sibling-a"
	if _, apiErr := svc.CreateSystem(context.Background(), adminCaller, req1); apiErr != nil {
		t.Fatalf("create A failed: %v", apiErr)
	}
	req2 := baseCreate()
	req2.Name = "Sibling B"
	req2.Slug = "sibling-b"
	b, apiErr := svc.CreateSystem(context.Background(), adminCaller, req2)
	if apiErr != nil {
		t.Fatalf("create B failed: %v", apiErr)
	}
	rename := "Sibling A"
	_, apiErr = svc.UpdateSystem(context.Background(), b.ID, model.PatchRequest{Name: &rename})
	if apiErr == nil || apiErr.Code != apierr.CodeNameTaken {
		t.Fatalf("rename onto sibling name should be name_taken, got %v", apiErr)
	}
}

func TestListSystemFilterSetsSystemOnlyAndSkipsSpace(t *testing.T) {
	store := newFakeStore()
	svc := New(store)

	_, apiErr := svc.ListSystem(context.Background(), ListParams{Categories: []string{"dev"}})
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if !store.lastFilter.SystemOnly {
		t.Fatalf("ListSystem must set SystemOnly=true on the filter")
	}
	if store.lastFilter.MineOnly {
		t.Fatalf("ListSystem must NOT set MineOnly")
	}
	// The admin path is cross-Space; no space anchor is threaded through.
	if store.lastFilter.SpaceID != "" || store.lastFilter.CallerUID != "" {
		t.Fatalf("ListSystem should not pass caller/space (filter: %+v)", store.lastFilter)
	}
	if len(store.lastFilter.Categories) != 1 || store.lastFilter.Categories[0] != "dev" {
		t.Fatalf("ListSystem must honor category filter")
	}
}

func TestListSystemFallsBackToAllCategoryWhenStoreReturnsNone(t *testing.T) {
	store := newFakeStore()
	store.listTotal = 2
	store.listCats = nil
	svc := New(store)

	resp, apiErr := svc.ListSystem(context.Background(), ListParams{})
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if len(resp.Categories) != 1 || resp.Categories[0].Count != 2 {
		t.Fatalf("category fallback wrong: %#v", resp.Categories)
	}
}

// --- Slug (mcp-v1.md §3, migration 20260714-03) -----------------------------

func TestCreateAutoSlugifiesFromNameWhenSlugOmitted(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	fixedClock(svc)
	req := baseCreate()
	req.Name = "GitHub MCP" // slugify → "github-mcp"

	_, apiErr := svc.Create(context.Background(), caller, req)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if store.created.Slug != "github-mcp" {
		t.Fatalf("slug not auto-derived: %q", store.created.Slug)
	}
}

func TestCreateRejectsSlugWithBadCharset(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	req := baseCreate()
	req.Slug = "GitHub MCP" // uppercase + space → invalid

	_, apiErr := svc.Create(context.Background(), caller, req)
	if apiErr == nil || apiErr.Code != apierr.CodeSlugInvalid {
		t.Fatalf("expected slug_invalid, got %v", apiErr)
	}
}

func TestCreateRejectsWhenNameYieldsEmptySlug(t *testing.T) {
	// All-CJK name → slugify returns "" → server refuses instead of
	// silently persisting an empty identifier.
	store := newFakeStore()
	svc := New(store)
	req := baseCreate()
	req.Name = "微博数据"
	req.Slug = ""

	_, apiErr := svc.Create(context.Background(), caller, req)
	if apiErr == nil || apiErr.Code != apierr.CodeSlugInvalid {
		t.Fatalf("expected slug_invalid, got %v", apiErr)
	}
}

func TestCreateHonorsExplicitSlug(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	fixedClock(svc)
	req := baseCreate()
	req.Name = "微博数据分析"
	req.Slug = "weibo-analytics"

	_, apiErr := svc.Create(context.Background(), caller, req)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if store.created.Slug != "weibo-analytics" {
		t.Fatalf("slug not preserved: %q", store.created.Slug)
	}
}

func TestCreateSystemAutoSlugifies(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	fixedClock(svc)
	req := baseCreate()
	req.Name = "Fetch MCP"

	_, apiErr := svc.CreateSystem(context.Background(), adminCaller, req)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if store.created.Slug != "fetch-mcp" {
		t.Fatalf("system-slug not auto-derived: %q", store.created.Slug)
	}
}

func TestCreateSlugTakenMapsTo409(t *testing.T) {
	store := newFakeStore()
	store.createErr = repository.ErrSlugTaken
	svc := New(store)

	_, apiErr := svc.Create(context.Background(), caller, baseCreate())
	if apiErr == nil || apiErr.Code != apierr.CodeSlugTaken {
		t.Fatalf("expected slug_taken, got %v", apiErr)
	}
}

func TestPatchRejectsEmptyStringSlug(t *testing.T) {
	// A non-nil empty-string slug is a client bug (they meant to omit).
	// Rejecting prevents an accidental identifier wipe.
	store := newFakeStore()
	seed(store, model.MCP{
		ID: "abc", Name: "n", Slug: "n", OwnerUID: "u1", SpaceID: "space-a",
		Visibility: model.VisibilityPublic, Transport: model.TransportStreamableHTTP,
	})
	svc := New(store)
	empty := ""
	patch := model.PatchRequest{Slug: &empty}

	_, apiErr := svc.Patch(context.Background(), caller, "abc", patch)
	if apiErr == nil || apiErr.Code != apierr.CodeSlugInvalid {
		t.Fatalf("expected slug_invalid, got %v", apiErr)
	}
}

func TestPatchAcceptsValidSlug(t *testing.T) {
	store := newFakeStore()
	seed(store, model.MCP{
		ID: "abc", Name: "n", Slug: "n", OwnerUID: "u1", SpaceID: "space-a",
		Visibility: model.VisibilityPublic, Transport: model.TransportStreamableHTTP,
	})
	svc := New(store)
	slug := "new-slug"
	patch := model.PatchRequest{Slug: &slug}

	_, apiErr := svc.Patch(context.Background(), caller, "abc", patch)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if store.updated.Slug != "new-slug" {
		t.Fatalf("slug not applied: %q", store.updated.Slug)
	}
}

// TestCreateAutoSlugRoundtripsInDetail verifies the auto-derived slug is what
// gets echoed back in the wire response, not the original user input. This is
// the round-trip contract from mcp-v1.md §3.1 field notes: "auto-derived by
// the server from name when the client omits it".
func TestCreateAutoSlugRoundtripsInDetail(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	fixedClock(svc)
	req := baseCreate()
	req.Name = "GitHub MCP"
	req.Slug = "" // omit; server should slugify from name

	detail, apiErr := svc.Create(context.Background(), caller, req)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if detail.QuickStart.Slug != "github-mcp" {
		t.Fatalf("wire response slug = %q, want %q", detail.QuickStart.Slug, "github-mcp")
	}
}
