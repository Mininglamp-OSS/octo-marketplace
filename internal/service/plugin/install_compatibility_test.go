package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
)

// Compare complete outgoing definitions, files, environment and runtime bindings,
// rather than merely proving that both adapters call some installer.
func TestInstallAdaptersSendIdenticalContent(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, team := range []bool{false, true} {
			t.Run(map[bool]string{false: "legacy-content", true: "released-pins"}[pinned]+map[bool]string{false: "/expert", true: "/team"}[team], func(t *testing.T) {
				var svc *Service
				var remote *fakeCapabilityInstaller
				var root string
				if pinned {
					var f *pinnedFixtureStore
					svc, f, remote = fixturePinned()
					root = "team"
					if !team {
						root = "expert-a"
						f.state.PublishedVersionID = pin("expert-v1")
					}
				} else {
					svc, _, remote = capabilityFixture()
					root = map[bool]string{false: "expert-1", true: "team-1"}[team]
				}
				p := installationParams()
				p.ResourceName = ""
				modern, err := svc.CreateInstallation(context.Background(), testCaller, root, p)
				if err != nil {
					t.Fatal(err)
				}
				first, _ := json.Marshal(remote.in)
				legacy, err := svc.Install(context.Background(), testCaller, root, InstallParams{WorkspaceID: p.WorkspaceID, RuntimeID: p.RuntimeID, CustomEnv: p.CustomEnv, Token: p.Token, IdempotencyKey: p.IdempotencyKey})
				if err != nil {
					t.Fatal(err)
				}
				second, _ := json.Marshal(remote.in)
				if string(first) != string(second) || legacy.AgentID != modern.AgentID || legacy.SquadID != modern.SquadID || remote.calls != 2 {
					t.Fatalf("adapter content/result diverged: calls=%d", remote.calls)
				}
			})
		}
	}
}

func TestLegacyInstallAcceptsOldOpaqueIDsWithoutNewHeaders(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		var svc *Service
		var remote *fakeCapabilityInstaller
		root := "team-1"
		if pinned {
			svc, _, remote = fixturePinned()
			root = "team"
		} else {
			svc, _, remote = capabilityFixture()
		}
		n := 0
		svc.id = func() string { n++; return "generated-operation-" + string(rune('a'+n)) }
		p := InstallParams{WorkspaceID: " ws-1 ", RuntimeID: " rt-1 ", CustomEnv: map[string]string{"VALUE": " \n中文 "}, Token: "caller-token"}
		if _, err := svc.Install(context.Background(), testCaller, root, p); err != nil {
			t.Fatal(err)
		}
		firstKey := remote.key
		if remote.workspace != "ws-1" || remote.token != p.Token || remote.in.Bindings.Experts[0].RuntimeID != "rt-1" || remote.in.Definition.Experts[0].CustomEnv["VALUE"] != " \n中文 " {
			t.Fatal("legacy input changed")
		}
		if _, err := svc.Install(context.Background(), testCaller, root, p); err != nil {
			t.Fatal(err)
		}
		if remote.key == "" || remote.key == firstKey {
			t.Fatal("keyless calls unexpectedly share an operation")
		}
		if _, err := svc.CreateInstallation(context.Background(), testCaller, root, InstallationParams{WorkspaceID: "ws-1", RuntimeID: "rt-1", IdempotencyKey: "key"}); err == nil {
			t.Fatal("new endpoint UUID validation removed")
		}
	}
}

func TestBothInstallAdaptersFailClosedWithoutCapabilityInstaller(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		svc, _, _ := capabilityFixture()
		svc.capabilityInstaller = nil
		svc.WithPinnedInstallation(pinned)
		p := installationParams()
		_, err := svc.Install(context.Background(), testCaller, "expert-1", InstallParams{WorkspaceID: p.WorkspaceID, RuntimeID: p.RuntimeID})
		if !errors.Is(err, expertsvc.ErrFleetNotConfigured) {
			t.Fatal(err)
		}
		if _, err = svc.CreateInstallation(context.Background(), testCaller, "expert-1", p); !errors.Is(err, expertsvc.ErrFleetNotConfigured) {
			t.Fatal(err)
		}
	}
}

