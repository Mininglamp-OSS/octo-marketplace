package artifactscope

import (
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestPlatformAndSpaceArtifactIsolation(t *testing.T) {
	space := "space-a"
	platform := &model.Plugin{ID: "skill-a", Visibility: model.PluginVisibilitySystem}
	local := &model.Plugin{ID: "skill-a", Visibility: model.PluginVisibilitySpace, SpaceID: &space}
	if Prefix(platform) != "platform/plugins/skill-a/attachments/" {
		t.Fatal("platform prefix")
	}
	for _, tc := range []struct {
		p       *model.Plugin
		key     string
		allowed bool
	}{
		{platform, "platform/plugins/skill-a/attachments/versions/hash", true},
		{platform, "platform/plugins/skill-b/attachments/versions/hash", false},
		{platform, "plugins/space-a/attachments/file", false},
		{local, "platform/plugins/skill-a/attachments/versions/hash", false},
		{local, "plugins/space-a/attachments/file", true},
		{local, "plugins/space-b/attachments/file", false},
		{platform, "platform/plugins/skill-a/attachments/../secret", false},
		{platform, "platform/plugins/skill-a/attachments/", false},
		{platform, "platform/plugins/skill-a/attachments/a?query", false},
	} {
		if Allows(tc.p, tc.key) != tc.allowed {
			t.Fatalf("key %q allowed=%v", tc.key, tc.allowed)
		}
	}
	if Prefix(&model.Plugin{ID: "../foreign", Visibility: model.PluginVisibilitySystem}) != "" {
		t.Fatal("unsafe ID accepted")
	}
	if Prefix(&model.Plugin{ID: "skill-a", Visibility: model.PluginVisibilitySpace}) != "" {
		t.Fatal("missing Space accepted")
	}
	// Existing system resources may have an historical owning Space; its keys
	// remain valid, while that never grants another Space access.
	platform.SpaceID = &space
	if !Allows(platform, "plugins/space-a/attachments/legacy") || Allows(platform, "plugins/space-b/attachments/legacy") {
		t.Fatal("historical own-Space compatibility")
	}
}
