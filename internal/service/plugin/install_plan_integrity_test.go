package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func invokeIntegrityInstall(s *Service, adapter string, p InstallationParams) error {
	if adapter == "legacy" {
		_, err := s.Install(context.Background(), testCaller, "team", InstallParams{WorkspaceID: p.WorkspaceID, RuntimeID: p.RuntimeID, CustomEnv: p.CustomEnv, Token: p.Token, IdempotencyKey: p.IdempotencyKey})
		return err
	}
	_, err := s.CreateInstallation(context.Background(), testCaller, "team", p)
	return err
}

func TestFrozenEligibilityMetadataTamperingBlocksBothAdapters(t *testing.T) {
	for _, adapter := range []string{"new", "legacy"} {
		for _, completed := range []bool{false, true} {
			for _, mode := range []string{"omit_connector", "omit_edges", "alter_edge", "append_node", "unsigned", "bad_mac"} {
				stage := "unknown"
				if completed {
					stage = "completed"
				}
				t.Run(adapter+"/"+stage+"/"+mode, func(t *testing.T) {
					s, f, remote := fixturePinned()
					p := installationParams()
					p.ResourceName = ""
					if !completed {
						remote.err = errors.New("response lost")
					}
					err := invokeIntegrityInstall(s, adapter, p)
					if (completed && err != nil) || (!completed && err == nil) || remote.calls != 1 {
						t.Fatalf("initial err=%v calls=%d", err, remote.calls)
					}
					a := f.attempts[p.WorkspaceID+digestInstall(p.IdempotencyKey)]
					if a == nil {
						t.Fatal("missing attempt")
					}
					original := append([]byte(nil), a.FrozenPlan...)
					var raw map[string]json.RawMessage
					if err = json.Unmarshal(a.FrozenPlan, &raw); err != nil {
						t.Fatal(err)
					}
					switch mode {
					case "omit_connector":
						var ids []string
						_ = json.Unmarshal(raw["NodeIDs"], &ids)
						filtered := []string{}
						for _, id := range ids {
							if id != "connector" {
								filtered = append(filtered, id)
							}
						}
						raw["NodeIDs"], _ = json.Marshal(filtered)
						var edges []planEligibilityEdge
						_ = json.Unmarshal(raw["Edges"], &edges)
						kept := []planEligibilityEdge{}
						for _, e := range edges {
							if e.Target != "connector" {
								kept = append(kept, e)
							}
						}
						raw["Edges"], _ = json.Marshal(kept)
						f.plugins["connector"].ListingState = model.PluginListingStateDelisted
					case "omit_edges":
						var edges []planEligibilityEdge
						_ = json.Unmarshal(raw["Edges"], &edges)
						kept := []planEligibilityEdge{}
						for _, e := range edges {
							if e.Source != "expert-a" {
								kept = append(kept, e)
							}
						}
						raw["Edges"], _ = json.Marshal(kept)
						f.plugins["expert-a"].Visibility = model.PluginVisibilitySpace
					case "alter_edge":
						raw["Edges"] = json.RawMessage(`[{"Source":"team","Target":"team"}]`)
					case "append_node":
						var ids []string
						_ = json.Unmarshal(raw["NodeIDs"], &ids)
						ids = append(ids, "team")
						raw["NodeIDs"], _ = json.Marshal(ids)
					case "unsigned":
						delete(raw, "EligibilityHMAC")
					case "bad_mac":
						raw["EligibilityHMAC"] = json.RawMessage(`"invalid"`)
					}
					a.FrozenPlan, err = json.Marshal(raw)
					if err != nil {
						t.Fatal(err)
					}
					before, _ := json.Marshal(a)
					calls := f.pluginCalls
					remote.err = nil
					err = invokeIntegrityInstall(s, adapter, p)
					after, _ := json.Marshal(a)
					if !errors.Is(err, ErrIntegrity) || remote.calls != 1 || string(before) != string(after) || f.pluginCalls != calls {
						t.Fatalf("err=%v calls=%d unchanged=%t governanceReads=%d", err, remote.calls, string(before) == string(after), f.pluginCalls-calls)
					}
					// Restore the authentic bytes, not a newly resolved plan. Current governance
					// remains mandatory even with a valid MAC, including successful receipts.
					a.FrozenPlan = original
					if mode == "omit_connector" || mode == "omit_edges" {
						err = invokeIntegrityInstall(s, adapter, p)
						if !errors.Is(err, ErrDependencyHidden) || remote.calls != 1 {
							t.Fatalf("current governance bypassed: %v calls=%d", err, remote.calls)
						}
						f.plugins["connector"].ListingState = model.PluginListingStatePublished
						f.plugins["expert-a"].Visibility = model.PluginVisibilityPrivate
					}
					if err = invokeIntegrityInstall(s, adapter, p); err != nil || remote.calls != 2 {
						t.Fatalf("authentic retry=%v calls=%d", err, remote.calls)
					}
				})
			}
		}
	}
}