func TestSuccessfulReplayStillChecksFleetWorkspacePermission(t *testing.T) {
	svc, _, remote := fixturePinned()
	p := installationParams()
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "team", p); err != nil {
		t.Fatal(err)
	}
	remote.err = &fleet.CapabilityAPIError{Status: 403}
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "team", p); err == nil {
		t.Fatal("revoked workspace permission bypassed through a local receipt")
	}
	if remote.calls != 2 {
		t.Fatal("replay skipped Fleet authorization")
	}
}

func TestFrozenRetryRejectsRevokedPrivateDependency(t *testing.T) {
	svc, f, remote := fixturePinned()
	p := installationParams()
	remote.err = errors.New("timeout")
	_, _ = svc.CreateInstallation(context.Background(), testCaller, "team", p)
	f.plugins["skill-a"].OwnerUID = "different-owner"
	remote.err = nil
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "team", p); err == nil {
		t.Fatal("frozen plan bypassed current private ownership")
	}
	if remote.calls != 1 {
		t.Fatal("ineligible retry reached Fleet")
	}
}

func TestLegacyInstallPreservesExistingIdentityAdmission(t *testing.T) {
	svc, _, remote := capabilityFixture()
	caller := testCaller
	caller.BotUID = "existing-bot-context"
	p := installationParams()
	if _, err := svc.CreateInstallation(context.Background(), caller, "expert-1", p); !errors.Is(err, ErrDependencyHidden) {
		t.Fatal("new endpoint user-only policy changed", err)
	}
	if _, err := svc.Install(context.Background(), caller, "expert-1", InstallParams{WorkspaceID: p.WorkspaceID, RuntimeID: p.RuntimeID, Token: p.Token}); err != nil {
		t.Fatal("legacy caller rejected before Fleet authorization", err)
	}
	if remote.calls != 1 || remote.token != p.Token {
		t.Fatal("legacy authorization credential not forwarded")
	}
}

func TestPinnedOversizedSerializedPlanIsNotPersisted(t *testing.T) {
	svc, f, remote := fixturePinned()
	for key, v := range f.versions {
		if key.PluginID == "team" || key.PluginID == "expert-a" || key.PluginID == "expert-b" {
			v.Package = packageWith(rawAtt("AGENTS.md", strings.Repeat("\x01", 1<<20)))
		}
	}
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "team", installationParams()); !errors.Is(err, ErrTooLarge) {
		t.Fatal("oversized escaped wire plan accepted", err)
	}
	if remote.calls != 0 || len(f.attempts) != 0 {
		t.Fatal("invalid plan persisted or dispatched")
	}
}

func TestBothAdaptersRejectPrivateReferencesFromBroaderLegacySources(t *testing.T) {
	for _, targetKind := range []string{"skill", "expert"} {
		for _, visibility := range []model.PluginVisibility{"private", "space", "system"} {
			for _, legacy := range []bool{true, false} {
				svc, store, remote := capabilityFixture()
				root, target := "expert-1", "skill-1"
				if targetKind == "expert" {
					root, target = "team-1", "expert-1"
				}
				store.plugins[root].Visibility = visibility
				store.plugins[target].Visibility = model.PluginVisibilityPrivate
				p := installationParams()
				var err error
				if legacy {
					_, err = svc.Install(context.Background(), testCaller, root, InstallParams{WorkspaceID: p.WorkspaceID, RuntimeID: p.RuntimeID, CustomEnv: p.CustomEnv})
				} else {
					_, err = svc.CreateInstallation(context.Background(), testCaller, root, p)
				}
				if visibility == model.PluginVisibilityPrivate {
					if err != nil || remote.calls != 1 {
						t.Fatalf("legal private %s blocked: %v", targetKind, err)
					}
				} else if !errors.Is(err, ErrDependencyHidden) || remote.calls != 0 {
					t.Fatalf("%s source exposed private %s: %v", visibility, targetKind, err)
				}
			}
		}
	}
}

