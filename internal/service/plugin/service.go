// Package plugin implements business rules for the unified Plugin domain.
// It has no HTTP or storage concerns; caller identity and Space are supplied by
// the trusted authentication boundary and are always passed to the repository.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/id"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/notify"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

var (
	ErrNotFound       = errors.New("plugin not found")
	ErrConflict       = errors.New("plugin conflict")
	ErrInvalidRequest = errors.New("invalid plugin request")
	ErrTooLarge       = errors.New("plugin artifact exceeds size limit")
	// ErrDeadlock is returned when a review transaction was chosen as the InnoDB
	// deadlock victim (submit and the decision paths take the plugin/request rows
	// in opposite orders). It is TRANSIENT — the aborted transaction did not
	// commit and a retry succeeds — and deliberately distinct from ErrConflict so
	// the IM card path does not treat it as a settled decision and silently
	// discard a real admin's click. Web decision handlers surface it as a
	// retryable 409; the card path lets it fall through to a 503 so octo-server
	// redelivers.
	ErrDeadlock = errors.New("plugin transaction deadlock, retry")
	// ErrDependencyHidden is returned when an install cannot see every declared
	// relation target, so the full published topology cannot be reproduced —
	// refused loudly rather than provisioning a partial expert/squad (P1-1).
	ErrDependencyHidden = errors.New("plugin install dependency not accessible to caller")
	// ErrIntegrity is returned when a stored artifact's bytes do not match the
	// content_hash/content_size recorded for it at publish time — a
	// content-addressed object must never be served under a mismatched digest.
	ErrIntegrity = errors.New("plugin artifact integrity check failed")
)

// Caller is populated from verified authentication context, never request JSON.
type Caller struct {
	UID           string
	Name          string
	SpaceID       string
	BotUID        string
	BotName       string
	RequestID     string
	IsSystemAdmin bool
	// SpaceRole is the caller's role in SpaceID using octo-server's
	// space_member.role encoding (0=member, 1=admin, 2=owner). Populated by the
	// HTTP layer from the verified identity's space_roles map, never from request
	// data. Reviewer-side review operations require >= SpaceRoleAdmin.
	SpaceRole int
}

// Store is the transactional persistence boundary required by Service.
// Implementations must apply Scope to every tenant-owned lookup and mutation.
type Store interface {
	List(context.Context, pluginrepo.Scope, pluginrepo.ListFilter) ([]model.Plugin, int64, error)
	GetWithRelations(context.Context, pluginrepo.Scope, string) (*model.Plugin, []model.PluginRelation, error)
	Create(context.Context, pluginrepo.Scope, pluginrepo.Mutation) (*pluginrepo.RelationSync, error)
	CreateGraph(context.Context, pluginrepo.Scope, []pluginrepo.Mutation) ([]*pluginrepo.RelationSync, error)
	RebuildGraph(context.Context, pluginrepo.Scope, pluginrepo.Mutation, []pluginrepo.Mutation) (*pluginrepo.RelationSync, error)
	Update(context.Context, pluginrepo.Scope, pluginrepo.Mutation) (*pluginrepo.RelationSync, error)
	Delete(context.Context, pluginrepo.Scope, string, string, string, string, *string) error
	DeleteGraph(context.Context, pluginrepo.Scope, string, string, string, string, *string) error
	ListVersions(context.Context, pluginrepo.Scope, string, int, int) ([]model.PluginVersion, int64, error)
	CountMemberRelations(context.Context, []string) (map[string]int, error)
	CountDeclaredRelations(context.Context, string) (int, error)
	ListTags(context.Context, pluginrepo.Scope, pluginrepo.TagListFilter) ([]model.TagFilter, error)

	// Space review requests. Every method carries the caller Scope; the
	// AnySpace variant is the single deliberate exception and is documented at
	// its definition.
	InsertReviewRequest(context.Context, pluginrepo.Scope, *model.PluginReviewRequest, pluginrepo.FrozenSnapshot) error
	GetReviewRequest(context.Context, pluginrepo.Scope, string, bool) (*model.PluginReviewRequest, error)
	LoadReviewSnapshot(context.Context, pluginrepo.Scope, string, bool) (*model.PluginReviewRequest, error)
	ListReviewRequests(context.Context, pluginrepo.Scope, pluginrepo.ReviewListFilter) ([]*model.PluginReviewRequest, int64, error)
	HasPendingReview(context.Context, pluginrepo.Scope, string) (bool, error)
	LatestReviewForPlugin(context.Context, pluginrepo.Scope, string) (string, model.ReviewStatus, error)

	// Listing lifecycle. These are the only two writers of listing_state besides
	// insertPlugin and ApproveReview.
	PublishPlugin(context.Context, pluginrepo.Scope, pluginrepo.PublishParams) (*model.Plugin, error)
	DelistPlugin(context.Context, pluginrepo.Scope, pluginrepo.DelistParams) (*model.Plugin, error)
	ApproveReview(context.Context, pluginrepo.Scope, pluginrepo.ApproveReviewParams) (*model.Plugin, error)
	RejectReview(context.Context, pluginrepo.Scope, pluginrepo.RejectReviewParams) (json.RawMessage, map[string]struct{}, error)
	CancelReview(context.Context, pluginrepo.Scope, string, string) (json.RawMessage, map[string]struct{}, error)
	GetReviewRequestAnySpace(context.Context, string) (*model.PluginReviewRequest, error)
	GetReviewPolicy(context.Context, pluginrepo.Scope) (model.PluginReviewPolicy, error)
	UpsertReviewPolicy(context.Context, pluginrepo.Scope, bool, string, string) (model.PluginReviewPolicy, error)

	// Card-action receipts (IM decision idempotency).
	GetCardActionReceipt(context.Context, string) (*model.CardActionReceipt, error)
	InsertCardActionReceipt(context.Context, *model.CardActionReceipt) error
}

