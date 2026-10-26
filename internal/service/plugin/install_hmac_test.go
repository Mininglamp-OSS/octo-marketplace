package plugin

import (
	"strings"
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
