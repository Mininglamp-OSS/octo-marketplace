package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// These boundaries come from Fleet test 49a8266 pkg/capability/validation.go.
func TestCapabilityFleetTextLimitsBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name, root string
		limit      int
		change     func(*fakeStore, *InstallationParams, string)
	}{
		{"custom_name", "expert-1", 128, func(_ *fakeStore, p *InstallationParams, v string) { p.ResourceName = v }},
		{"catalog_name", "expert-1", 128, func(s *fakeStore, p *InstallationParams, v string) {
			p.ResourceName = ""
			s.plugins["expert-1"].Name = v
		}},
		{"expert_description", "expert-1", 255, func(s *fakeStore, _ *InstallationParams, v string) {
			s.plugins["expert-1"].Manifest, _ = json.Marshal(map[string]string{"description": v})
		}},
		{"team_description", "team-1", 255, func(s *fakeStore, _ *InstallationParams, v string) {
			s.plugins["team-1"].Manifest, _ = json.Marshal(map[string]string{"description": v})
		}},
		{"skill_description", "expert-1", 512, func(s *fakeStore, _ *InstallationParams, v string) {
			s.plugins["skill-1"].Manifest, _ = json.Marshal(map[string]string{"description": v})
		}},
		{"member_role", "team-1", 500, func(s *fakeStore, _ *InstallationParams, v string) {
			second := *s.plugins["expert-1"]
			second.ID, second.Name = "expert-2", "Bob"
			s.plugins[second.ID] = &second
			wiring, _ := json.Marshal(map[string]string{"role": v})
			s.relations["team-1"] = append(s.relations["team-1"], model.PluginRelation{Type: "expert_team_expert", TargetPluginID: second.ID, Status: 1, SortOrder: 1, Data: wiring})
		}},
	} {
		for _, excess := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/at_limit", true: "/over_limit"}[excess], func(t *testing.T) {
				svc, store, installer := capabilityFixture()
				p := installationParams()
				n := tc.limit
				if excess {
					n++
				}
				tc.change(store, &p, strings.Repeat("名", n))
				_, err := svc.CreateInstallation(context.Background(), testCaller, tc.root, p)
				if excess {
					var invalid *InstallationValidationError
					if !errors.As(err, &invalid) || installer.calls != 0 {
						t.Fatalf("err=%v calls=%d", err, installer.calls)
					}
				} else if err != nil || installer.calls != 1 {
					t.Fatalf("err=%v calls=%d", err, installer.calls)
				}
			})
		}
	}
}

func TestCapabilityFleetDefinitionEnvironmentAndEmptyMCP(t *testing.T) {
	for _, root := range []string{"expert-1", "team-1"} {
		t.Run(root, func(t *testing.T) {
			svc, _, installer := capabilityFixture()
			if _, err := svc.CreateInstallation(context.Background(), testCaller, root, installationParams()); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(installer.in)
			var wire struct {
				Definition struct {
					Experts []map[string]json.RawMessage `json:"experts"`
				} `json:"definition"`
				Bindings struct {
					Experts []map[string]json.RawMessage `json:"experts"`
				} `json:"bindings"`
			}
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			for _, expert := range wire.Definition.Experts {
				if _, exists := expert["mcp_config"]; exists {
					t.Fatal("empty MCP must be omitted, not sent to Fleet")
				}
				var env map[string]string
				if json.Unmarshal(expert["custom_env"], &env) != nil || env["OCTOBUDDY_PROVIDER_ID"] != "provider-current" {
					t.Fatal("expert environment missing")
				}
			}
			for _, binding := range wire.Bindings.Experts {
				if len(binding) != 2 || binding["expert_name"] == nil || binding["runtime_id"] == nil {
					t.Fatal("binding contains fields Fleet rejects")
				}
			}
		})
	}
}

func TestCapabilityUnknownReplayDoesNotCountRetries(t *testing.T) {
	svc, _, installer := capabilityFixture()
	tracker := &fakeTracker{}
	svc.WithMetrics(tracker)
	for range 2 {
		out, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams())
		if err != nil || out.Replayed || out.AgentID == "" {
			t.Fatalf("out=%+v err=%v", out, err)
		}
	}
	if installer.calls != 2 || tracker.id != "" {
		t.Fatal("missing replay metadata caused an unconfirmed metric increment")
	}
}

func TestCapabilitySupportingFileCannotAliasSkillMD(t *testing.T) {
	for _, name := range []string{"skill.md", "Skill.md"} {
		svc, store, installer := capabilityFixture()
		store.plugins["skill-1"].Package = packageWith(rawAtt("SKILL.md", "main"), rawAtt(name, "extra"))
		if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err == nil || installer.calls != 0 {
			t.Fatalf("alias %s reached Fleet", name)
		}
	}
}