func TestCachedPrivateSkillDoesNotBypassSecondExpertAudience(t *testing.T) {
	svc, store, remote := capabilityFixture()
	store.plugins["team-1"].Visibility = model.PluginVisibilityPrivate
	store.plugins["expert-1"].Visibility = model.PluginVisibilityPrivate
	store.plugins["skill-1"].Visibility = model.PluginVisibilityPrivate
	second := *store.plugins["expert-1"]
	second.ID, second.Name, second.Visibility = "expert-2", "Second", model.PluginVisibilitySpace
	store.plugins[second.ID] = &second
	store.relations[second.ID] = store.relations["expert-1"]
	store.relations["team-1"] = append(store.relations["team-1"], model.PluginRelation{Type: "expert_team_expert", TargetPluginID: second.ID, Status: 1, SortOrder: 1, Data: json.RawMessage(`{"role":"reviewer"}`)})
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "team-1", installationParams()); !errors.Is(err, ErrDependencyHidden) {
		t.Fatal("cached private skill reached a space expert", err)
	}
	if remote.calls != 0 {
		t.Fatal("invalid private graph dispatched")
	}
}

func TestBothAdaptersEnforceSpaceDependencyAudience(t *testing.T) {
	for _, kind := range []string{"skill", "expert"} {
		for _, visibility := range []model.PluginVisibility{model.PluginVisibilityPrivate, model.PluginVisibilitySpace, model.PluginVisibilitySystem, model.PluginVisibilityPublic} {
			for _, legacy := range []bool{true, false} {
				svc, store, remote := capabilityFixture()
				root, target := "expert-1", "skill-1"
				if kind == "expert" {
					root, target = "team-1", "expert-1"
				}
				store.plugins[root].Visibility = visibility
				store.plugins[target].Visibility = model.PluginVisibilitySpace
				p := installationParams()
				var err error
				if legacy {
					_, err = svc.Install(context.Background(), testCaller, root, InstallParams{WorkspaceID: p.WorkspaceID, RuntimeID: p.RuntimeID, CustomEnv: p.CustomEnv})
				} else {
					_, err = svc.CreateInstallation(context.Background(), testCaller, root, p)
				}
				if visibility == model.PluginVisibilityPrivate || visibility == model.PluginVisibilitySpace {
					if err != nil || remote.calls != 1 {
						t.Fatalf("same-Space dependency rejected: kind=%s source=%s err=%v", kind, visibility, err)
					}
				} else if !errors.Is(err, ErrDependencyHidden) || remote.calls != 0 {
					t.Fatalf("broader source dispatched Space dependency: kind=%s source=%s legacy=%t err=%v calls=%d", kind, visibility, legacy, err, remote.calls)
				}
			}
		}
	}
}

func TestCachedSpaceSkillDoesNotBypassSecondExpertAudience(t *testing.T) {
	svc, store, remote := capabilityFixture()
	store.plugins["team-1"].Visibility = model.PluginVisibilityPrivate
	store.plugins["expert-1"].Visibility = model.PluginVisibilitySpace
	store.plugins["skill-1"].Visibility = model.PluginVisibilitySpace
	second := *store.plugins["expert-1"]
	second.ID, second.Name, second.Visibility = "expert-2", "Second", model.PluginVisibilitySystem
	store.plugins[second.ID] = &second
	store.relations[second.ID] = store.relations["expert-1"]
	store.relations["team-1"] = append(store.relations["team-1"], model.PluginRelation{Type: "expert_team_expert", TargetPluginID: second.ID, Status: 1, SortOrder: 1, Data: json.RawMessage(`{"role":"reviewer"}`)})
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "team-1", installationParams()); !errors.Is(err, ErrDependencyHidden) {
		t.Fatal("cached Space skill reached a system expert", err)
	}
	if remote.calls != 0 {
		t.Fatal("invalid Space graph dispatched")
	}
}
