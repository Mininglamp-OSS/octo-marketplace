package plugin

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func (b *capabilityBuilder) marketConnector(ctx context.Context, detail *Detail, name string) (fleet.CapabilityConnector, error) {
	p := detail.Plugin
	if p.Type != model.PluginTypeConnector {
		return fleet.CapabilityConnector{}, installationInvalid("definition.connectors", "wrong_dependency_type")
	}
	if !installationName(name, maxInstallationNameRunes) {
		return fleet.CapabilityConnector{}, installationInvalid("definition.connectors.name", "invalid")
	}
	config, present, err := b.connectorConfig(ctx, p)
	if err != nil {
		return fleet.CapabilityConnector{}, err
	}
	if !present {
		return fleet.CapabilityConnector{}, invalidCapabilityConnectorMCP("required")
	}

	var manifest struct {
		Description   string `json:"description"`
		Documentation string `json:"documentation"`
	}
	if json.Unmarshal(p.Manifest, &manifest) != nil {
		return fleet.CapabilityConnector{}, installationInvalid("definition.connectors", "invalid_market_metadata")
	}
	tags, err := capabilityConnectorTags(p.Tags)
	if err != nil {
		return fleet.CapabilityConnector{}, err
	}

	connector := fleet.CapabilityConnector{
		Name: name, Description: manifest.Description,
		Documentation: manifest.Documentation, Config: config,
		Version: p.CurrentVersion, MarketMCPID: p.ID, Tags: tags,
	}
	if p.CategoryID != nil {
		connector.Category = *p.CategoryID
	}
	if p.CreatorName != "" {
		author := p.CreatorName
		connector.AuthorName = &author
	}
	for _, value := range []string{connector.Name, connector.Description, connector.Slug, connector.Category, connector.Documentation} {
		if err := b.text(value); err != nil {
			return fleet.CapabilityConnector{}, err
		}
	}
	if connector.AuthorName != nil {
		if err := b.text(*connector.AuthorName); err != nil {
			return fleet.CapabilityConnector{}, err
		}
	}
	for _, tag := range connector.Tags {
		if err := b.text(tag); err != nil {
			return fleet.CapabilityConnector{}, err
		}
	}
	return connector, nil
}

func (b *capabilityBuilder) connectorConfig(ctx context.Context, p *model.Plugin) (json.RawMessage, bool, error) {
	var target *attachmentView
	attachments := decodePackageAttachments(p.Package)
	for i := range attachments {
		if attachments[i].Path != "mcp.json" {
			continue
		}
		if target != nil {
			return nil, false, invalidCapabilityConnectorMCP("duplicate_attachment")
		}
		target = &attachments[i]
	}
	if target == nil {
		return nil, false, nil
	}

	var raw string
	switch target.ContentType {
	case "raw":
		raw = target.RawContent
	case "storage":
		key, ok := storageAttachmentKey(p, "mcp.json")
		if !ok {
			return nil, false, installationInvalid("definition.connectors.config", "unavailable_source")
		}
		data, err := b.object(ctx, p, key, min(b.remaining, int64(1<<20)), target.ContentSize, target.ContentHash)
		if err != nil {
			return nil, false, err
		}
		raw = string(data)
	default:
		return nil, false, installationInvalid("definition.connectors.config", "unsupported_source")
	}
	if err := b.text(raw); err != nil {
		return nil, false, err
	}
	config, present, err := normalizeCapabilityConnectorMCP(raw)
	if err != nil {
		return nil, false, err
	}
	return config, present, nil
}

func capabilityConnectorTags(raw json.RawMessage) ([]string, error) {
	var tags []string
	if json.Unmarshal(raw, &tags) != nil {
		return nil, installationInvalid("definition.connectors.tags", "invalid")
	}
	return tags, nil
}

// normalizeCapabilityConnectorMCP validates only Fleet's connector snapshot
// shape; opaque server and extension fields remain part of the snapshot.
func normalizeCapabilityConnectorMCP(raw string) (json.RawMessage, bool, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var document map[string]any
	if decoder.Decode(&document) != nil || document == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, false, invalidCapabilityConnectorMCP("invalid_json")
	}
	if len(document) == 0 {
		return nil, false, nil
	}
	servers, ok := document["mcpServers"].(map[string]any)
	if !ok {
		return nil, false, invalidCapabilityConnectorMCP("invalid_servers")
	}
	for _, server := range servers {
		if _, ok := server.(map[string]any); !ok {
			return nil, false, invalidCapabilityConnectorMCP("invalid_server")
		}
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return nil, false, invalidCapabilityConnectorMCP("invalid_json")
	}
	if len(canonical) > model.MaxMCPConfigBytes {
		return nil, false, ErrTooLarge
	}
	if len(servers) == 0 {
		return nil, false, nil
	}
	return canonical, true, nil
}

func invalidCapabilityConnectorMCP(reason string) error {
	return installationInvalid("definition.connectors.config", reason)
}
func (b *capabilityBuilder) addConnector(identity string, connector fleet.CapabilityConnector) (string, error) {
	if existing, ok := b.connectorsByID[identity]; ok {
		return existing.Name, nil
	}
	if owner, ok := b.connectorOwnerByName[connector.Name]; ok && owner != identity {
		return "", installationInvalid("definition.connectors.name", "ambiguous")
	}
	b.connectorsByID[identity] = connector
	b.connectorOwnerByName[connector.Name] = identity
	return connector.Name, nil
}

func appendCapabilityConnectorName(expert *fleet.CapabilityExpert, name string) {
	for _, existing := range expert.ConnectorNames {
		if existing == name {
			return
		}
	}
	expert.ConnectorNames = append(expert.ConnectorNames, name)
}
