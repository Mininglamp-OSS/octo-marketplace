package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

var ErrVersionBaselineUnavailable = errors.New("version history migration required")

// VersionStore is the concrete persistence extension used only with the rollout
// enabled. The legacy Store contract stays compatible with existing adapters.
type VersionStore interface {
	GetVersionState(context.Context, pluginrepo.Scope, string) (*model.PluginVersionState, error)
	GetManagedVersion(context.Context, pluginrepo.Scope, string, string) (*model.PluginVersion, error)
	ListManagedVersions(context.Context, pluginrepo.Scope, string, int, int) ([]model.PluginVersion, int64, error)
	PublishManaged(context.Context, pluginrepo.Scope, pluginrepo.PublishParams) (*model.Plugin, error)
	AdoptPublishedBaseline(context.Context, pluginrepo.Scope, pluginrepo.AdoptParams) (*model.Plugin, error)
}

func (s *Service) WithVersionManagement(enabled bool) *Service {
	s.versionManagement = enabled
	if repo, ok := s.repo.(interface{ WithVersionManagement(bool) *pluginrepo.Repo }); ok {
		repo.WithVersionManagement(enabled)
	}
	return s
}

func (s *Service) versionStore() (VersionStore, error) {
	if !s.versionManagement {
		return nil, ErrNotFound
	}
	v, ok := s.repo.(VersionStore)
	if !ok {
		return nil, ErrInvalidRequest
	}
	return v, nil
}

// CurrentDetail is an explicit editing read; no non-owner can use it to obtain
// an unapproved draft even if the resource itself is public.
func (s *Service) CurrentDetail(ctx context.Context, caller Caller, pluginID string) (*Detail, error) {
	if _, err := s.versionStore(); err != nil {
		return nil, err
	}
	d, err := s.currentDetail(ctx, caller, pluginID, true)
	if err != nil {
		return nil, err
	}
	if d.Plugin.OwnerUID != caller.UID || d.Plugin.SpaceID == nil || *d.Plugin.SpaceID != caller.SpaceID || d.Plugin.IsEmbedded {
		return nil, ErrNotFound
	}
	// Never prefill a new draft with the previous formal release's notes.
	note := ""
	if d.Plugin.CurrentVersionID != nil {
		store, err := s.versionStore()
		if err != nil {
			return nil, err
		}
		v, err := store.GetManagedVersion(ctx, scope(caller), d.Plugin.ID, *d.Plugin.CurrentVersionID)
		if err != nil {
			return nil, mapStoreError(err)
		}
		if v.Release == nil && v.Changelog != nil {
			note = *v.Changelog
		}
	}
	d.DraftChangelog = &note
	return d, nil
}

