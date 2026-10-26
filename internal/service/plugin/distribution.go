package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"sort"
	"strconv"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	libplugin "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

// LatestGraphStore is retained for legacy readers. Published A34 graphs must
// resolve through PinnedGraphStore so targets are the snapshotted pins.
type LatestGraphStore interface {
	ResolveLatestGraph(context.Context, pluginrepo.Scope, string) (*pluginrepo.LatestGraph, error)
}

// PinnedGraphStore resolves a published source snapshot to the exact pinned
// Skill/Expert versions it froze. Connectors remain ID + effective.
type PinnedGraphStore interface {
	ResolvePinnedGraph(context.Context, pluginrepo.Scope, string) (*pluginrepo.PinnedGraph, error)
}
type DistributionRoot struct {
	PluginID          string           `json:"plugin_id"`
	PluginType        model.PluginType `json:"plugin_type"`
	PluginName        string           `json:"plugin_name"`
	SelectedVersionID string           `json:"selected_version_id,omitempty"`
	EffectiveRevision *uint64          `json:"effective_revision,omitempty"`
}
type DistributionFile struct {
	Path        string `json:"path"`
	MIMEType    string `json:"mime_type"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash"`
}
type DistributionFileInfo struct {
	Path        string `json:"path"`
	MIMEType    string `json:"mime_type"`
	ContentSize int    `json:"content_size"`
	ContentHash string `json:"content_hash"`
}
type DistributionNode struct {
	NodeKey           string                  `json:"node_key"`
	PluginID          string                  `json:"plugin_id"`
	VersionID         string                  `json:"version_id,omitempty"`
	PluginType        model.PluginType        `json:"plugin_type"`
	VersionLabel      string                  `json:"version_label,omitempty"`
	PluginName        string                  `json:"plugin_name"`
	ManifestJSON      json.RawMessage         `json:"manifest_json" swaggertype:"object"`
	Files             *[]DistributionFileInfo `json:"files,omitempty"`
	EffectiveRevision *uint64                 `json:"effective_revision,omitempty"`
	EffectiveDigest   *string                 `json:"effective_digest,omitempty"`
}
type DistributionEdge struct {
	RelationID                string          `json:"relation_id"`
	SourceNodeKey             string          `json:"source_node_key"`
	TargetNodeKey             string          `json:"target_node_key"`
	SourceVersionID           string          `json:"source_version_id"`
	TargetPluginID            string          `json:"target_plugin_id"`
	TargetVersionID           string          `json:"target_version_id,omitempty"`
	ResolvedEffectiveRevision *uint64         `json:"resolved_effective_revision,omitempty"`
	RelationType              string          `json:"relation_type"`
	SortOrder                 int             `json:"sort_order"`
	Data                      json.RawMessage `json:"data" swaggertype:"object"`
}
type DistributionGraph struct {
	RootNodeKey string             `json:"root_node_key"`
	Nodes       []DistributionNode `json:"nodes"`
	Edges       []DistributionEdge `json:"edges"`
}
type DistributionAsset struct {
	NodeKey    string             `json:"node_key"`
	MarketName string             `json:"market_name"`
	Files      []DistributionFile `json:"files"`
	MCPConfig  json.RawMessage    `json:"mcp_config,omitempty" swaggertype:"object"`
}
type DistributionBundle struct {
	SchemaVersion string              `json:"schema_version"`
	Root          DistributionRoot    `json:"root"`
	Graph         DistributionGraph   `json:"graph"`
	Assets        []DistributionAsset `json:"assets"`
	BundleDigest  string              `json:"bundle_digest,omitempty"`
}