var _ Store = (*pluginrepo.Repo)(nil)

type Service struct {
	repo               Store
	storage            storage.Storage
	provisioner        Provisioner
	metrics            InstallTracker
	parseTasks         ParseTaskStore
	id                 func() string
	now                func() time.Time
	maxAttachmentBytes int64
	maxArchiveBytes    int64

	// Review IM notification wiring. Both are optional and are used ONLY by the
	// IM surface: a nil notifier means no approval card is dispatched, and the
	// card-action callback cannot verify an operator (it faults rather than
	// granting). Web-path review authorization never consults either of these —
	// it reads Caller.SpaceRole — so review works whether or not IM is wired.
	notify     ReviewNotifier
	bestEffort func(string, func(ctx context.Context) error)
}

func New(repo Store, stores ...storage.Storage) *Service {
	s := &Service{
		repo:               repo,
		id:                 id.New,
		now:                func() time.Time { return time.Now().UTC() },
		maxAttachmentBytes: defaultMaxAttachmentBytes,
		maxArchiveBytes:    defaultMaxArchiveBytes,
	}
	if len(stores) > 0 {
		s.storage = stores[0]
	}
	return s
}

// ReviewNotifier is the subset of *notify.Client the review workflow uses. The
// compile-time assertion keeps it honest: the interface exists for test
// substitution, not to re-abstract the client.
type ReviewNotifier interface {
	Enabled() bool
	// MemberRole reports uid's role in spaceID. A nil role means "not an active
	// member of an active Space" — non-member, removed member, unknown Space and
	// disbanded Space are deliberately indistinguishable. An error means the
	// lookup could not be performed and must NEVER be treated as a refusal.
	MemberRole(ctx context.Context, spaceID, uid string) (*int, error)
	NotifySpaceAdmins(ctx context.Context, req notify.NotifyRequest) (*notify.NotifyResponse, error)
}

var _ ReviewNotifier = (*notify.Client)(nil)

// WithNotify wires the octo-server client used for IM approval cards and the
// post-commit dispatcher that runs them. Both are optional; leaving them unset
// disables IM dispatch and makes the card-action callback report a fault rather
// than authorizing anyone.
//
// This deliberately does NOT govern web-path authorization: that reads
// Caller.SpaceRole, so approving from the web works with IM entirely unwired.
func (s *Service) WithNotify(n ReviewNotifier, bestEffort func(string, func(ctx context.Context) error)) *Service {
	s.notify = n
	s.bestEffort = bestEffort
	return s
}

// SetArtifactLimits applies deployment upload size to individual attachments and
// keeps a larger but bounded aggregate archive limit.
func (s *Service) SetArtifactLimits(maxAttachmentBytes int64) {
	if maxAttachmentBytes > 0 {
		s.maxAttachmentBytes = maxAttachmentBytes
		s.maxArchiveBytes = maxAttachmentBytes * 5
	}
}

// MaxArchiveBytes is the hard ceiling on an uploaded archive's raw bytes: the
// top-level container import/reupload size check enforces it, and the HTTP
// handler caps the multipart body at the SAME value so the transport limit and
// the service limit are one number driven by MAX_UPLOAD_MB (not two independent
// constants).
func (s *Service) MaxArchiveBytes() int64 { return s.maxArchiveBytes }

// WithRuntime is intended for deterministic tests and process wiring that uses
// a shared ID generator or clock.
func (s *Service) WithRuntime(idGen func() string, now func() time.Time) *Service {
	if idGen != nil {
		s.id = idGen
	}
	if now != nil {
		s.now = now
	}
	return s
}

type ListParams struct {
	PlacementCode string
	Type          model.PluginType
	CategoryID    string
	Tags          []string
	Keyword       string
	Mine          bool
	Sort          string
	Limit         int
	Offset        int
}

type Detail struct {
	Plugin    *model.Plugin
	Relations []model.PluginRelation
	// RelationResult reports upsert relation synchronization; nil on reads.
	RelationResult *RelationResult
}

// RelationResult mirrors the target-state relation sync outcome on the wire:
// empty relation_id created, known relation_id with changes updated, live
// relations absent from the submission deleted.
type RelationResult struct {
	Created []string
	Updated []string
	Deleted []string
}

