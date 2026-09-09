package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"errors"
	"fmt"
	libplugin "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

const (
	maxJSONBytes = 1 << 20 // current-state and snapshot JSON are metadata, not artifacts
	maxNameBytes = 160
	maxTags      = 100
	maxTagBytes  = 128
	maxRelations = 200
	maxListLimit = 100
	maxListTags  = 20 // bound on AND-combined tag filters per list query
	maxIconBytes = 512
)

// defaultCurrentVersion is the current-version label stamped on a write that
// declares no version of its own (e.g. connectors, which have no version field).
const defaultCurrentVersion = "1.0.0"

var (
	// A version label is three numeric parts and nothing else: 1.0.1. The old
	// pattern accepted any identifier-ish string, which is how labels like
	// "v999", "1.0.0lll" and "oooo1.0.0" reached production — none of them can be
	// ordered against another, so "the version may only go up" was unanswerable.
	//
	// Tightening this alone would strand those rows: every save re-sends the
	// stored label, so an existing plugin would become permanently uneditable.
	// versionNotRegressed exempts an UNCHANGED label for exactly that reason.
	versionPattern    = regexp.MustCompile(`^\d{1,9}\.\d{1,9}\.\d{1,9}$`)
	relationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// iconKeyPattern identifies storage-object-key shaped icons that the read
	// path resolves to presigned URLs (legacy skill icons are object keys).
	iconKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	// iconSchemePattern matches any URI scheme prefix; only http(s) is allowed.
	iconSchemePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
)

// validIcon accepts an empty icon, an absolute http(s) URL, a storage object
// key, or a short text glyph (legacy MCP icons are emoji). Every other URI
// scheme — javascript:, data:, file: — plus control characters and traversal
// segments fail closed because icons are echoed into <img src>.
func validIcon(v string) bool {
	if v == "" {
		return true
	}
	if len(v) > maxIconBytes || !utf8.ValidString(v) {
		return false
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		u, err := url.Parse(v)
		return err == nil && u.Host != ""
	}
	if iconSchemePattern.MatchString(v) {
		return false
	}
	return !strings.Contains(v, "..") && !strings.Contains(v, "//")
}

func validPluginType(v model.PluginType) bool {
	switch v {
	case model.PluginTypeExpert, model.PluginTypeExpertTeam, model.PluginTypeSkill, model.PluginTypeConnector:
		return true
	default:
		return false
	}
}

func validVisibility(v model.PluginVisibility, systemAdmin bool) bool {
	switch v {
	case model.PluginVisibilitySpace, model.PluginVisibilityPrivate:
		return true
	case model.PluginVisibilitySystem:
		// system is marketplace-wide: readable, listable and installable from every
		// Space (see visibilitySQL). Only the admin surface (which sets
		// IsSystemAdmin) may mint it; a tenant caller on /plugins/upsert cannot
		// self-publish globally. Mirrors the legacy skill rule and the import path,
		// which already forces uploads non-public.
		return systemAdmin
	default:
		// `public` is retired on the WRITE path after unification: a fresh public
		// row would vanish from the admin visibility=system filter, so no caller
		// (tenant or systemAdmin) may mint one — the write enum is exactly
		// system/space/private. The constant is kept only to READ legacy public
		// rows; admin edits normalize a preserved public visibility to `system`
		// (see adminEffectiveWrite).
		return false
	}
}

// tenantVisibilityAllowed was removed with the introduction of listing_state.
// It encoded "a tenant may keep its visibility or lower it to private, never
// raise it", which was the gate that kept a single upsert from bypassing review
// while `private` doubled as the draft state. visibility is now a declared intent
// that lists nothing on its own, so raising it on an unlisted row is meaningless
// and lowering it is no longer a way to self-delist. The gate that replaced it is
// listing_state: ErrListedRequiresReview refuses edits to a published org-visible
// row, and only ApproveReview and Publish can set published.

