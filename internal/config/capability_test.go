package config

import "testing"

func TestCapabilityInstallRequiresExplicitEnable(t *testing.T) {
	for _, value := range []string{"", "false", "invalid", "true"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("OCTO_FLEET_CAPABILITY_INSTALL_ENABLED", value)
			if got := Load().FleetCapabilityInstallEnabled; got != (value == "true") {
				t.Fatalf("enabled=%v for %q", got, value)
			}
		})
	}
}
