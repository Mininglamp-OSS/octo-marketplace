// Plugin install maps a unified expert / expert_team Plugin onto the Loop
// provisioning flow owned by the expert service: attachments become the agent
// spec, expert_skill relations become packaged skill references, and
// expert_team_expert relations become squad members. Fleet authorizes every
// call as the end user (forwarded token), so this layer does not re-check
// workspace membership or runtime access.

package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
	"go.uber.org/zap"
)

// Provisioner is the Loop provisioning surface, implemented by
// *expertsvc.Service; it owns timeouts, file budgets, and rollback.
type Provisioner interface {
	ProvisionAgentFromSpec(context.Context, expertsvc.InstallInput, expertsvc.ProvisionAgentSpec) (string, error)
	ProvisionSquadFromSpec(context.Context, expertsvc.InstallInput, *model.Squad) (expertsvc.InstallSquadResult, error)
}

// InstallTracker mirrors the expert service's metrics seam so install and
// download counts accrue to resource_type "plugin" without importing the
// metrics package.
type InstallTracker interface {
	TrackInstall(ctx context.Context, resourceType, resourceID string) error
	TrackDownload(ctx context.Context, resourceType, resourceID string) error
}

// WithProvisioner wires the Loop provisioner (chainable at construction).
func (s *Service) WithProvisioner(p Provisioner) *Service {
	s.provisioner = p
	return s
}

// WithMetrics wires the install counter; nil keeps tracking a no-op.
func (s *Service) WithMetrics(m InstallTracker) *Service {
	s.metrics = m
	return s
}

type InstallParams struct {
	WorkspaceID string
	RuntimeID   string
	// Token is the caller's forwarded credential, re-read from the request
	// header by the handler because middleware discards it.
	Token string
}

// InstallOutcome carries exactly one of the created Loop resource IDs,
// matching the Plugin's type.
type InstallOutcome struct {
	AgentID string
	SquadID string
}

func (s *Service) Install(ctx context.Context, caller Caller, pluginID string, p InstallParams) (*InstallOutcome, error) {
	if validateCaller(caller) != nil {
		return nil, ErrInvalidRequest
	}
	if s.provisioner == nil {
		return nil, expertsvc.ErrFleetNotConfigured
	}
	if strings.TrimSpace(p.WorkspaceID) == "" || strings.TrimSpace(p.RuntimeID) == "" {
		return nil, ErrInvalidRequest
	}
	detail, err := s.resolveInstallDetail(ctx, caller, pluginID)
	if err != nil {
		return nil, err
	}
	// An embedded child (a bundled skill, a squad member expert) is installed only
	// as part of its container graph, never on its own — every other write verb
	// refuses it the same way (service.update, container reupload, admin, review
	// submit, delist). Install was the one path with no such guard, so after a
	// container was delisted a colleague could still install the child's full
	// content directly by its id. ErrNotFound keeps the answer identical to a read
	// the caller could not make.
	if detail.Plugin.IsEmbedded {
		return nil, ErrNotFound
	}
	in := expertsvc.InstallInput{WorkspaceID: strings.TrimSpace(p.WorkspaceID), RuntimeID: strings.TrimSpace(p.RuntimeID), SpaceID: caller.SpaceID, Token: p.Token}
	// One aggregate byte budget for the whole install, threaded through every
	// skill of every member, so an expert_team fanning out to many members ×
	// skills cannot multiply the resident-memory bound (A4/P1-3). Mirrors the
	// download path's maxArchiveBytes ceiling.
	budget := newInstallBudget(s.maxArchiveBytes)
	switch detail.Plugin.Type {
	case model.PluginTypeExpert:
		spec, err := s.agentSpecFromPlugin(ctx, caller, detail, budget)
		if err != nil {
			return nil, err
		}
		agentID, err := s.provisioner.ProvisionAgentFromSpec(ctx, in, *spec)
		if err != nil {
			return nil, err
		}
		s.trackInstall(ctx, detail.Plugin.ID)
		return &InstallOutcome{AgentID: agentID}, nil
	case model.PluginTypeExpertTeam:
		squad, err := s.squadFromPlugin(ctx, caller, detail, budget)
		if err != nil {
			return nil, err
		}
		result, err := s.provisioner.ProvisionSquadFromSpec(ctx, in, squad)
		if err != nil {
			return nil, err
		}
		s.trackInstall(ctx, detail.Plugin.ID)
		return &InstallOutcome{SquadID: result.SquadID}, nil
	default:
		return nil, ErrInvalidRequest
	}
}

