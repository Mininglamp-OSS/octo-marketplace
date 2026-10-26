package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	libplugin "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

type distributionFixture struct {
	Store
	graph *pluginrepo.LatestGraph
	err   error
}

func (f *distributionFixture) ResolveLatestGraph(context.Context, pluginrepo.Scope, string) (*pluginrepo.LatestGraph, error) {
	return f.graph, f.err
}
func distributionTestService(t *testing.T) (*Service, *distributionFixture) {
	t.Helper()
	_, f, _ := fixturePinned()
	// Keep a single expert root, one Skill and one effective Connector.
	g := &pluginrepo.LatestGraph{RootID: "expert-a", Plugins: map[string]*model.Plugin{}, Versions: map[pluginrepo.InstallVersionKey]*model.PluginVersion{}, Selected: map[string]string{}, Relations: map[string][]model.PluginRelation{}}
	for _, id := range []string{"expert-a", "skill-a", "connector"} {
		g.Plugins[id] = f.plugins[id]
	}
	for key, v := range f.versions {
		if key.PluginID == "expert-a" || key.PluginID == "skill-a" {
			g.Versions[key] = v
			g.Selected[key.PluginID] = key.VersionID
			v.Release.VersionLabel = "1.0.0"
			rels, err := model.DecodeSnapshotRelations(v.Relations)
			if err != nil {
				t.Fatal(err)
			}
			g.Relations[key.PluginID] = rels
		}
	}
	p := g.Plugins["connector"]
	revision := uint64(2)
	p.EffectiveRevision = &revision
	p.Manifest = json.RawMessage(`{"plugin_name":"Connector"}`)
	p.ManifestHash = hashJSON(p.Manifest)
	p.PluginHash, _ = libplugin.ComputePluginHash(p.Manifest, p.Package)
	digest := "sha256:" + contentSHA256([]byte(p.ManifestHash+"\x00"+p.PluginHash))
	p.EffectiveDigest = &digest
	p.Manifest = json.RawMessage(`{"plugin_name":"Connector"}`)
	store := &distributionFixture{Store: &fakeStore{}, graph: g}
	return New(store).WithVersionManagement(true), store
}
func TestDistributionOriginalContentAndJCSDigest(t *testing.T) {
	s, f := distributionTestService(t)
	out, err := s.Distribution(context.Background(), testCaller, "expert-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Graph.Nodes) != 3 || len(out.Assets) != 3 || len(out.Graph.Edges) != 2 || out.Root.SelectedVersionID != "expert-v1" {
		t.Fatalf("incomplete response: %+v", out)
	}
	for _, asset := range out.Assets {
		if strings.HasPrefix(asset.NodeKey, "skill-a@") {
			if asset.MarketName != "Search a" || len(asset.Files) != 2 || !strings.Contains(asset.Files[0].Content, "name: Search\n") {
				t.Fatalf("original bytes/name changed: %+v", asset)
			}
			for _, file := range asset.Files {
				if file.ContentHash != contentSHA256([]byte(file.Content)) {
					t.Fatal("file hash mismatch")
				}
			}
		}
		if strings.HasPrefix(asset.NodeKey, "connector@") && (len(asset.Files) != 0 || len(asset.MCPConfig) == 0) {
			t.Fatal("missing effective config")
		}
	}
	digest := out.BundleDigest
	out.BundleDigest = ""
	raw, _ := json.Marshal(out)
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil || contentSHA256(canonical) != digest {
		t.Fatal("invalid JCS bundle digest")
	}
	// This snapshot is changed to simulate the next independently resolved read.
	p := f.graph.Plugins["connector"]
	p.Package = packageWith(rawAtt("mcp.json", `{"mcpServers":{"search":{"command":"search-v2"}}}`))
	p.PluginHash, _ = libplugin.ComputePluginHash(p.Manifest, p.Package)
	d := "sha256:" + contentSHA256([]byte(p.ManifestHash+"\x00"+p.PluginHash))
	p.EffectiveDigest = &d
	*p.EffectiveRevision = 3
	next, err := s.Distribution(context.Background(), testCaller, "expert-a")
	if err != nil || next.BundleDigest == digest {
		t.Fatalf("new effective content ignored: %v", err)
	}
}
func TestDistributionFailsWholeResponse(t *testing.T) {
	for _, kind := range []string{"missing dependency", "bad effective digest", "binary skill", "missing skill"} {
		t.Run(kind, func(t *testing.T) {
			s, f := distributionTestService(t)
			switch kind {
			case "missing dependency":
				f.err = pluginrepo.ErrNotFound
			case "bad effective digest":
				d := "wrong"
				f.graph.Plugins["connector"].EffectiveDigest = &d
			case "binary skill":
				f.graph.Versions[pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: "skill-v1"}].Package = packageWith(rawAtt("SKILL.md", "\x00"))
			case "missing skill":
				f.graph.Versions[pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: "skill-v1"}].Package = packageWith(rawAtt("notes.txt", "notes"))
			}
			out, err := s.Distribution(context.Background(), testCaller, "expert-a")
			if err == nil || out != nil {
				t.Fatalf("partial success: %+v %v", out, err)
			}
			if kind == "missing dependency" && !errors.Is(err, ErrNotFound) {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}