// The latest review example cannot establish a legal initial installation.
// A private target requires a private source, same owner and same Space.
func TestPrivateReviewExampleAndLegalRetryBothAdapters(t *testing.T) {
	for _, adapter := range []string{"new", "legacy"} {
		t.Run(adapter+"/space_example_rejected", func(t *testing.T) {
			s, f, remote := fixturePinned()
			p := installationParams()
			p.ResourceName = ""
			f.plugins["team"].Visibility = model.PluginVisibilitySpace
			for _, id := range []string{"expert-a", "expert-b"} {
				f.plugins[id].Visibility = model.PluginVisibilitySpace
			}
			err := invokeIntegrityInstall(s, adapter, p)
			if !errors.Is(err, ErrDependencyHidden) || remote.calls != 0 || len(f.attempts) != 0 {
				t.Fatalf("err=%v calls=%d attempts=%d", err, remote.calls, len(f.attempts))
			}
		})
		t.Run(adapter+"/legal_private_retry", func(t *testing.T) {
			s, _, remote := fixturePinned()
			p := installationParams()
			p.ResourceName = ""
			remote.err = errors.New("lost response")
			if err := invokeIntegrityInstall(s, adapter, p); err == nil || remote.calls != 1 {
				t.Fatalf("initial=%v calls=%d", err, remote.calls)
			}
			before, _ := json.Marshal(remote.in)
			key := remote.key
			remote.err = nil
			if err := invokeIntegrityInstall(s, adapter, p); err != nil || remote.calls != 2 {
				t.Fatalf("retry=%v calls=%d", err, remote.calls)
			}
			after, _ := json.Marshal(remote.in)
			if string(before) != string(after) || key != remote.key {
				t.Fatal("legal retry changed key or request")
			}
		})
	}
}

func TestFrozenEligibilityCannotBeTransplantedBetweenOperations(t *testing.T) {
	for _, otherWorkspace := range []bool{false, true} {
		name := "different_key"
		if otherWorkspace {
			name = "different_workspace"
		}
		t.Run(name, func(t *testing.T) {
			s, f, remote := fixturePinned()
			p := installationParams()
			remote.err = errors.New("timeout")
			_, _ = s.CreateInstallation(context.Background(), testCaller, "team", p)
			first := f.attempts[p.WorkspaceID+digestInstall(p.IdempotencyKey)]
			next := p
			if otherWorkspace {
				next.WorkspaceID = "other-workspace"
			} else {
				next.IdempotencyKey = "other-key"
			}
			_, _ = s.CreateInstallation(context.Background(), testCaller, "team", next)
			second := f.attempts[next.WorkspaceID+digestInstall(next.IdempotencyKey)]
			if first == nil || second == nil || remote.calls != 2 {
				t.Fatal("missing attempts")
			}
			if first.RequestFingerprint != second.RequestFingerprint {
				t.Fatal("control requests must be identical")
			}
			second.FrozenPlan = append([]byte(nil), first.FrozenPlan...)
			before, _ := json.Marshal(second)
			reads := f.pluginCalls
			remote.err = nil
			_, err := s.CreateInstallation(context.Background(), testCaller, "team", next)
			after, _ := json.Marshal(second)
			if !errors.Is(err, ErrIntegrity) || remote.calls != 2 || reads != f.pluginCalls || string(before) != string(after) {
				t.Fatalf("transplant err=%v calls=%d", err, remote.calls)
			}
		})
	}
}