// adoptPublishedBaseline is the explicit compatibility path for resources that
// were already published before managed releases existed. Re-publishing one
// registers its approved snapshot as the formal release instead of opening a
// review whose label the approval evidence already occupies. It returns (nil,
// nil) when a release already exists, so the ordinary publish flow still runs.
func (s *Service) adoptPublishedBaseline(ctx context.Context, caller Caller, d *Detail) (*Detail, error) {
	if !s.versionManagement || d.Plugin.Type == model.PluginTypeConnector || d.Plugin.CurrentVersionID == nil {
		return nil, nil
	}
	store, err := s.versionStore()
	if err != nil {
		return nil, err
	}
	sc := scope(caller)
	state, err := store.GetVersionState(ctx, sc, d.Plugin.ID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if state.PublishedVersionID != nil {
		return nil, nil
	}
	v, err := store.GetManagedVersion(ctx, sc, d.Plugin.ID, *d.Plugin.CurrentVersionID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if err = s.verifyVersionFiles(ctx, d.Plugin, v); err != nil {
		return nil, err
	}
	published, err := store.AdoptPublishedBaseline(ctx, sc, pluginrepo.AdoptParams{
		PluginID:          d.Plugin.ID,
		ExpectedVersionID: v.ID,
		OperatorID:        caller.UID,
		OperatorName:      caller.Name,
		RequestID:         caller.RequestID,
	})
	if err != nil {
		return nil, mapStoreError(err)
	}
	published.IconURL = s.resolveIcon(ctx, published.Icon)
	return &Detail{Plugin: published, Relations: d.Relations}, nil
}

// publishedDetail overlays only immutable content. Current permissions, listing,
// and moderation metadata remain authoritative; a draft cannot freeze access.
func (s *Service) publishedDetail(ctx context.Context, sc pluginrepo.Scope, p *model.Plugin, rels []model.PluginRelation, allowUnavailable bool) (*Detail, error) {
	if !s.versionManagement || p.Type == model.PluginTypeConnector {
		return &Detail{Plugin: p, Relations: rels}, nil
	}
	store, err := s.versionStore()
	if err != nil {
		return nil, err
	}
	state, err := store.GetVersionState(ctx, sc, p.ID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if state.PublishedVersionID == nil {
		// Explicit legacy compatibility: no release identity is invented. Drafts are
		// readable only by their authorized editor through the current-state path.
		return &Detail{Plugin: p, Relations: rels}, nil
	}
	return s.publishedDetailAt(ctx, sc, p, rels, *state.PublishedVersionID, allowUnavailable)
}

// publishedDetailAt overlays a specific released snapshot. Graph traversal uses
// it for a pinned team member so the member's later release cannot mask the
// version the team actually published.
func (s *Service) publishedDetailAt(ctx context.Context, sc pluginrepo.Scope, p *model.Plugin, rels []model.PluginRelation, versionID string, allowUnavailable bool) (*Detail, error) {
	if !s.versionManagement || p.Type == model.PluginTypeConnector {
		return &Detail{Plugin: p, Relations: rels}, nil
	}
	store, err := s.versionStore()
	if err != nil {
		return nil, err
	}
	v, err := store.GetManagedVersion(ctx, sc, p.ID, versionID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if v.Release == nil {
		return nil, ErrIntegrity
	}
	frozen, err := model.DecodeSnapshotRelations(v.Relations)
	if err != nil {
		return nil, ErrIntegrity
	}
	if !allowUnavailable {
		if err = s.checkHistoricalRelations(ctx, sc, frozen); err != nil {
			return nil, err
		}
	}
	copy := *p
	copy.Manifest = v.Manifest
	copy.Package = v.Package
	copy.AttachmentKeys = v.AttachmentKeys
	copy.ManifestHash = v.ManifestHash
	copy.PluginHash = v.PluginHash
	copy.CurrentVersionID = &v.ID
	copy.CurrentVersion = v.DeclaredVersion
	var manifest struct {
		Name       string          `json:"name"`
		PluginName string          `json:"plugin_name"`
		Labels     json.RawMessage `json:"labels"`
	}
	if json.Unmarshal(v.Manifest, &manifest) != nil {
		return nil, ErrIntegrity
	}
	if manifest.PluginName != "" {
		copy.Name = manifest.PluginName
	}
	// Missing/null legacy labels are an empty released tag set. Never inherit
	// editable tags from the current row when projecting a published snapshot.
	copy.Tags = json.RawMessage(`[]`)
	var labels []json.RawMessage
	if json.Unmarshal(manifest.Labels, &labels) == nil && labels != nil {
		copy.Tags = manifest.Labels
	}
	return &Detail{Plugin: &copy, Relations: frozen}, nil
}

func (s *Service) checkHistoricalRelations(ctx context.Context, sc pluginrepo.Scope, rels []model.PluginRelation) error {
	for _, rel := range rels {
		if _, _, err := s.repo.GetWithRelations(ctx, sc, rel.TargetPluginID); err != nil {
			if errors.Is(err, pluginrepo.ErrNotFound) {
				return ErrDependencyHidden
			}
			return mapStoreError(err)
		}
	}
	return nil
}

func (s *Service) ManagedVersion(ctx context.Context, caller Caller, pluginID, versionID string, admin bool) (*model.PluginVersion, error) {
	store, err := s.versionStore()
	if err != nil {
		return nil, err
	}
	sc := scope(caller)
	if admin {
		if !caller.IsSystemAdmin {
			return nil, ErrNotFound
		}
		sc = adminScope(caller)
	} else if err = validateCaller(caller); err != nil {
		return nil, err
	}
	pid, err := parseStorageID(pluginID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(versionID) == "" {
		return nil, ErrInvalidRequest
	}
	p, _, err := s.repo.GetWithRelations(ctx, sc, pid)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if p.Type == model.PluginTypeConnector {
		return nil, ErrNotFound
	}
	v, err := store.GetManagedVersion(ctx, sc, pid, versionID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	v.PluginType = p.Type
	rels, err := model.DecodeSnapshotRelations(v.Relations)
	if err != nil {
		return nil, ErrIntegrity
	}
	if err = s.checkHistoricalRelations(ctx, sc, rels); err != nil {
		return nil, err
	}
	return v, nil
}

// VersionFile returns verified original bytes, never a presigned mutable URL or
// a private storage key. Bounded reads complete before HTTP success is written.
func (s *Service) VersionFile(ctx context.Context, caller Caller, pluginID, versionID, filePath string, admin bool) ([]byte, error) {
	v, err := s.ManagedVersion(ctx, caller, pluginID, versionID, admin)
	if err != nil {
		return nil, err
	}
	if filePath == "" || path.Clean(filePath) != filePath || strings.HasPrefix(filePath, "/") || filePath == ".." || strings.HasPrefix(filePath, "../") || strings.ContainsAny(filePath, "\\\x00") {
		return nil, ErrInvalidRequest
	}
	sc := scope(caller)
	if admin {
		sc = adminScope(caller)
	}
	p, _, err := s.repo.GetWithRelations(ctx, sc, v.PluginID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if filePath == "skill/ref.json" {
		return nil, ErrNotFound
	}
	return s.versionFileBytes(ctx, p, v, filePath)
}

func (s *Service) versionFileBytes(ctx context.Context, p *model.Plugin, v *model.PluginVersion, filePath string) ([]byte, error) {
	if key, size, digest, ok, err := legacyVersionPointer(v, filePath); err != nil {
		return nil, err
	} else if ok {
		if s.storage == nil || !trustedArtifactKey(key, p.SpaceID) {
			return nil, ErrIntegrity
		}
		limit := s.maxAttachmentBytes
		if filePath == "skill/package.zip" || strings.HasSuffix(filePath, "/zip_object_key") || strings.HasSuffix(filePath, "/file_url") {
			limit = s.maxArchiveBytes
		}
		data, err := s.readArtifactBytes(ctx, key, limit)
		if err != nil {
			return nil, ErrIntegrity
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != size || digest != "sha256:"+hex.EncodeToString(sum[:]) {
			return nil, ErrIntegrity
		}
		return data, nil
	}
	keys := attachmentKeyMap(v.AttachmentKeys)
	for _, a := range decodePackageAttachments(v.Package) {
		if a.Path != filePath {
			continue
		}
		if a.ContentType == "raw" {
			if int64(len(a.RawContent)) > s.maxAttachmentBytes {
				return nil, ErrTooLarge
			}
			return []byte(a.RawContent), nil
		}
		if a.ContentType != "storage" || s.storage == nil {
			return nil, ErrIntegrity
		}
		key := keys[filePath]
		if !trustedArtifactKey(key, p.SpaceID) {
			return nil, ErrIntegrity
		}
		data, err := s.readArtifactBytes(ctx, key, s.maxAttachmentBytes)
		if err != nil {
			return nil, ErrIntegrity
		}
		sum := sha256.Sum256(data)
		if a.ContentSize != int64(len(data)) || a.ContentHash != "sha256:"+hex.EncodeToString(sum[:]) {
			return nil, ErrIntegrity
		}
		return data, nil
	}
	return nil, ErrNotFound
}

func (s *Service) verifyVersionFiles(ctx context.Context, p *model.Plugin, v *model.PluginVersion) error {
	var doc struct {
		Attachments []attachmentView `json:"attachments"`
	}
	if json.Unmarshal(v.Package, &doc) != nil {
		return ErrIntegrity
	}
	seen := map[string]bool{}
	var total int64
	// Review submissions predate a version inventory. Inspect their descriptor
	// directly so approval cannot skip missing original markdown/archive bytes.
	for _, field := range []string{"object_key", "zip_object_key", "file_url"} {
		filePath := "skill/ref.json/" + field
		if _, _, _, ok, err := legacyVersionPointer(v, filePath); err != nil {
			return err
		} else if ok {
			data, err := s.versionFileBytes(ctx, p, v, filePath)
			if err != nil {
				return err
			}
			total += int64(len(data))
			if total > s.maxArchiveBytes {
				return ErrTooLarge
			}
		}
	}
	for _, a := range doc.Attachments {
		if a.Path == "" || seen[a.Path] {
			return ErrIntegrity
		}
		seen[a.Path] = true
		data, err := s.versionFileBytes(ctx, p, v, a.Path)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > s.maxArchiveBytes {
			return ErrTooLarge
		}
	}
	return nil
}

func (s *Service) AdminPublishVersion(ctx context.Context, caller Caller, pluginID string) (*Detail, error) {
	store, err := s.versionStore()
	if err != nil {
		return nil, err
	}
	if !caller.IsSystemAdmin {
		return nil, ErrNotFound
	}
	d, err := s.AdminDetail(ctx, caller, pluginID, true)
	if err != nil {
		return nil, err
	}
	if d.Plugin.CurrentVersionID == nil {
		return nil, ErrVersionBaselineUnavailable
	}
	v, err := s.ManagedVersion(ctx, caller, pluginID, *d.Plugin.CurrentVersionID, true)
	if err != nil {
		return nil, err
	}
	if err = s.verifyVersionFiles(ctx, d.Plugin, v); err != nil {
		return nil, err
	}
	p, err := store.PublishManaged(ctx, adminScope(caller), pluginrepo.PublishParams{PluginID: d.Plugin.ID, ExpectedVersionID: v.ID, OperatorID: caller.UID, OperatorName: caller.Name, RequestID: caller.RequestID})
	if err != nil {
		return nil, mapStoreError(err)
	}
	return &Detail{Plugin: p, Relations: d.Relations}, nil
}

func (s *Service) HistoricalVersions(ctx context.Context, caller Caller, pluginID string, limit, offset int, admin bool) ([]model.PluginVersion, int64, error) {
	if !admin {
		return s.ListVersions(ctx, caller, pluginID, limit, offset)
	}
	store, err := s.versionStore()
	if err != nil {
		return nil, 0, err
	}
	if !caller.IsSystemAdmin {
		return nil, 0, ErrNotFound
	}
	if limit < 0 || limit > 100 || offset < 0 {
		return nil, 0, ErrInvalidRequest
	}
	id, err := parseStorageID(pluginID)
	if err != nil {
		return nil, 0, err
	}
	rows, n, err := store.ListManagedVersions(ctx, adminScope(caller), id, limit, offset)
	return rows, n, mapStoreError(err)
}

// approveWithVersionFiles validates frozen storage before the short DB decision.
// Storage bytes are immutable; the repository still rechecks authorization and
// state under locks. All approval entrances (web, policy, IM) use this boundary.
func (s *Service) approveWithVersionFiles(ctx context.Context, sc pluginrepo.Scope, params pluginrepo.ApproveReviewParams) (*model.Plugin, error) {
	if s.versionManagement {
		rr, err := s.repo.LoadReviewSnapshot(ctx, sc, params.ReviewID, true)
		if err != nil {
			return nil, err
		}
		p, _, err := s.repo.GetWithRelations(ctx, pluginrepo.Scope{CallerUID: rr.ApplicantUID, SpaceID: rr.SpaceID}, rr.PluginID)
		if err != nil {
			return nil, err
		}
		if p.Type != model.PluginTypeConnector {
			v := &model.PluginVersion{Package: rr.PluginJSON, AttachmentKeys: rr.AttachmentKeys}
			if err = s.verifyVersionFiles(ctx, p, v); err != nil {
				return nil, err
			}
		}
	}
	return s.repo.ApproveReview(ctx, sc, params)
}

func (s *Service) publishedPlugin(ctx context.Context, sc pluginrepo.Scope, id string) (*model.Plugin, error) {
	p, rels, err := s.repo.GetWithRelations(ctx, sc, id)
	if err != nil {
		return nil, err
	}
	if sc.Admin {
		return p, nil
	}
	d, err := s.publishedDetail(ctx, sc, p, rels, false)
	if err != nil {
		return nil, err
	}
	return d.Plugin, nil
}

// Read the published graph rather than a graph built from later draft edges.
// Each pinned release is a distinct graph node. Current authorization and graph
// caps still apply; legacy unpinned edges resolve the current published release.
func (s *Service) publishedGraph(ctx context.Context, caller Caller, id string) (*DetailGraph, error) {
	root, err := s.Detail(ctx, caller, id, true)
	if err != nil {
		return nil, err
	}
	if len(root.DependencyIssues) > 0 {
		// The author can diagnose a broken published graph without traversing a
		// deleted target. Ordinary readers were already rejected by Detail.
		return &DetailGraph{Plugin: root.Plugin, Relations: root.Relations, Related: []*model.Plugin{}, DependencyIssues: root.DependencyIssues}, nil
	}
	out := &DetailGraph{Plugin: root.Plugin, Relations: []model.PluginRelation{}, Related: []*model.Plugin{}}
	rootKey := publishedGraphVersionKey(id, root.Plugin.CurrentVersionID)
	seen := map[pluginrepo.InstallVersionKey]bool{rootKey: true}
	projected := map[pluginrepo.InstallVersionKey]bool{rootKey: true}
	omitted := map[pluginrepo.InstallVersionKey]bool{}
	queue := []*Detail{root}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		if len(out.Relations)+len(d.Relations) > pluginrepo.MaxGraphEdges() {
			return nil, ErrGraphTooLarge
		}
		out.Relations = append(out.Relations, d.Relations...)
		for _, r := range d.Relations {
			key := publishedGraphVersionKey(r.TargetPluginID, r.TargetVersionID)
			if seen[key] {
				continue
			}
			seen[key] = true
			// Bound raw selection work separately: aliases and unavailable
			// pins still cost a read, but do not each produce a graph node.
			// The root is already tracked and is not a related selection.
			if len(seen) > pluginrepo.MaxGraphEdges()+1 {
				return nil, ErrGraphTooLarge
			}
			next, err := s.publishedGraphDetail(ctx, caller, r.TargetPluginID, r.TargetVersionID)
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrDependencyHidden) {
				// Match GetGraphClosure: omit a blocked node and its edges rather
				// than failing the whole graph request.
				omitted[key] = true
				continue
			}
			if err != nil {
				return nil, err
			}
			// A legacy unpinned edge and an explicit pin may select the same
			// release. Project/expand that snapshot once, while retaining both
			// edges. Connector edges use the same current-resource identity.
			selected := publishedGraphVersionKey(next.Plugin.ID, next.Plugin.CurrentVersionID)
			if projected[selected] {
				continue
			}
			// Charge only a newly resolved snapshot to the related-node cap.
			if len(out.Related) >= pluginrepo.MaxGraphNodes() {
				return nil, ErrGraphTooLarge
			}
			projected[selected] = true
			light := *next.Plugin
			light.Package = nil
			light.AttachmentKeys = nil
			out.Related = append(out.Related, &light)
			queue = append(queue, next)
		}
	}
	if len(omitted) > 0 {
		kept := out.Relations[:0]
		for _, rel := range out.Relations {
			if !omitted[publishedGraphVersionKey(rel.TargetPluginID, rel.TargetVersionID)] {
				kept = append(kept, rel)
			}
		}
		out.Relations = kept
	}
	return out, nil
}

func publishedGraphVersionKey(id string, version *string) pluginrepo.InstallVersionKey {
	key := pluginrepo.InstallVersionKey{PluginID: id}
	if version != nil {
		key.VersionID = *version
	}
	return key
}

// publishedGraphDetail resolves a related node without the public catalog gate.
// When the parent edge carries a version pin, it overlays that exact release so
// a later member release cannot hide the version the team published.
func (s *Service) publishedGraphDetail(ctx context.Context, caller Caller, pluginID string, versionID *string) (*Detail, error) {
	d, err := s.currentDetail(ctx, caller, pluginID, true)
	if err != nil {
		return nil, err
	}
	sc := scope(caller)
	if versionID != nil && s.versionManagement && d.Plugin.Type != model.PluginTypeConnector {
		return s.publishedDetailAt(ctx, sc, d.Plugin, d.Relations, *versionID, false)
	}
	return s.publishedDetail(ctx, sc, d.Plugin, d.Relations, false)
}

func legacyVersionPointer(v *model.PluginVersion, filePath string) (key string, size int64, digest string, ok bool, err error) {
	field := ""
	switch filePath {
	case "SKILL.md":
		field = "object_key"
	case "skill/package.zip":
		field = "zip_object_key"
	case "skill/ref.json/object_key":
		field = "object_key"
	case "skill/ref.json/zip_object_key":
		field = "zip_object_key"
	case "skill/ref.json/file_url":
		field = "file_url"
	default:
		return
	}
	raw, exists := rawAttachmentContent(v.Package, "skill/ref.json")
	if !exists {
		return
	}
	var ref map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &ref) != nil {
		err = ErrIntegrity
		return
	}
	_ = json.Unmarshal(ref[field], &key)
	if key == "" && field == "zip_object_key" {
		field = "file_url"
		_ = json.Unmarshal(ref[field], &key)
	}
	if key == "" {
		return
	}
	ok = true
	if json.Unmarshal(ref[field+"_size"], &size) != nil || json.Unmarshal(ref[field+"_hash"], &digest) != nil || size < 0 || len(digest) != 71 {
		err = ErrIntegrity
	}
	return
}
