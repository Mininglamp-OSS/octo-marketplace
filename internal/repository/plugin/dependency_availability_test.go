package plugin

import (
	"strings"
	"testing"
)

func TestUnavailableDirectSQLRejectsUnknownAlias(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("unknown source alias must panic instead of being concatenated")
		}
	}()
	unavailableDirectSQL("p; DROP TABLE plugins")
}

func TestUnavailableTargetSQLFormalNullPinUsesPublishedRelease(t *testing.T) {
	formal := unavailableTargetSQL("ref", "target", true)
	if !strings.Contains(formal, "COALESCE(ref.target_version_id,target.published_version_id)") {
		t.Fatalf("formal release check missing published fallback: %s", formal)
	}
	live := unavailableTargetSQL("live", "target", false)
	if strings.Contains(live, "COALESCE(") || !strings.Contains(live, "live.target_version_id IS NOT NULL") {
		t.Fatalf("live release check must keep the explicit-pin rule: %s", live)
	}
}