func validName(v string) bool {
	return v != "" && utf8.ValidString(v) && len(v) <= maxNameBytes && !strings.ContainsRune(v, '\x00')
}

func validVersion(v string) bool { return versionPattern.MatchString(strings.TrimSpace(v)) }

// versionNotRegressed reports whether `next` may replace `current`.
//
// The ordering rule itself lives in model.VersionNotRegressed so the unlocked
// service pre-check here and the authoritative locked re-check inside
// repository.InsertReviewRequest share one implementation and cannot drift. This
// stays a package-local alias so existing call sites read naturally.
func versionNotRegressed(current, next string) bool {
	return model.VersionNotRegressed(current, next)
}
func validRelationID(v string) bool { return relationIDPattern.MatchString(v) }

// relationEndpointProbe* are fixed well-formed identities so the octo-plugin-
// lib endpoint validator (which also checks the source/target IDs and
// self-reference on the Relation object) can be reused as the single source of
// the relation-type endpoint matrix. The 2.0 contract Relation carries no
// relation_id or timestamps, so the probe supplies only the endpoint IDs and
// type that ValidateRelation still inspects.
const (
	relationProbeSource = "00000000-0000-8000-8000-000000000002"
	relationProbeTarget = "00000000-0000-8000-8000-000000000003"
)

// validRelationType reports whether relation may connect source -> target,
// delegating the endpoint matrix (expert_team_expert: expert_team -> expert,
// expert_skill: expert -> skill, expert_connector: expert -> connector) to
// libplugin.ValidateRelationEndpoints.
func validRelationType(relation string, source, target model.PluginType) bool {
	probe := libplugin.Relation{
		SourcePluginID: relationProbeSource,
		TargetPluginID: relationProbeTarget,
		RelationType:   libplugin.RelationType(relation),
	}
	return libplugin.ValidateRelationEndpoints(probe, libplugin.Type(source), libplugin.Type(target)) == nil
}

// validRelationSource is the fast pre-check run before the target row is
// loaded: the relation type must exist and admit this source type with at
// least one valid target.
func validRelationSource(relation string, source model.PluginType) bool {
	for _, target := range []model.PluginType{model.PluginTypeExpert, model.PluginTypeExpertTeam, model.PluginTypeSkill, model.PluginTypeConnector} {
		if validRelationType(relation, source, target) {
			return true
		}
	}
	return false
}

func normalizeObject(raw json.RawMessage) (json.RawMessage, string, error) {
	canonical, err := canonicalJSONObject(raw)
	if err != nil {
		return nil, "", ErrInvalidRequest
	}
	return canonical, hashJSON(canonical), nil
}

func normalizeOptionalObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	canonical, err := canonicalJSONObject(raw)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return canonical, nil
}

func canonicalJSONObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > maxJSONBytes {
		return nil, ErrInvalidRequest
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, ErrInvalidRequest
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) > maxJSONBytes {
		return nil, ErrInvalidRequest
	}
	return canonical, nil
}

func canonicalJSONValue(value any) (json.RawMessage, error) {
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) > maxJSONBytes {
		return nil, ErrInvalidRequest
	}
	return canonical, nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if err == nil {
		return ErrInvalidRequest
	}
	if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func normalizeTags(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("[]"), nil
	}
	if len(raw) > maxJSONBytes {
		return nil, ErrInvalidRequest
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tags []string
	if err := dec.Decode(&tags); err != nil || ensureJSONEOF(dec) != nil || len(tags) > maxTags {
		return nil, ErrInvalidRequest
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || !utf8.ValidString(tag) || len(tag) > maxTagBytes {
			return nil, ErrInvalidRequest
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return canonicalJSONValue(out)
}

func hashJSON(canonical json.RawMessage) string {
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func hashForTest(raw json.RawMessage) (string, error) {
	canonical, err := canonicalJSONObject(raw)
	if err != nil {
		return "", fmt.Errorf("canonicalize: %w", err)
	}
	return hashJSON(canonical), nil
}