type WriteRequest struct {
	// grandfatheredVersion is the plugin's STORED version label, set only by
	// Service.update. A label minted before the format was tightened (v999,
	// 1.0.0lll) no longer passes validVersion, and every save re-sends the stored
	// value — so without this exemption tightening the format would retroactively
	// make those rows permanently unsavable. Unexported on purpose: a request body
	// must never be able to claim its own value is grandfathered.
	grandfatheredVersion string

	Name       string
	Type       model.PluginType
	CategoryID *string
	Tags       json.RawMessage
	Publisher  string
	// Icon is a persistent public URL or a managed storage object key; it is
	// display metadata kept outside the content-addressed manifest.
	Icon       string
	Visibility model.PluginVisibility
	Manifest   json.RawMessage
	Package    json.RawMessage
	Relations  []RelationRequest
	// Version is the caller-declared current-version label written to
	// plugins.current_version. Empty defaults to "1.0.0". It is independent of the
	// per-save history label (plugin_versions.version stays an auto-increment int).
	Version string
	// Changelog is the optional note recorded on the version snapshot this write
	// appends. The tenant upsert path leaves it nil; skill import carries the
	// uploaded changelog through to the snapshot.
	Changelog *string
}

type RelationRequest struct {
	// ID is the submitted relation_id: empty creates, a live relation's ID
	// updates, and live relations omitted from the submission are deleted.
	ID string
	// SourcePluginID, when supplied, must address the Plugin being upserted.
	SourcePluginID string
	TargetPluginID string
	Type           string
	SortOrder      int
	Data           json.RawMessage
}

func scope(c Caller) pluginrepo.Scope { return pluginrepo.Scope{CallerUID: c.UID, SpaceID: c.SpaceID} }

// writeScope is the scope relation targets are resolved under during a write:
// the caller's tenant scope normally, but the cross-Space admin scope on the
// admin write path so a space-scoped target stays visible (matching the repo's
// admin-aware lockRelationTargets).
func writeScope(c Caller, admin bool) pluginrepo.Scope {
	if admin {
		return adminScope(c)
	}
	return scope(c)
}

func (s *Service) List(ctx context.Context, caller Caller, p ListParams) ([]model.Plugin, int64, error) {
	if err := validateCaller(caller); err != nil {
		return nil, 0, err
	}
	if p.Type != "" && !validPluginType(p.Type) {
		return nil, 0, ErrInvalidRequest
	}
	switch p.Sort {
	case "", "newest", "oldest", "updated", "name", "placement", "views", "installs", "downloads", "comprehensive":
	default:
		return nil, 0, ErrInvalidRequest
	}
	if p.Sort == "placement" && strings.TrimSpace(p.PlacementCode) == "" {
		return nil, 0, ErrInvalidRequest
	}
	if p.Limit < 0 || p.Limit > maxListLimit || p.Offset < 0 {
		return nil, 0, ErrInvalidRequest
	}
	tags, err := normalizeListTags(p.Tags)
	if err != nil {
		return nil, 0, err
	}
	items, total, err := s.repo.List(ctx, scope(caller), pluginrepo.ListFilter{PlacementCode: strings.TrimSpace(p.PlacementCode), Type: p.Type, CategoryID: strings.TrimSpace(p.CategoryID), Tags: tags, Keyword: strings.TrimSpace(p.Keyword), Mine: p.Mine, Sort: strings.TrimSpace(p.Sort), Limit: p.Limit, Offset: p.Offset, IncludeReviewState: p.Mine})
	if err != nil {
		return nil, 0, mapStoreError(err)
	}
	if err := s.fillMemberCounts(ctx, items); err != nil {
		return nil, 0, mapStoreError(err)
	}
	for i := range items {
		items[i].IconURL = s.resolveIcon(ctx, items[i].Icon)
	}
	return items, total, nil
}

// TagListParams drives the aggregated tag suggestions; the visible set mirrors
// List so callers only see tags on Plugins they could open.
type TagListParams struct {
	PlacementCode string
	Type          model.PluginType
	Keyword       string
	Mine          bool
	Limit         int
}

func (s *Service) ListTags(ctx context.Context, caller Caller, p TagListParams) ([]model.TagFilter, error) {
	if err := validateCaller(caller); err != nil {
		return nil, err
	}
	if p.Type != "" && !validPluginType(p.Type) {
		return nil, ErrInvalidRequest
	}
	if p.Limit < 0 || p.Limit > maxListLimit {
		return nil, ErrInvalidRequest
	}
	tags, err := s.repo.ListTags(ctx, scope(caller), pluginrepo.TagListFilter{
		PlacementCode: strings.TrimSpace(p.PlacementCode),
		Type:          p.Type,
		Keyword:       strings.TrimSpace(p.Keyword),
		Mine:          p.Mine,
		Limit:         p.Limit,
	})
	if err != nil {
		return nil, mapStoreError(err)
	}
	return tags, nil
}

// fillMemberCounts derives list-only member counts for expert teams in one
// batched relation query instead of persisting a column that upsert, relation
// sync, and delete would each have to keep consistent.
func (s *Service) fillMemberCounts(ctx context.Context, items []model.Plugin) error {
	var teamIDs []string
	for i := range items {
		if items[i].Type == model.PluginTypeExpertTeam {
			teamIDs = append(teamIDs, items[i].ID)
		}
	}
	if len(teamIDs) == 0 {
		return nil
	}
	counts, err := s.repo.CountMemberRelations(ctx, teamIDs)
	if err != nil {
		return err
	}
	for i := range items {
		items[i].MemberCount = counts[items[i].ID]
	}
	return nil
}

