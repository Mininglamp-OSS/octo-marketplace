package plugin

import "testing"

func TestLegacyNameColumnsUsesDraftAndPublishedReservations(t *testing.T) {
	draft, published, scope, err := legacyNameColumns(plugRow{name: "Alpha", typ: "skill", space: "space-a", versionID: "v1"})
	if err != nil || draft != "alpha" || published != "alpha" || scope != "space-a" {
		t.Fatalf("draft=%v published=%v scope=%q err=%v", draft, published, scope, err)
	}
}
