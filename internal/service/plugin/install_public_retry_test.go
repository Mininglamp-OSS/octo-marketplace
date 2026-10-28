package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Legacy public is a global audience, including when a frozen plan's NodeIDs
// contains its own root. Keep the same eligibility checks on first dispatch and
// replay; normalizing the alias must not bypass later delisting or edge checks.
func TestLegacyPublicFrozenPlansReplayBothAdapters(t *testing.T) {
	for _, adapter := range []string{"legacy", "new"} {
		for _, unknown := range []bool{false, true} {
			for _, pair := range [][2]model.PluginVisibility{
				{model.PluginVisibilityPublic, model.PluginVisibilityPublic},
				{model.PluginVisibilityPublic, model.PluginVisibilitySystem},
				{model.PluginVisibilitySystem, model.PluginVisibilityPublic},
				{model.PluginVisibilityPrivate, model.PluginVisibilityPublic},
			} {
				t.Run(fmt.Sprintf("%s/unknown=%t/root=%s/dependency=%s", adapter, unknown, pair[0], pair[1]), func(t *testing.T) {
					svc, store, remote := fixturePinned()
					for _, p := range store.plugins {
						p.Visibility, p.SpaceID = pair[1], nil
					}
					root := store.plugins["team"]
					root.Visibility = pair[0]
					if pair[0] == model.PluginVisibilityPrivate {
						root.SpaceID = stringPtr(testCaller.SpaceID)
					}
					params := installationParams()
					params.ResourceName = ""
					if unknown {
						remote.err = errors.New("response lost")
					}
					first := invokeIntegrityInstall(svc, adapter, params)
					if (unknown && first == nil) || (!unknown && first != nil) || remote.calls != 1 {
						t.Fatalf("first install: err=%v calls=%d", first, remote.calls)
					}
					if unknown {
						var outcome *InstallationAttemptError
						if !errors.As(first, &outcome) || !outcome.Unknown {
							t.Fatalf("lost response did not retain uncertainty: %v", first)
						}
					}
					attemptKey := params.WorkspaceID + digestInstall(params.IdempotencyKey)
					attempt := store.attempts[attemptKey]
					if attempt == nil {
						t.Fatal("missing frozen attempt")
					}
					var plan pinnedInstallationPlan
					if err := json.Unmarshal(attempt.FrozenPlan, &plan); err != nil {
						t.Fatal(err)
					}
					if !slices.Contains(plan.NodeIDs, root.ID) {
						t.Fatal("frozen governance graph omitted root")
					}
					body, err := json.Marshal(remote.in)
					if err != nil {
						t.Fatal(err)
					}
					key := remote.key
					remote.err = nil
					assertBlocked := func() {
						t.Helper()
						before, _ := json.Marshal(store.attempts[attemptKey])
						err := invokeIntegrityInstall(svc, adapter, params)
						after, _ := json.Marshal(store.attempts[attemptKey])
						if !errors.Is(err, ErrDependencyHidden) || remote.calls != 1 || string(before) != string(after) {
							t.Fatalf("governance refusal: err=%v calls=%d unchanged=%t", err, remote.calls, string(before) == string(after))
						}
					}
					root.ListingState = model.PluginListingStateDelisted
					assertBlocked()
					root.ListingState = model.PluginListingStatePublished
					// A global Expert may not distribute a newly private Skill even
					// if the caller (and a private root) can still read that Skill.
					dependency := store.plugins["skill-a"]
					dependency.Visibility, dependency.SpaceID = model.PluginVisibilityPrivate, stringPtr(testCaller.SpaceID)
					assertBlocked()
					dependency.Visibility, dependency.SpaceID = pair[1], nil
					if err := invokeIntegrityInstall(svc, adapter, params); err != nil || remote.calls != 2 {
						t.Fatalf("same-key recovery: err=%v calls=%d", err, remote.calls)
					}
					after, err := json.Marshal(remote.in)
					if err != nil || key != remote.key || string(body) != string(after) {
						t.Fatalf("replay changed original key/request: %v", err)
					}
				})
			}
		}
	}
}
