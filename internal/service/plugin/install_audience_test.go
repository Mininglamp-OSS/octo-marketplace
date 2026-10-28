package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Retained legacy records may predate write-time audience checks. Caller
// visibility alone cannot authorize distributing a Space target via a public
// source. Exercise the real adapters rather than only the audience predicate.
func TestBothAdaptersEnforceIndependentSpaceAudience(t *testing.T) {
	for _, adapter := range []string{"legacy", "new"} {
		for _, targetKind := range []string{"skill", "expert"} {
			for _, visibility := range []model.PluginVisibility{"private", "space", "system", "public"} {
				t.Run(fmt.Sprintf("%s/%s/%s", adapter, targetKind, visibility), func(t *testing.T) {
					s, store, remote := capabilityFixture()
					root, target := "expert-1", "skill-1"
					if targetKind == "expert" {
						root, target = "team-1", "expert-1"
						store.plugins["skill-1"].Visibility = model.PluginVisibilitySystem
					}
					store.plugins[root].Visibility = visibility
					store.plugins[target].Visibility = model.PluginVisibilitySpace
					err := invokeAudienceInstall(s, adapter, root)
					if visibility == model.PluginVisibilitySystem || visibility == "public" {
						if !errors.Is(err, ErrDependencyHidden) || remote.calls != 0 {
							t.Fatalf("broader source dispatched Space %s: err=%v calls=%d", targetKind, err, remote.calls)
						}
					} else if err != nil || remote.calls != 1 {
						t.Fatalf("same-Space reference blocked: err=%v calls=%d", err, remote.calls)
					}
				})
			}
		}
	}
}

func invokeAudienceInstall(s *Service, adapter, root string) error {
	p := installationParams()
	if adapter == "legacy" {
		_, err := s.Install(context.Background(), testCaller, root, InstallParams{WorkspaceID: p.WorkspaceID, RuntimeID: p.RuntimeID, IdempotencyKey: p.IdempotencyKey, Token: p.Token})
		return err
	}
	_, err := s.CreateInstallation(context.Background(), testCaller, root, p)
	return err
}

func TestCachedSpaceSkillRechecksEachExpertAudience(t *testing.T) {
	for _, adapter := range []string{"legacy", "new"} {
		t.Run(adapter, func(t *testing.T) {
			s, store, remote := capabilityFixture()
			store.plugins["team-1"].Visibility = model.PluginVisibilityPrivate
			store.plugins["expert-1"].Visibility = model.PluginVisibilityPrivate
			store.plugins["skill-1"].Visibility = model.PluginVisibilitySpace
			second := *store.plugins["expert-1"]
			second.ID, second.Name, second.Visibility = "expert-2", "Second", model.PluginVisibilitySystem
			store.plugins[second.ID] = &second
			store.relations[second.ID] = append([]model.PluginRelation(nil), store.relations["expert-1"]...)
			for i := range store.relations[second.ID] {
				store.relations[second.ID][i].SourcePluginID = second.ID
				store.relations[second.ID][i].ID = fmt.Sprintf("second-skill-%d", i)
			}
			store.relations["team-1"] = append(store.relations["team-1"], model.PluginRelation{Type: "expert_team_expert", TargetPluginID: second.ID, Status: 1, SortOrder: 1, Data: json.RawMessage(`{"role":"reviewer"}`)})
			if err := invokeAudienceInstall(s, adapter, "team-1"); !errors.Is(err, ErrDependencyHidden) || remote.calls != 0 {
				t.Fatalf("cache bypassed second source: err=%v calls=%d", err, remote.calls)
			}
		})
	}
}

// Root audience checks are intentional. Every supported legal two-edge chain
// must satisfy them, including same-owner private chains; a Space source may
// never hide a private target behind a member Expert.
func TestInstallAudienceIsTransitiveForSupportedVisibilities(t *testing.T) {
	var nodes []model.Plugin
	for _, visibility := range []model.PluginVisibility{"system", "public", "space", "private"} {
		for _, owner := range []string{"a", "b"} {
			for _, space := range []string{"one", "two"} {
				nodes = append(nodes, model.Plugin{Visibility: visibility, OwnerUID: owner, SpaceID: pin(space)})
			}
		}
	}
	for _, root := range nodes {
		for _, member := range nodes {
			for _, target := range nodes {
				if installAudience(&root, &member) && installAudience(&member, &target) && !installAudience(&root, &target) {
					t.Fatalf("legal chain violates root audience: root=%+v member=%+v target=%+v", root, member, target)
				}
			}
		}
	}
}
