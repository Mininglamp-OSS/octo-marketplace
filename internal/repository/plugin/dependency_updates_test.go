package plugin

import (
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"testing"
)

func TestUpdateTargetBoundAndRevisionKind(t *testing.T) {
	v := model.DependencyUpdateTarget{PluginID: "skill", TargetVersionID: "release"}
	for _, targets := range [][]model.DependencyUpdateTarget{nil, {v, v}, {{PluginID: "both", TargetVersionID: "v", TargetConfigRevision: "c"}}, {{TargetVersionID: "v"}}, make([]model.DependencyUpdateTarget, 101)} {
		if ValidUpdateTargets(targets) {
			t.Fatal("invalid targets accepted")
		}
	}
	if !ValidUpdateTargets([]model.DependencyUpdateTarget{v, {PluginID: "connector", TargetConfigRevision: "hash"}}) {
		t.Fatal("valid mixed selection refused")
	}
}
