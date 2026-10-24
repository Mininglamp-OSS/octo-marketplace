package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/plugindoc"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
)

type referenceContainerTarget struct {
	ID        string  `json:"id"`
	VersionID *string `json:"version_id,omitempty"`
	MemberKey string  `json:"member_key,omitempty"`
	Role      string  `json:"role,omitempty"`
	IsLeader  bool    `json:"is_leader,omitempty"`
}
type referenceContainer struct {
	SchemaVersion string                     `json:"schema_version"`
	Kind          string                     `json:"kind"`
	Name          string                     `json:"name"`
	Summary       string                     `json:"summary,omitempty"`
	Tags          []string                   `json:"tags,omitempty"`
	Instruction   string                     `json:"instruction"`
	Skills        []referenceContainerTarget `json:"skills,omitempty"`
	Connectors    []referenceContainerTarget `json:"connectors,omitempty"`
	Members       []referenceContainerTarget `json:"members,omitempty"`
}

// parseReferenceContainer accepts only references in the declared v2 envelope.
// Unknown inline overrides and additional archive files fail closed before any
// storage object or graph mutation is created.
func (s *Service) parseReferenceContainer(p ContainerImportParams) (WriteRequest, error) {
	if p.SchemaVersion != "2" {
		return WriteRequest{}, ErrInvalidContainer
	}
	files, code, _ := parse.ExtractArchive(bytes.NewReader(p.Archive), int64(len(p.Archive)), s.maxArchiveBytes, s.maxArchiveBytes, containerMaxArchiveFiles)
	if code != "" || len(files) != 1 {
		return WriteRequest{}, ErrInvalidContainer
	}
	raw, expert := files["expert.json"]
	if !expert {
		raw = files["squad.json"]
	}
	if len(raw) == 0 || len(raw) > containerMaxManifestBytes {
		return WriteRequest{}, ErrInvalidContainer
	}
	if model.ValidateUniqueJSONFields(raw) != nil {
		return WriteRequest{}, ErrInvalidContainer
	}
	var doc referenceContainer
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&doc) != nil || decoder.Decode(new(any)) != io.EOF || doc.SchemaVersion != "2" || !validName(doc.Name) || len(doc.Instruction) > containerMaxManifestBytes {
		return WriteRequest{}, ErrInvalidContainer
	}
	typ := model.PluginTypeExpert
	if expert {
		if doc.Kind != "agent" || len(doc.Members) != 0 || len(doc.Skills) > containerMaxSkills || len(doc.Connectors) > containerMaxSkills {
			return WriteRequest{}, ErrInvalidContainer
		}
	} else {
		typ = model.PluginTypeExpertTeam
		if doc.Kind != "squad" || len(doc.Members) == 0 || len(doc.Members) > containerMaxMembers || len(doc.Skills)+len(doc.Connectors) != 0 {
			return WriteRequest{}, ErrInvalidContainer
		}
	}
	rels := []RelationRequest{}
	appendTargets := func(targets []referenceContainerTarget, relType string) error {
		for i, target := range targets {
			var data json.RawMessage
			if relType == "expert_team_expert" {
				data, _ = json.Marshal(map[string]any{"member_key": target.MemberKey, "role": target.Role, "is_leader": target.IsLeader})
			} else if target.MemberKey != "" || target.Role != "" || target.IsLeader {
				return ErrInvalidContainer
			}
			if relType == "expert_connector" && target.VersionID != nil {
				return ErrInvalidContainer
			}
			rels = append(rels, RelationRequest{TargetPluginID: target.ID, TargetVersionID: target.VersionID, Type: relType, SortOrder: i, Data: data})
		}
		return nil
	}
	if appendTargets(doc.Skills, "expert_skill") != nil || appendTargets(doc.Connectors, "expert_connector") != nil || appendTargets(doc.Members, "expert_team_expert") != nil {
		return WriteRequest{}, ErrInvalidContainer
	}
	manifest, pkg, err := pluginDocuments(typ, doc.Name, doc.Summary, doc.Tags, []map[string]any{rawAttachmentMap("AGENTS.md", "text/markdown", plugindoc.ExpertAgentsMarkdown(doc.Name, doc.Summary, doc.Instruction))})
	if err != nil {
		return WriteRequest{}, err
	}
	return WriteRequest{Name: doc.Name, Type: typ, CategoryID: p.CategoryID, Version: p.Version, Manifest: manifest, Package: pkg, Relations: rels}, nil
}
func (s *Service) importReferenceContainer(ctx context.Context, caller Caller, p ContainerImportParams, pluginID string) (*Detail, error) {
	req, err := s.parseReferenceContainer(p)
	if err != nil {
		return nil, err
	}
	if pluginID == "" {
		return s.AdminCreate(ctx, caller, req)
	}
	return s.AdminUpdate(ctx, caller, pluginID, req)
}