// iconNamespaces are the only storage roots resolveIcon will presign: the
// legacy icon upload endpoints generate keys under them. Icon is a
// caller-writable field, so presigning arbitrary key shapes would hand out
// signed URLs for unrelated objects (e.g. another Space's attachments).
var iconNamespaces = []string{"icons/", "mcp-icons/"}

func presignableIconKey(icon string) bool {
	if !iconKeyPattern.MatchString(icon) || strings.Contains(icon, "..") {
		return false
	}
	for _, namespace := range iconNamespaces {
		if strings.HasPrefix(icon, namespace) {
			return true
		}
	}
	return false
}

// resolveIcon turns a stored icon object key into a time-limited display URL
// while passing persistent http(s) URLs and text glyphs (legacy emoji icons)
// through unchanged. Only known icon namespaces are presigned; resolution
// failures keep the raw value, matching the legacy skill icon behavior.
func (s *Service) resolveIcon(ctx context.Context, icon string) string {
	if icon == "" {
		return ""
	}
	if strings.HasPrefix(icon, "http://") || strings.HasPrefix(icon, "https://") || s.storage == nil || !presignableIconKey(icon) {
		return icon
	}
	if resolved, err := s.storage.PresignGet(ctx, icon, time.Hour); err == nil {
		return resolved
	}
	return icon
}