func contentSHA256(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// latestAsPinned adapts the pre-P graph for the explicit flag-off path. The two
// shapes carry the same fields, so the distribution builder is shared without a
// second code path.
func latestAsPinned(g *pluginrepo.LatestGraph) *pluginrepo.PinnedGraph {
	return &pluginrepo.PinnedGraph{RootID: g.RootID, Plugins: g.Plugins, Versions: g.Versions, Selected: g.Selected, Relations: g.Relations}
}

// Distribution materializes a single authorized graph without workspace/runtime
// credentials or Fleet calls. No partial response escapes if any file fails.
func (s *Service) Distribution(ctx context.Context, caller Caller, id string) (*DistributionBundle, error) {
	if !s.versionManagement {
		return nil, ErrNotFound
	}
	var graph *pluginrepo.PinnedGraph
	if s.references {
		// Published P graphs resolve only through their frozen pins. A repo that
		// cannot do so fails closed; it never silently distributes live heads.
		store, ok := s.repo.(PinnedGraphStore)
		if !ok {
			return nil, ErrVersionBaselineUnavailable
		}
		pinned, err := store.ResolvePinnedGraph(ctx, scope(caller), id)
		if err != nil {
			return nil, mapStoreError(err)
		}
		graph = pinned
	} else {
		// Legacy/flag-off deployments predate P pins. Preserve their existing
		// latest-release distribution; this branch is never used once references
		// (and therefore snapshotted pins) are enabled.
		store, ok := s.repo.(LatestGraphStore)
		if !ok {
			return nil, ErrVersionBaselineUnavailable
		}
		latest, err := store.ResolveLatestGraph(ctx, scope(caller), id)
		if err != nil {
			return nil, mapStoreError(err)
		}
		graph = latestAsPinned(latest)
	}
	var err error
	root := graph.Plugins[id]
	if root == nil || (root.Type != model.PluginTypeExpert && root.Type != model.PluginTypeExpertTeam) {
		return nil, ErrInvalidRequest
	}
	out := &DistributionBundle{SchemaVersion: "1.0", Graph: DistributionGraph{Nodes: []DistributionNode{}, Edges: []DistributionEdge{}}, Assets: []DistributionAsset{}}
	builder := capabilityBuilder{service: s, caller: caller, remaining: maxCapabilityBytes}
	keys := map[string]string{}
	ids := make([]string, 0, len(graph.Plugins))
	for id := range graph.Plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := *graph.Plugins[id]
		version := graph.Selected[id]
		node := DistributionNode{PluginID: id, PluginType: p.Type, VersionID: version}
		if p.Type == model.PluginTypeConnector {
			// Connector assets carry mcp_config only; expose an explicit empty
			// files list so clients never have to distinguish omitted vs empty.
			emptyFiles := []DistributionFileInfo{}
			node.Files = &emptyFiles
			if p.EffectiveRevision == nil || p.EffectiveDigest == nil {
				return nil, ErrIntegrity
			}
			canonicalManifest, hashErr := libplugin.CanonicalJSON(p.Manifest)
			payloadHash, payloadErr := libplugin.ComputePluginHash(p.Manifest, p.Package)
			if hashErr != nil || payloadErr != nil || hashJSON(canonicalManifest) != p.ManifestHash || payloadHash != p.PluginHash {
				return nil, ErrIntegrity
			}
			// The effective revision and its complete payload were read together.
			if *p.EffectiveDigest != model.EffectiveDigest(p) {
				return nil, ErrIntegrity
			}
			node.NodeKey = id + "@" + strconv.FormatUint(*p.EffectiveRevision, 10)
			node.EffectiveRevision, node.EffectiveDigest = p.EffectiveRevision, p.EffectiveDigest
		} else {
			v := graph.Versions[pluginrepo.InstallVersionKey{PluginID: id, VersionID: version}]
			if v == nil || v.Release == nil {
				return nil, ErrIntegrity
			}
			if err = s.verifyVersionFiles(ctx, &p, v); err != nil {
				return nil, err
			}
			p.Manifest, p.Package, p.AttachmentKeys = v.Manifest, v.Package, v.AttachmentKeys
			p.CurrentVersionID = &v.ID
			var manifest struct {
				Name string `json:"plugin_name"`
			}
			if json.Unmarshal(v.Manifest, &manifest) != nil || manifest.Name == "" {
				return nil, ErrIntegrity
			}
			p.Name = manifest.Name
			node.NodeKey = id + "@" + version
			node.VersionLabel = v.Release.VersionLabel
			list := []DistributionFileInfo{}
			node.Files = &list
		}
		node.PluginName, node.ManifestJSON = p.Name, p.Manifest
		asset := DistributionAsset{NodeKey: node.NodeKey, MarketName: p.Name, Files: []DistributionFile{}}
		var files map[string]string
		switch p.Type {
		case model.PluginTypeSkill:
			files, err = builder.skillFiles(ctx, &p)
			if err != nil {
				return nil, err
			}
			if files["SKILL.md"] == "" {
				return nil, ErrIntegrity
			}
		case model.PluginTypeExpert, model.PluginTypeExpertTeam:
			content, ok := rawAttachmentContent(p.Package, "AGENTS.md")
			if !ok {
				return nil, ErrIntegrity
			}
			if err = builder.text(content); err != nil {
				return nil, err
			}
			files = map[string]string{"AGENTS.md": content}
		case model.PluginTypeConnector:
			raw, ok := rawAttachmentContent(p.Package, "mcp.json")
			if !ok {
				return nil, ErrIntegrity
			}
			if err = builder.text(raw); err != nil {
				return nil, err
			}
			asset.MCPConfig, err = normalizeCapabilityMCP(raw)
			if err != nil {
				return nil, err
			}
			if len(asset.MCPConfig) == 0 {
				asset.MCPConfig = json.RawMessage(`{"mcpServers":{}}`)
			}
		default:
			return nil, ErrIntegrity
		}
		paths := make([]string, 0, len(files))
		for name := range files {
			paths = append(paths, name)
		}
		sort.Strings(paths)
		for _, name := range paths {
			content := files[name]
			mime := "text/plain"
			switch path.Ext(name) {
			case ".md":
				mime = "text/markdown"
			case ".json":
				mime = "application/json"
			case ".yaml", ".yml":
				mime = "application/yaml"
			}
			hash := contentSHA256([]byte(content))
			asset.Files = append(asset.Files, DistributionFile{Path: name, MIMEType: mime, Content: content, ContentHash: hash})
			*node.Files = append(*node.Files, DistributionFileInfo{Path: name, MIMEType: mime, ContentSize: len(content), ContentHash: hash})
		}
		keys[id] = node.NodeKey
		if id == graph.RootID {
			out.Root = DistributionRoot{PluginID: id, PluginType: p.Type, PluginName: p.Name, SelectedVersionID: version}
			out.Graph.RootNodeKey = node.NodeKey
		}
		out.Graph.Nodes = append(out.Graph.Nodes, node)
		out.Assets = append(out.Assets, asset)
	}
	for source, rels := range graph.Relations {
		for _, rel := range rels {
			target := graph.Plugins[rel.TargetPluginID]
			if target == nil {
				return nil, ErrIntegrity
			}
			data := rel.Data
			if len(data) == 0 {
				data = json.RawMessage(`{}`)
			}
			out.Graph.Edges = append(out.Graph.Edges, DistributionEdge{RelationID: rel.ID, SourceNodeKey: keys[source], TargetNodeKey: keys[target.ID], SourceVersionID: graph.Selected[source], TargetPluginID: target.ID, TargetVersionID: graph.Selected[target.ID], ResolvedEffectiveRevision: target.EffectiveRevision, RelationType: rel.Type, SortOrder: rel.SortOrder, Data: data})
		}
	}
	sort.Slice(out.Graph.Nodes, func(i, j int) bool { return out.Graph.Nodes[i].NodeKey < out.Graph.Nodes[j].NodeKey })
	sort.Slice(out.Assets, func(i, j int) bool { return out.Assets[i].NodeKey < out.Assets[j].NodeKey })
	sort.Slice(out.Graph.Edges, func(i, j int) bool {
		a, b := out.Graph.Edges[i], out.Graph.Edges[j]
		if a.SourceNodeKey != b.SourceNodeKey {
			return a.SourceNodeKey < b.SourceNodeKey
		}
		if a.RelationType != b.RelationType {
			return a.RelationType < b.RelationType
		}
		if a.SortOrder != b.SortOrder {
			return a.SortOrder < b.SortOrder
		}
		return a.RelationID < b.RelationID
	})
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, ErrIntegrity
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		return nil, ErrIntegrity
	}
	out.BundleDigest = contentSHA256(canonical)
	envelope, err := json.Marshal(struct {
		Data *DistributionBundle `json:"data"`
	}{out})
	if err != nil {
		return nil, ErrIntegrity
	}
	if len(envelope) > maxCapabilityBytes {
		return nil, ErrTooLarge
	}
	return out, nil
}