// resolveInstallDetail loads a plugin's detail (with relations) for install and
// refuses LOUDLY when the caller cannot see every declared relation target. The
// tenant read filters relation targets by caller visibility (GetWithRelations),
// so a shared parent that legitimately depends on a private child would resolve
// to a short relation list — and the install fan-out would then provision an
// expert with missing skills or a squad missing members/leader while returning
// 200 (P1-1). Comparing the declared count (CountDeclaredRelations, unfiltered)
// against the visible list detects the drop; only the count is used, so nothing
// beyond the number of edges the publisher already declared is revealed. This is
// the visibility-driven sibling of the size-driven loud truncation elsewhere in
// this file. Resolving under the owner's visibility instead is deliberately NOT
// done: that would copy a private dependency's content into any caller's agent,
// a confidentiality decision the owner must take explicitly.
func (s *Service) resolveInstallDetail(ctx context.Context, caller Caller, pluginID string) (*Detail, error) {
	detail, err := s.Detail(ctx, caller, pluginID, true)
	if err != nil {
		return nil, err
	}
	declared, err := s.repo.CountDeclaredRelations(ctx, detail.Plugin.ID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if declared > len(detail.Relations) {
		return nil, ErrDependencyHidden
	}
	return detail, nil
}

// agentSpecFromPlugin materializes one expert Plugin into a provisioning spec:
// the AGENTS.md instruction entry and root mcp.json from its package
// attachments, packaged skills from its expert_skill relation targets.
func (s *Service) agentSpecFromPlugin(ctx context.Context, caller Caller, detail *Detail, budget *installBudget) (*expertsvc.ProvisionAgentSpec, error) {
	p := detail.Plugin
	instruction, _ := rawAttachmentContent(p.Package, "AGENTS.md")
	mcpConfig, _ := rawAttachmentContent(p.Package, "mcp.json")
	// Charge the agent's own inline documents against the shared budget so the
	// invariant "aggregate resident bytes are bounded across the fan-out" holds
	// for members too, not just their skills (P1-3).
	budget.bytes -= int64(len(instruction) + len(mcpConfig))
	skills, err := s.skillRefsFromRelations(ctx, caller, detail.Relations, budget)
	if err != nil {
		return nil, err
	}
	return &expertsvc.ProvisionAgentSpec{
		Name:        p.Name,
		Summary:     manifestDescription(p.Manifest),
		Instruction: instruction,
		MCPConfig:   mcpConfig,
		Skills:      skills,
	}, nil
}

// squadFromPlugin materializes one expert_team Plugin into the squad model the
// expert provisioner consumes: leader and strategies from team/config.json,
// members from expert_team_expert relations (relation_json carries role,
// is_leader, and member_key), each member's spec from its own Plugin.
func (s *Service) squadFromPlugin(ctx context.Context, caller Caller, detail *Detail, budget *installBudget) (*model.Squad, error) {
	p := detail.Plugin
	// Contract layout: the team package is a single AGENTS.md document that
	// carries the collaboration/dispatch prose; it becomes the Loop squad's
	// instructions verbatim. Leadership comes from member relations
	// (relation_json is_leader), not from a config attachment.
	instructions, _ := rawAttachmentContent(p.Package, "AGENTS.md")
	memberRelations := relationsOfType(detail.Relations, "expert_team_expert")
	members := make([]model.SquadMember, 0, len(memberRelations))
	for _, rel := range memberRelations {
		// Loud structural truncation (see skillRefsFromRelations): refuse rather
		// than install a squad missing members once the install-wide target OR
		// byte budget is spent — a silently leaderless/partial team is worse than
		// an error the caller can act on. The byte gate sits here, before
		// s.Detail, so a team whose members carry large instructions but no skills
		// (the skill loop that used to be the only byte gate never runs for them)
		// still stops the fan-out instead of charging budget.bytes into deep
		// negative territory and retaining every member's document (P1-2).
		if budget.targets <= 0 || budget.bytes <= 0 {
			return nil, ErrTooLarge
		}
		budget.targets--
		memberDetail, err := s.resolveInstallDetail(ctx, caller, rel.TargetPluginID)
		if err != nil {
			return nil, err
		}
		member, err := s.squadMemberFromPlugin(ctx, caller, rel, memberDetail, budget)
		if err != nil {
			return nil, err
		}
		members = append(members, *member)
	}
	return &model.Squad{
		ID:           p.ID,
		Name:         p.Name,
		Summary:      manifestDescription(p.Manifest),
		Instructions: instructions,
		Members:      members,
	}, nil
}

func (s *Service) squadMemberFromPlugin(ctx context.Context, caller Caller, rel model.PluginRelation, memberDetail *Detail, budget *installBudget) (*model.SquadMember, error) {
	mp := memberDetail.Plugin
	// relation_json is authoritative for squad wiring; expert/context.json is
	// the snapshot fallback carried inside the member package.
	var wiring struct {
		MemberKey string `json:"member_key"`
		Role      string `json:"role"`
		IsLeader  bool   `json:"is_leader"`
	}
	if len(rel.Data) > 0 {
		_ = json.Unmarshal(rel.Data, &wiring)
	}
	if wiring.MemberKey == "" && wiring.Role == "" {
		if raw, ok := rawAttachmentContent(mp.Package, "expert/context.json"); ok {
			_ = json.Unmarshal([]byte(raw), &wiring)
		}
	}
	instruction, _ := rawAttachmentContent(mp.Package, "AGENTS.md")
	mcpConfig, _ := rawAttachmentContent(mp.Package, "mcp.json")
	// Charge the member's inline documents against the shared budget (P1-3), so a
	// large team's member instructions count toward the aggregate memory bound.
	budget.bytes -= int64(len(instruction) + len(mcpConfig))
	skills, err := s.skillRefsFromRelations(ctx, caller, memberDetail.Relations, budget)
	if err != nil {
		return nil, err
	}
	return &model.SquadMember{
		MemberKey:   wiring.MemberKey,
		Name:        mp.Name,
		Role:        wiring.Role,
		IsLeader:    wiring.IsLeader,
		Instruction: instruction,
		MCPConfig:   mcpConfig,
		Skills:      skills,
	}, nil
}

// skillRefsFromRelations loads every expert_skill relation target and resolves
// it into a SkillRef the provisioner installs. Tree-shaped skills resolve their
// SKILL.md and supporting text files inline (fetching any storage-backed text
// through this service's object store); legacy pointer skills keep their
// object/zip keys for the provisioner to fetch.
func (s *Service) skillRefsFromRelations(ctx context.Context, caller Caller, relations []model.PluginRelation, budget *installBudget) ([]model.SkillRef, error) {
	skillRelations := relationsOfType(relations, "expert_skill")
	refs := make([]model.SkillRef, 0, len(skillRelations))
	for _, rel := range skillRelations {
		// Structural truncation must be loud: if the install-wide target or byte
		// budget is exhausted, a skill would be silently dropped — fail with the
		// too-large error instead so the caller sees a partial topology was
		// refused, not installed. skillRefFromPlugin is loud too: a SKILL.md that
		// does not fit the remaining budget errors rather than yielding a
		// document-less ref the provisioner drops (P1-2). (Supporting-file
		// degradation inside it stays silent — pre-existing and bounded.)
		if budget.targets <= 0 || budget.bytes <= 0 {
			return nil, ErrTooLarge
		}
		budget.targets--
		target, err := s.Detail(ctx, caller, rel.TargetPluginID, false)
		if err != nil {
			return nil, err
		}
		ref, err := s.skillRefFromPlugin(ctx, target.Plugin, budget)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// skillRefFromPlugin resolves a skill Plugin into a SkillRef. A tree-shaped
// package (no skill/ref.json pointer) resolves its SKILL.md and supporting text
// files inline; a legacy package keeps its object/zip pointers for the
// provisioner. Storage-backed supporting files are fetched here and included
// only when they are UTF-8 text (binaries are skipped, mirroring the fleet
// text-only skill-file store).
func (s *Service) skillRefFromPlugin(ctx context.Context, p *model.Plugin, budget *installBudget) (model.SkillRef, error) {
	ref := model.SkillRef{Name: p.Name}
	var legacy struct {
		FileName     string   `json:"file_name"`
		FileSize     int64    `json:"file_size"`
		Files        []string `json:"files"`
		ObjectKey    string   `json:"object_key"`
		ZipObjectKey string   `json:"zip_object_key"`
	}
	hasRef := false
	if raw, ok := rawAttachmentContent(p.Package, "skill/ref.json"); ok && json.Unmarshal([]byte(raw), &legacy) == nil {
		// skill/ref.json is caller-writable, so its object keys are honored only
		// when they sit in the plugin's own managed Space prefix; a forged
		// cross-Space or arbitrary bucket key is dropped rather than fetched by
		// the provisioner. Legacy backfilled skills resolve once expand-skills has
		// rewritten them into own-Space trees. hasRef tracks whether a key was
		// actually TRUSTED (not merely parsed): an untrusted/forged pointer must not
		// short-circuit tree resolution with an empty, unusable ref (B7).
		if trustedArtifactKey(legacy.ObjectKey, p.SpaceID) {
			ref.ObjectKey = legacy.ObjectKey
			hasRef = true
		}
		if trustedArtifactKey(legacy.ZipObjectKey, p.SpaceID) {
			ref.ZipObjectKey = legacy.ZipObjectKey
			hasRef = true
		}
		if hasRef {
			// FileName/FileSize/Files describe the trusted object; carry them only
			// when a key was trusted. FileSize is advisory — the provisioner stats
			// the actual bytes (expert service is authoritative).
			ref.FileName = legacy.FileName
			ref.FileSize = legacy.FileSize
			ref.Files = legacy.Files
		}
	}
	if key, ok := storageAttachmentKey(p, "skill/package.zip"); ok {
		// Same own-Space scoping as skill/ref.json above (Q5): the managed zip key
		// is honored only inside this plugin's own prefix, matching legacyZipKey /
		// migrationZipKey, so a forged or cross-Space key is never handed to the
		// provisioner to fetch.
		if ref.ZipObjectKey == "" && p.SpaceID != nil && validReferencedObjectKey(key, *p.SpaceID) {
			ref.ZipObjectKey = key
			hasRef = true
		}
	}
	// Legacy pointer shape: leave object/zip keys for the provisioner to fetch.
	if hasRef {
		return ref, nil
	}
	// Tree shape: resolve SKILL.md and supporting text files from the attachments.
	// SKILL.md is the skill's required document; a tree-shaped ref with neither a
	// pointer key nor Markdown is silently dropped downstream
	// (expert/install.go), so truncating it here must be LOUD, matching the
	// sibling fan-out gates (P1-2). The caller's budget.bytes<=0 gate covers full
	// exhaustion; this covers the 0<budget.bytes<len(md) window, where the doc
	// exists but does not fit — fail the install rather than provision a skill
	// missing its instructions.
	md, hasMD := rawAttachmentContent(p.Package, "SKILL.md")
	if hasMD {
		if int64(len(md)) > budget.bytes {
			return model.SkillRef{}, ErrTooLarge
		}
		ref.Markdown = md
		budget.bytes -= int64(len(md))
	}
	// Cap the supporting files materialized here BEFORE fetching, mirroring the
	// downstream per-skill fan-out budget (expert.maxSkillFilesPerSkill). Counting
	// every processed attachment — not just accepted text — bounds both the
	// GetObject fan-out and the in-memory SupportingFiles slice for a plugin whose
	// plugin_json packs thousands of storage attachments. The shared budget.bytes
	// byte budget additionally caps aggregate resident bytes across the install.
	processed := 0
	keys := attachmentKeyMap(p.AttachmentKeys)
	for _, a := range decodePackageAttachments(p.Package) {
		if a.Path == "SKILL.md" {
			continue
		}
		if processed >= maxInstallSupportingFiles || budget.bytes <= 0 {
			break
		}
		processed++
		if a.ContentType == "raw" {
			if utf8.ValidString(a.RawContent) && int64(len(a.RawContent)) <= budget.bytes {
				ref.SupportingFiles = append(ref.SupportingFiles, model.SkillFile{Path: a.Path, Content: a.RawContent})
				budget.bytes -= int64(len(a.RawContent))
			}
			continue
		}
		// Bound the fetch to the remaining budget so an over-budget or binary
		// object is not pulled in full only to be discarded (P1-3); readStorageText
		// reads at most limit+1 and reports too-large rather than truncating (B2),
		// and verifies the recorded content_hash/content_size before use.
		storageKey := keys[a.Path]
		if storageKey == "" {
			storageKey = a.StorageURI
		}
		if content, ok := s.readStorageText(ctx, p.SpaceID, storageKey, budget.bytes, a.ContentSize, a.ContentHash); ok {
			ref.SupportingFiles = append(ref.SupportingFiles, model.SkillFile{Path: a.Path, Content: content})
			budget.bytes -= int64(len(content))
		}
	}
	return ref, nil
}

// maxInstallSupportingFiles bounds how many non-SKILL.md attachments one skill
// contributes to an install, applied before any storage fetch. It mirrors the
// provisioner's per-skill file budget so this pre-fetch cap can never admit more
// than the downstream stage would accept anyway.
const maxInstallSupportingFiles = 50

// maxInstallRelationTargets caps how many relation targets (skills, squad
// members) one install resolves in total. maxRelations (200) is per-plugin, so
// without an install-wide cap an expert_team could fan out to 200 members ×
// 200 skills = 40k Detail round-trips and SkillRefs. This bounds both the query
// count and the resident memory across the whole fan-out.
const maxInstallRelationTargets = 500

// installBudget is the shared, mutable budget threaded through the relation
// fan-out of one install: an aggregate reconstructed-byte ceiling and a total
// resolved-target ceiling. Both are decremented as targets/documents are
// materialized and stop the fan-out once exhausted.
type installBudget struct {
	bytes   int64
	targets int
}

func newInstallBudget(bytes int64) *installBudget {
	return &installBudget{bytes: bytes, targets: maxInstallRelationTargets}
}

// readStorageText fetches a storage attachment from this plugin's managed prefix
// and returns it only when it is valid UTF-8 text within the caller's remaining
// byte budget. It reads at most min(budget, maxAttachmentBytes)+1 bytes and
// reports too-large (ok=false) rather than silently truncating the object into a
// partial file (B2); binary attachments are skipped, matching the text-only
// downstream skill-file store. When the attachment records a content_hash/
// content_size, the fetched bytes are verified against them (B2) so a corrupted
// or replaced object is not installed under an immutable version.
func (s *Service) readStorageText(ctx context.Context, spaceID *string, key string, remaining, wantSize int64, wantHash string) (string, bool) {
	if s.storage == nil || spaceID == nil || remaining <= 0 || !validReferencedObjectKey(key, *spaceID) {
		return "", false
	}
	limit := remaining
	if s.maxAttachmentBytes < limit {
		limit = s.maxAttachmentBytes
	}
	body, err := s.storage.GetObject(ctx, key)
	if err != nil {
		return "", false
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil || int64(len(data)) > limit || !utf8.Valid(data) {
		return "", false
	}
	if wantSize > 0 && int64(len(data)) != wantSize {
		return "", false
	}
	if wantHash != "" {
		sum := sha256.Sum256(data)
		if wantHash != "sha256:"+hex.EncodeToString(sum[:]) {
			return "", false
		}
	}
	return string(data), true
}

func relationsOfType(relations []model.PluginRelation, relationType string) []model.PluginRelation {
	out := make([]model.PluginRelation, 0, len(relations))
	for _, rel := range relations {
		if rel.Type == relationType && rel.Status == 1 && rel.DeletedAt == nil {
			out = append(out, rel)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SortOrder < out[j].SortOrder })
	return out
}

func manifestDescription(manifest json.RawMessage) string {
	var doc struct {
		Description string `json:"description"`
	}
	_ = json.Unmarshal(manifest, &doc)
	return doc.Description
}

// manifestName reads the display name (`name`) out of a stored manifest. Used
// to preserve an existing skill's curated display name on a package-only
// reupload rather than resetting it to the freshly-parsed package's machine
// name.
func manifestName(manifest json.RawMessage) string {
	var doc struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(manifest, &doc)
	return doc.Name
}

// trackTimeout bounds the best-effort install counter bump; see the expert
// service for the rationale (a stalled Redis must not delay the response).
const trackTimeout = 2 * time.Second

// trackInstall bumps the plugin install counter after a successful provision.
// Best-effort and detached from the request context; failures are logged.
func (s *Service) trackInstall(ctx context.Context, pluginID string) {
	if s.metrics == nil {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), trackTimeout)
	defer cancel()
	if err := s.metrics.TrackInstall(cctx, "plugin", pluginID); err != nil {
		logging.Warn("install_metric_track_failed",
			zap.String("operation", "plugin.install.track"),
			zap.String("resource_id", pluginID),
			logging.ErrorField(err),
		)
	}
}
