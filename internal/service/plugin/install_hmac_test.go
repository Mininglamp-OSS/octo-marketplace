package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	"testing"
)

func TestInstallationHMACRotationAndDomainSeparation(t *testing.T) {
	old := strings.Repeat("a", 32)
	next := strings.Repeat("b", 32)
	svc := New(&fakeStore{}).WithInstallationHMACKeys("old", map[string]string{"old": old})
	input := map[string]string{"custom_env": "synthetic secret"}
	before, err := svc.installationHMAC("old", "input", input)
	if err != nil {
		t.Fatal(err)
	}
	svc.WithInstallationHMACKeys("new", map[string]string{"old": old, "new": next})
	replay, err := svc.installationHMAC("old", "input", input)
	if err != nil || !equalInstallationHMAC(before, replay) {
		t.Fatal("rotation invalidated frozen attempt")
	}
	fresh, err := svc.installationHMAC("new", "input", input)
	if err != nil || equalInstallationHMAC(before, fresh) {
		t.Fatal("active key ignored")
	}
	request, err := svc.installationHMAC("old", "request", input)
	if err != nil || equalInstallationHMAC(before, request) {
		t.Fatal("HMAC domains overlap")
	}
	if _, err = svc.installationHMAC("missing", "input", input); err == nil || strings.Contains(err.Error(), "synthetic secret") {
		t.Fatal("missing key did not fail safely")
	}
}

func TestInstallationEligibilityHMACBindsOperationContext(t *testing.T) {
	s, f, remote := fixturePinned()
	p := installationParams()
	remote.err = errors.New("timeout")
	_, _ = s.CreateInstallation(context.Background(), testCaller, "team", p)
	a := f.attempts[p.WorkspaceID+digestInstall(p.IdempotencyKey)]
	if a == nil {
		t.Fatal("missing attempt")
	}
	var plan pinnedInstallationPlan
	if err := json.Unmarshal(a.FrozenPlan, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.EligibilityHMAC) != 64 {
		t.Fatal("plan lacks eligibility MAC")
	}
	for _, field := range []string{"space", "caller", "workspace", "key", "plugin", "adapter", "intent", "input", "request", "key_id", "root_version"} {
		t.Run(field, func(t *testing.T) {
			caller, binding := testCaller, *a
			boundPlan := plan
			switch field {
			case "space":
				caller.SpaceID = "other-space"
			case "caller":
				caller.UID = "other-caller"
			case "workspace":
				binding.WorkspaceID = "other-workspace"
			case "key":
				binding.KeyDigest = digestInstall("other-key")
			case "plugin":
				binding.PluginID = "other-plugin"
			case "root_version":
				boundPlan.RootVersionID = "other-version"
			case "adapter":
				binding.AdapterVersion = "other-adapter"
			case "intent":
				binding.IntentDigest = digestInstall("other-intent")
			case "input":
				binding.InputFingerprint = digestInstall("other-input")
			case "request":
				binding.RequestFingerprint = digestInstall("other-request")
			case "key_id":
				binding.HMACKeyID = "same-material-other-id"
				s.installHMACKeys[binding.HMACKeyID] = s.installHMACKeys[a.HMACKeyID]
			}
			mac, err := s.installationEligibilityHMAC(caller, binding, &boundPlan)
			if err != nil || equalInstallationHMAC(plan.EligibilityHMAC, mac) {
				t.Fatalf("binding ignored: %v", err)
			}
		})
	}
	// Retry state changes do not invalidate the original graph authentication.
	mutable := *a
	mutable.Completed = true
	mutable.HadUnknown = true
	mutable.State = "SUCCEEDED"
	mutable.AgentID = "created"
	mutable.LeaseToken = "new-lease"
	check := func(a pluginrepo.InstallAttempt) {
		mac, err := s.installationEligibilityHMAC(testCaller, a, &plan)
		if err != nil || !equalInstallationHMAC(plan.EligibilityHMAC, mac) {
			t.Fatalf("mutable state invalidated MAC: %v", err)
		}
	}
	check(mutable)
	// Reformat/reorder object keys as a JSON database does; typed arrays remain exact.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(a.FrozenPlan, &fields); err != nil {
		t.Fatal(err)
	}
	formatted, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(formatted, &plan); err != nil {
		t.Fatal(err)
	}
	check(*a)
}