// normalizeListTags trims, deduplicates, and bounds the AND-combined tag
// filters so a request cannot inflate the query with unbounded predicates.
func normalizeListTags(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, tag := range raw {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if !utf8.ValidString(tag) || len(tag) > maxTagBytes {
			return nil, ErrInvalidRequest
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	if len(out) > maxListTags {
		return nil, ErrInvalidRequest
	}
	return out, nil
}

func (s *Service) Detail(ctx context.Context, caller Caller, pluginID string, includeRelations bool) (*Detail, error) {
	if validateCaller(caller) != nil {
		return nil, ErrInvalidRequest
	}
	storageID, err := parseStorageID(pluginID)
	if err != nil {
		return nil, err
	}
	p, rels, err := s.repo.GetWithRelations(ctx, scope(caller), storageID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	p.IconURL = s.resolveIcon(ctx, p.Icon)
	// The detail read carries the same derived status as the mine listing, or a card
	// would show 审核中 and the detail page it opens would show nothing.
	//
	// Owner-only, deliberately. Everyone in the Space can read a listed plugin, but
	// only its author has any business knowing that an update is sitting in the
	// review queue. LatestReviewForPlugin orders pending first, so one lookup
	// answers both "is there an open request" and "what happened last".
	if p.OwnerUID == caller.UID {
		reviewID, status, err := s.repo.LatestReviewForPlugin(ctx, scope(caller), p.ID)
		if err != nil {
			return nil, mapStoreError(err)
		}
		p.LatestReviewID, p.LatestReviewStatus = reviewID, status
		p.HasPendingReview = status == model.ReviewStatusPending
	}
	if !includeRelations {
		rels = []model.PluginRelation{}
	} else {
		for i := range rels {
			rels[i].SourcePluginType = p.Type
		}
	}
	return &Detail{Plugin: p, Relations: rels}, nil
}

func (s *Service) Create(ctx context.Context, caller Caller, req WriteRequest) (*Detail, error) {
	return s.createWithID(ctx, caller, req, "")
}

// createWithID is Create with an optional caller-reserved plugin ID. Import
// reserves the ID up front — it is baked into the SKILL.md frontmatter and used
// to namespace the spilled attachment object keys — so the persisted row must
// carry that same ID rather than minting a second one, or the shipped id, the
// object namespace, and the row would all disagree. Every create records a
// plugin_versions snapshot — a save IS a version.
func (s *Service) createWithID(ctx context.Context, caller Caller, req WriteRequest, reservedID string) (*Detail, error) {
	if err := validateCaller(caller); err != nil {
		return nil, err
	}
	// A fresh tenant plugin is always a DRAFT, whatever visibility it declares.
	// visibility is an intent ("who should see this once it is listed"), and
	// listing_state is what actually lists it — so there is no longer anything to
	// clamp here: declaring 仅本组织可见 on a draft is legal and lists nothing until
	// Publish routes it through review and ApproveReview stamps published.
	//
	// The value is still validated, so `public` and garbage stay 400s rather than
	// being silently rewritten. buildWrite stamps listing_state=draft; a system
	// admin is left alone because `system` rows are not tenant-owned and not
	// subject to Space review.
	if !caller.IsSystemAdmin && !validVisibility(req.Visibility, false) {
		return nil, ErrInvalidRequest
	}
	now := s.now()
	p, rels, err := s.buildWrite(ctx, caller, "", req, now, false)
	if err != nil {
		return nil, err
	}
	p.ID = reservedID
	if p.ID == "" {
		p.ID = s.id()
	}
	for i := range rels {
		rels[i].SourcePluginID = p.ID
	}
	audit := s.audit(caller, p.ID, "create", nil, p, now)
	m := mutation(*p, rels, audit)
	m.SnapshotVersion = true
	m.Changelog = req.Changelog
	// Every create auto-attaches the default visible placement so the new plugin
	// surfaces in scene-scoped market lists (including "mine") without a separate
	// publish call — the same auto-placement AdminCreate uses. This is what lets
	// the publish endpoint go away: create is now self-sufficient for visibility.
	m.Placements = []model.PluginPlacement{defaultMarketPlacement(p.CategoryID)}
	sync, err := s.repo.Create(ctx, scope(caller), m)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if sync != nil && sync.NewVersionID != "" {
		p.CurrentVersionID = &sync.NewVersionID
	}
	if sync != nil && sync.Relations != nil {
		rels = sync.Relations
	}
	return &Detail{Plugin: p, Relations: rels, RelationResult: relationResult(sync)}, nil
}

func (s *Service) Update(ctx context.Context, caller Caller, pluginID string, req WriteRequest) (*Detail, error) {
	return s.update(ctx, caller, pluginID, req)
}

// update is the shared content-write path for the tenant Update and the
// skill-import reupload. Every save records a plugin_versions snapshot — a save
// IS a version — so there is no snapshot toggle.
func (s *Service) update(ctx context.Context, caller Caller, pluginID string, req WriteRequest) (*Detail, error) {
	if err := validateCaller(caller); err != nil {
		return nil, err
	}
	storageID, err := parseStorageID(pluginID)
	if err != nil {
		return nil, err
	}
	old, _, err := s.repo.GetWithRelations(ctx, scope(caller), storageID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if old.OwnerUID != caller.UID || (old.SpaceID != nil && *old.SpaceID != caller.SpaceID) {
		return nil, ErrNotFound
	}
	// An embedded child (a bundled skill / squad member) is owned by its container
	// graph and may be content-swapped only through a container reupload — a
	// standalone update must not edit it out of band, matching AdminUpdate.
	if old.IsEmbedded {
		return nil, ErrNotFound
	}
	if req.Type != old.Type {
		return nil, ErrInvalidRequest
	}
	// A LISTED, org-visible plugin may not be modified through this path at all.
	// Every field a tenant can change here is org-visible — the documents, and
	// through the manifest the name/description/labels, plus category, publisher
	// and icon — so an edit that lands here is an unreviewed change to what the
	// whole Space is reading. Reviewed content arrives through SubmitReview.
	//
	// The gate is (published AND space), not published alone. A PRIVATE published
	// plugin has no review channel by design — Publish lists it directly — so
	// refusing edits there would leave it permanently uneditable, and the
	// justification for the refusal does not hold anyway: nobody else can read it.
	// A DRAFT or DELISTED row is freely editable, which is what makes "edit and
	// publish again" work after a takedown.
	//
	// Self-delisting by lowering visibility to private is NO LONGER a way out:
	// taking a listed plugin down is a Space-admin action (Delist). visibility is
	// otherwise free to change on an unlisted row, since it only declares intent.
	//
	// A system admin is exempt from the two tenant gates below. The residual that
	// buys is narrower than it looks, and worth naming exactly: a super-admin
	// cannot LIST anything through this path (listing_state is not settable on the
	// write path, and changing visibility on a published row still drops it to
	// draft below), and AdminUpdate stamps old.Visibility via adminEffectiveWrite
	// rather than taking it from the request. What they CAN do is edit the live
	// content of an ALREADY-LISTED row with no review request and no
	// `review_approve` audit row — only the ordinary `update` audit records it.
	// That is the platform-operator escape hatch, deliberately kept.
	//
	// The version label may go up or stay put, never back. Checked against the
	// STORED label rather than the request's own history, because a client that
	// forgets to send the field would otherwise silently reset it.
	if old.CurrentVersion != nil {
		if strings.TrimSpace(req.Version) != "" && !versionNotRegressed(*old.CurrentVersion, req.Version) {
			return nil, ErrVersionRegressed
		}
		// Lets buildWrite accept an unchanged pre-tightening label; see the field.
		req.grandfatheredVersion = *old.CurrentVersion
	}

	if !caller.IsSystemAdmin {
		if !validVisibility(req.Visibility, false) {
			return nil, ErrInvalidRequest
		}
		if old.ListingState == model.PluginListingStatePublished && old.Visibility == model.PluginVisibilitySpace {
			return nil, ErrListedRequiresReview
		}
		// While a review is pending, the frozen snapshot is what the reviewer will
		// act on, so content edits stay allowed (that is the whole point of freezing
		// it). Changing VISIBILITY is different: ApproveReview stamps
		// visibility=space, so approving a request whose author has since switched to
		// 仅自己可见 would publish a row against the author's last stated intent.
		if req.Visibility != old.Visibility {
			pending, err := s.repo.HasPendingReview(ctx, scope(caller), old.ID)
			if err != nil {
				return nil, mapStoreError(err)
			}
			if pending {
				return nil, ErrReviewPending
			}
		}
	}
	now := s.now()
	// A fetch-edit-save client echoes back the GET package, whose storage
	// attachments no longer carry an inline key (it lives in the host sidecar).
	// Re-inject the stored key for unchanged storage content so the round-trip is
	// not rejected by splitStorageKeys.
	req.Package = reinjectUpdateStorageKeys(req.Package, old.Package, old.AttachmentKeys)
	p, rels, err := s.buildWrite(ctx, caller, storageID, req, now, false)
	if err != nil {
		return nil, err
	}
	p.CreatedAt, p.CurrentVersionID = old.CreatedAt, old.CurrentVersionID
	// A metadata edit that omits `version` must keep the existing current-version
	// label — buildWrite otherwise defaults an omitted version to "1.0.0", which
	// would silently reset a plugin imported as e.g. "2.4.0" on its first save.
	// Mirrors AdminUpdate / container reupload, which also carry old.CurrentVersion.
	if strings.TrimSpace(req.Version) == "" {
		p.CurrentVersion = old.CurrentVersion
	}
	// Creation provenance is immutable; keep the original creator identity.
	p.CreatorName, p.CreatedByType = old.CreatorName, old.CreatedByType
	p.CreatedByBotUID, p.CreatedByBotName = old.CreatedByBotUID, old.CreatedByBotName
	for i := range rels {
		rels[i].SourcePluginID = storageID
	}
	// The saved row keeps the listing state it already had — buildWrite mints the
	// create-time default (draft), which would otherwise be reported back as the
	// plugin's state even though the UPDATE never writes the column.
	p.ListingState = old.ListingState

	audit := s.audit(caller, storageID, "update", old, p, now)
	m := mutation(*p, rels, audit)
	m.SnapshotVersion = true
	m.Changelog = req.Changelog
	// Forward-only was compared against the UNLOCKED `old` read above. Let the repo
	// restate it against the row it locks, for the same reason the two listing
	// decisions below are re-derived: an approval that publishes a frozen label can
	// advance current_version between that read and the write, after which this save
	// would snapshot a label the row has already moved past.
	m.EnforceForwardOnlyVersion = true
	// The unlocked pending guard above (non-admin visibility change) is a fast
	// pre-check; make it authoritative by re-checking under the plugin row lock in
	// the write transaction. Same condition as that guard so a content-only save
	// and the exempt system admin skip the extra read.
	if !caller.IsSystemAdmin && req.Visibility != old.Visibility {
		m.RefusePendingReview = true
	}
	// The listed_requires_review gate and the un-list-on-widen decision above were
	// both computed from the UNLOCKED `old` read. An approval or a publish can
	// commit between that read and Repo.Update's row lock, so let the repo be
	// authoritative: EnforceListingGate makes it re-derive both facts from the row
	// it locks (refuse a locked published+space edit; reset-to-draft on a widen of
	// a locked-published row). The exempt system admin keeps the platform-operator
	// escape hatch and is not gated.
	if !caller.IsSystemAdmin {
		m.EnforceListingGate = true
	}
	// Widening the declared audience of a LISTED plugin un-lists it.
	//
	// The refusal above only covers a published plugin that is already `space`.
	// A published PRIVATE one is editable by design — nobody else can read it —
	// but writing `space` onto it while it stays published would list it to the
	// whole organization with no review, which is the single thing this workflow
	// exists to prevent. Dropping to draft costs the author nothing (the plugin
	// was visible only to them) and puts it back on the normal 发布 path, where
	// the new visibility routes it through review.
	//
	// Any content-only edit leaves the listing alone. The condition is "the
	// visibility changed", not "it widened": for a tenant the only change that can
	// reach here IS the widening one (published+space is refused above with
	// ErrListedRequiresReview), and for the exempt system admin dropping a listed
	// row back to draft on a narrowing is the safe direction anyway.
	if old.ListingState == model.PluginListingStatePublished && req.Visibility != old.Visibility {
		m.ResetListingToDraft = true
		p.ListingState = model.PluginListingStateDraft
	}
	sync, err := s.repo.Update(ctx, scope(caller), m)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if sync != nil && sync.NewVersionID != "" {
		p.CurrentVersionID = &sync.NewVersionID
	}
	if sync != nil && sync.Relations != nil {
		rels = sync.Relations
	}
	return &Detail{Plugin: p, Relations: rels, RelationResult: relationResult(sync)}, nil
}

func relationResult(sync *pluginrepo.RelationSync) *RelationResult {
	if sync == nil {
		return &RelationResult{Created: []string{}, Updated: []string{}, Deleted: []string{}}
	}
	out := &RelationResult{Created: sync.Created, Updated: sync.Updated, Deleted: sync.Deleted}
	if out.Created == nil {
		out.Created = []string{}
	}
	if out.Updated == nil {
		out.Updated = []string{}
	}
	if out.Deleted == nil {
		out.Deleted = []string{}
	}
	return out
}

func (s *Service) Delete(ctx context.Context, caller Caller, pluginID string) error {
	if validateCaller(caller) != nil {
		return ErrInvalidRequest
	}
	storageID, err := parseStorageID(pluginID)
	if err != nil {
		return err
	}
	old, _, err := s.repo.GetWithRelations(ctx, scope(caller), storageID)
	if err != nil {
		return mapStoreError(err)
	}
	if old.OwnerUID != caller.UID || (old.SpaceID != nil && *old.SpaceID != caller.SpaceID) {
		return ErrNotFound
	}
	// A LISTED, org-visible plugin may not be deleted through this path. Delist
	// enforces the listing.go:141-148 invariant: "Space admins only… the author
	// deliberately cannot do this — self-delisting through the write path was
	// removed — so that a plugin the org depends on cannot vanish at its author's
	// discretion." Delete is the same write path and would let the author bypass
	// that takedown gate entirely, irreversibly (there is no undelete, and a
	// manual deleted_at=NULL drops the row back to draft per the listing-state
	// backfill migration). An admin must Delist first; once the row leaves
	// `published` (draft, delisted, or a private-published row nobody else can
	// read), the author can delete it again. Symmetric with the ErrListedRequiresReview
	// gate in Service.update, and checked BEFORE the expert/expert_team type switch
	// so DeleteGraph (the more exposed shape — graph roots almost never carry
	// incoming live relations) is covered by the same gate.
	//
	// A system admin keeps the platform-operator escape hatch, matching update.
	if !caller.IsSystemAdmin {
		if old.ListingState == model.PluginListingStatePublished && old.Visibility == model.PluginVisibilitySpace {
			return ErrListedRequiresReview
		}
	}
	audit := s.audit(caller, storageID, "delete", old, nil, s.now())
	// An expert/expert_team top owns embedded children (an expert's bundled skills;
	// a squad's member experts and their skills) — the population backfilled tenant
	// containers actually carry. Tearing it down through DeleteGraph removes the
	// whole subtree in one transaction so those rows are never orphaned (live,
	// is_embedded=1, unreachable). DeleteGraph derives the embedded child set under
	// the top's lock (never a pre-parse snapshot) so a concurrent reupload cannot
	// orphan a child; a standalone catalog skill merely referenced by the top
	// (is_embedded=0) is not collected and survives; it re-checks tenant ownership
	// on the top and each child under the same scope. Connectors and skills carry no
	// embedded children and take the single-row Delete.
	if old.Type == model.PluginTypeExpert || old.Type == model.PluginTypeExpertTeam {
		return mapStoreError(s.repo.DeleteGraph(ctx, scope(caller), storageID, audit.OperatorID, audit.OperatorName, audit.RequestID, audit.Remark))
	}
	return mapStoreError(s.repo.Delete(ctx, scope(caller), storageID, audit.OperatorID, audit.OperatorName, audit.RequestID, audit.Remark))
}

func (s *Service) ListVersions(ctx context.Context, caller Caller, pluginID string, limit, offset int) ([]model.PluginVersion, int64, error) {
	if validateCaller(caller) != nil || limit < 0 || limit > maxListLimit || offset < 0 {
		return nil, 0, ErrInvalidRequest
	}
	storageID, err := parseStorageID(pluginID)
	if err != nil {
		return nil, 0, err
	}
	p, _, err := s.repo.GetWithRelations(ctx, scope(caller), storageID)
	if err != nil {
		return nil, 0, mapStoreError(err)
	}
	items, total, err := s.repo.ListVersions(ctx, scope(caller), storageID, limit, offset)
	for i := range items {
		items[i].PluginType = p.Type
	}
	return items, total, mapStoreError(err)
}

func (s *Service) buildWrite(ctx context.Context, c Caller, pluginID string, req WriteRequest, now time.Time, admin bool) (*model.Plugin, []model.PluginRelation, error) {
	name := strings.TrimSpace(req.Name)
	if !validName(name) || !validPluginType(req.Type) || !validVisibility(req.Visibility, c.IsSystemAdmin) {
		return nil, nil, ErrInvalidRequest
	}
	docs, err := CanonicalizeDocuments(name, req.Type, req.Tags, req.Manifest, req.Package, c.SpaceID)
	if err != nil {
		return nil, nil, err
	}
	icon := strings.TrimSpace(req.Icon)
	if !validIcon(icon) {
		return nil, nil, ErrInvalidRequest
	}
	// current_version is caller-declared: use the submitted version, defaulting to
	// "1.0.0" when none is passed. Reject a malformed non-empty label — unless it
	// is byte-for-byte the label already stored, which is how an edit to a plugin
	// carrying a pre-tightening label stays possible.
	currentVersion := strings.TrimSpace(req.Version)
	if currentVersion == "" {
		currentVersion = defaultCurrentVersion
	} else if !validVersion(currentVersion) && currentVersion != strings.TrimSpace(req.grandfatheredVersion) {
		return nil, nil, ErrInvalidRequest
	}
	toolCount := 0
	if req.Type == model.PluginTypeConnector {
		toolCount = ConnectorToolCount(docs.Package)
	}
	spaceID := c.SpaceID
	createdBy, botUID, botName := provenance(c)
	p := &model.Plugin{ID: pluginID, Name: name, Type: req.Type, CategoryID: trimOptional(req.CategoryID), Tags: docs.Tags, Publisher: strings.TrimSpace(req.Publisher), OwnerUID: c.UID, SpaceID: &spaceID, Visibility: req.Visibility, ListingState: model.PluginListingStateDraft, CreatorName: c.Name, CreatedByType: createdBy, CreatedByBotUID: botUID, CreatedByBotName: botName, Icon: icon, IconURL: s.resolveIcon(ctx, icon), ToolCount: toolCount, Manifest: docs.Manifest, Package: docs.Package, AttachmentKeys: docs.AttachmentKeys, ManifestHash: docs.ManifestHash, PluginHash: docs.PluginHash, CurrentVersion: &currentVersion, Status: 1, CreatedAt: now, UpdatedAt: now}
	rels, err := s.buildRelations(ctx, c, admin, p, req.Relations, now)
	if err != nil {
		return nil, nil, err
	}
	return p, rels, nil
}

func (s *Service) buildRelations(ctx context.Context, c Caller, admin bool, source *model.Plugin, in []RelationRequest, now time.Time) ([]model.PluginRelation, error) {
	if len(in) > maxRelations {
		return nil, ErrInvalidRequest
	}
	out := make([]model.PluginRelation, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, r := range in {
		relationID := strings.TrimSpace(r.ID)
		// A relation ID can only address an existing edge, so it is meaningless
		// while the Plugin itself is being created (source.ID is still empty).
		if relationID != "" && (source.ID == "" || !validRelationID(relationID)) {
			return nil, ErrInvalidRequest
		}
		if submittedSource := strings.TrimSpace(r.SourcePluginID); submittedSource != "" {
			sourceID, err := parseStorageID(submittedSource)
			if err != nil || source.ID == "" || sourceID != source.ID {
				return nil, ErrInvalidRequest
			}
		}
		targetID, err := parseStorageID(r.TargetPluginID)
		typ := strings.TrimSpace(r.Type)
		if err != nil || targetID == source.ID || !validRelationSource(typ, source.Type) {
			return nil, ErrInvalidRequest
		}
		key := typ + "\x00" + targetID
		if _, ok := seen[key]; ok {
			return nil, ErrInvalidRequest
		}
		seen[key] = struct{}{}
		// On the admin write path the target must resolve cross-Space, matching
		// the repo layer's admin-aware lockRelationTargets; the tenant scope would
		// hide a space-scoped target and either 404 the edit or drop every edge.
		target, _, err := s.repo.GetWithRelations(ctx, writeScope(c, admin), targetID)
		if err != nil {
			return nil, mapStoreError(err)
		}
		if !validRelationType(typ, source.Type, target.Type) {
			return nil, ErrInvalidRequest
		}
		data, err := normalizeOptionalObject(r.Data)
		if err != nil {
			return nil, err
		}
		out = append(out, model.PluginRelation{ID: relationID, SourcePluginID: source.ID, SourcePluginType: source.Type, TargetPluginID: targetID, TargetPluginType: target.Type, Type: typ, SortOrder: r.SortOrder, Data: data, Status: 1, CreatedBy: c.UID, CreatedAt: now, UpdatedAt: now})
	}
	return out, nil
}

func (s *Service) audit(c Caller, pluginID, action string, before, after *model.Plugin, now time.Time) model.PluginAuditLog {
	a := model.PluginAuditLog{ID: s.id(), PluginID: pluginID, Action: action, OperatorID: c.UID, OperatorName: c.Name, RequestID: c.RequestID, CreatedAt: now}
	if before != nil {
		a.BeforeHash = stringPtr(before.PluginHash)
		a.ManifestSnapshot = cloneJSON(before.Manifest)
		a.PluginSnapshot = cloneJSON(before.Package)
	}
	if after != nil {
		a.AfterHash = stringPtr(after.PluginHash)
		a.ManifestSnapshot = cloneJSON(after.Manifest)
		a.PluginSnapshot = cloneJSON(after.Package)
	}
	return a
}

func provenance(c Caller) (string, *string, *string) {
	if c.BotUID != "" {
		return "bot", stringPtr(c.BotUID), stringPtr(c.BotName)
	}
	return "human", nil, nil
}

func mutation(p model.Plugin, relations []model.PluginRelation, audit model.PluginAuditLog) pluginrepo.Mutation {
	return pluginrepo.Mutation{
		Plugin:       p,
		Relations:    relations,
		OperatorID:   audit.OperatorID,
		OperatorName: audit.OperatorName,
		RequestID:    audit.RequestID,
		Remark:       audit.Remark,
	}
}

func mapStoreError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pluginrepo.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, pluginrepo.ErrConflict):
		return ErrConflict
	case errors.Is(err, pluginrepo.ErrDeadlock):
		return ErrDeadlock
	case errors.Is(err, pluginrepo.ErrReviewPending):
		return ErrReviewPending
	case errors.Is(err, pluginrepo.ErrListedRequiresReview):
		return ErrListedRequiresReview
	case errors.Is(err, pluginrepo.ErrVersionRegressed):
		return ErrVersionRegressed
	case errors.Is(err, pluginrepo.ErrLabelTaken):
		return ErrLabelTaken
	case errors.Is(err, pluginrepo.ErrReviewContentRequired):
		return ErrReviewContentRequired
	case errors.Is(err, pluginrepo.ErrInvalidRelation), errors.Is(err, pluginrepo.ErrInvalidCategory), errors.Is(err, pluginrepo.ErrInvalidPlacement):
		return ErrInvalidRequest
	default:
		return err
	}
}

func validateCaller(c Caller) error {
	if strings.TrimSpace(c.UID) == "" || strings.TrimSpace(c.SpaceID) == "" {
		return ErrInvalidRequest
	}
	return nil
}
func validateReadPage(c Caller, id string, limit, offset int) error {
	if validateCaller(c) != nil || strings.TrimSpace(id) == "" || limit < 0 || limit > maxListLimit || offset < 0 {
		return ErrInvalidRequest
	}
	return nil
}
func cloneJSON(v json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), v...) }
func stringPtr(v string) *string                  { return &v }
func trimOptional(v *string) *string {
	if v == nil {
		return nil
	}
	x := strings.TrimSpace(*v)
	if x == "" {
		return nil
	}
	return &x
}
