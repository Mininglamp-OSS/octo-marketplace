package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// WithReferences gates the complete new reference write path; version management
// remains a prerequisite. No runtime configuration enables it by default.
func (s *Service) WithReferences(enabled bool) *Service {
	s.references = enabled && s.versionManagement
	if repo, ok := s.repo.(interface{ WithReferences(bool) *pluginrepo.Repo }); ok {
		repo.WithReferences(s.references)
	}
	return s
}

// rejectInlineCapabilities rejects package-level bundled skill/MCP/member files.
// Standalone skill and connector documents retain their existing formats.
func rejectInlineCapabilities(typ model.PluginType, raw json.RawMessage) error {
	if typ != model.PluginTypeExpert && typ != model.PluginTypeExpertTeam {
		return nil
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return ErrInvalidRequest
	}
	for _, key := range []string{"skills", "mcp_config", "mcp_servers", "members", "experts"} {
		if value, ok := doc[key]; ok && len(bytes.TrimSpace(value)) > 0 && string(value) != "null" {
			return ErrInvalidRequest
		}
	}
	var attachments []struct {
		Path string `json:"path"`
	}
	if value, ok := doc["attachments"]; ok && json.Unmarshal(value, &attachments) != nil {
		return ErrInvalidRequest
	}
	for _, a := range attachments {
		p := strings.ToLower(strings.TrimSpace(a.Path))
		if p == "mcp.json" || p == "expert/context.json" || strings.HasPrefix(p, "skills/") || strings.HasPrefix(p, "members/") {
			return ErrInvalidRequest
		}
	}
	return nil
}

// ValidateReferences is an explicit read-only preflight for API clients. The
// repository still repeats all authorization under mutation locks.
func (s *Service) validateReferenceShape(ctx context.Context, source *model.Plugin, rels []model.PluginRelation) error {
	if !s.references {
		return nil
	}
	if err := pluginrepo.ValidateReferenceMetadata(source.Type, rels); err != nil {
		return ErrInvalidRequest
	}
	return nil
}

var ErrReferenceIndexIncomplete = errors.New("reference index coverage is incomplete")

type referenceReadStore interface {
	ReferenceDependents(context.Context, pluginrepo.Scope, string, string, int, int) ([]pluginrepo.ReferenceDependent, int, error)
	ReferenceCandidates(context.Context, pluginrepo.Scope, string, string, int, int) ([]pluginrepo.ReferenceCandidate, int, error)
}

func (s *Service) ReferenceDependents(ctx context.Context, caller Caller, pluginID, versionID string, limit, offset int, admin bool) ([]pluginrepo.ReferenceDependent, int, error) {
	if !s.references {
		return nil, 0, ErrNotFound
	}
	id, err := parseStorageID(pluginID)
	if err != nil {
		return nil, 0, err
	}
	store, ok := s.repo.(referenceReadStore)
	if !ok {
		return nil, 0, ErrInvalidRequest
	}
	sc := scope(caller)
	if admin {
		sc = adminScope(caller)
	}
	out, total, err := store.ReferenceDependents(ctx, sc, id, versionID, limit, offset)
	if errors.Is(err, pluginrepo.ErrReferenceIndexIncomplete) {
		return nil, 0, ErrReferenceIndexIncomplete
	}
	return out, total, mapStoreError(err)
}
func (s *Service) ReferenceCandidates(ctx context.Context, caller Caller, pluginID, relationType string, limit, offset int) ([]pluginrepo.ReferenceCandidate, int, error) {
	if !s.references {
		return nil, 0, ErrNotFound
	}
	id, err := parseStorageID(pluginID)
	if err != nil {
		return nil, 0, err
	}
	store, ok := s.repo.(referenceReadStore)
	if !ok {
		return nil, 0, ErrInvalidRequest
	}
	out, total, err := store.ReferenceCandidates(ctx, scope(caller), id, relationType, limit, offset)
	return out, total, mapStoreError(err)
}
