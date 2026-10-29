package artifacts

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// visitCarriers traverses host-defined storage metadata, never arbitrary file
// contents or MCP configuration. Field names inside those payloads are opaque.
func visitCarriers(column string, raw []byte, visit func(string, map[string]any) error) error {
	var walk func(string, any) error
	walk = func(context string, value any) error {
		if text, ok := value.(string); ok {
			if text == "" || text == "null" {
				return nil
			}
			var decoded any
			d := json.NewDecoder(bytes.NewBufferString(text))
			d.UseNumber()
			if d.Decode(&decoded) != nil {
				return fmt.Errorf("invalid_reference_json")
			}
			return walk(context, decoded)
		}
		if list, ok := value.([]any); ok {
			for _, entry := range list {
				if e := walk(context, entry); e != nil {
					return e
				}
			}
			return nil
		}
		m, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		switch context {
		case "attachment_keys_json":
			return visit("sidecar", m)
		case "artifacts_json":
			return visit("artifact", m)
		case "skill/ref.json", "storage", "version_storage", "skills_json":
			return visit("ref", m)
		case "attachments":
			if typ, _ := m["content_type"].(string); typ == "storage" {
				return visit("storage", m)
			}
			if name, _ := m["path"].(string); name == "skill/ref.json" {
				if content, ok := m["raw_content"].(string); ok {
					return walk("skill/ref.json", content)
				}
			}
			return nil
		case "members_json":
			if skills, ok := m["skills"]; ok {
				return walk("skills_json", skills)
			}
			return nil
		case "snapshot_row":
			if e := visit("ref", m); e != nil {
				return e
			}
		}
		if e := visit("icons", m); e != nil {
			return e
		}
		for key, nested := range m {
			switch key {
			case "attachments", "attachment_keys_json", "artifacts_json", "storage", "version_storage", "skills_json", "members_json":
				if nested != nil {
					if e := walk(key, nested); e != nil {
						return e
					}
				}
			case "plugin_json", "plugin_snapshot_json", "manifest_json", "manifest_snapshot_json", "before_json", "after_json", "snapshot_json", "plugin", "package", "manifest":
				if nested != nil {
					if e := walk("document", nested); e != nil {
						return e
					}
				}
			}
		}
		return nil
	}
	switch column {
	case "attachment_keys_json", "artifacts_json", "skill/ref.json", "storage", "version_storage", "skills_json", "members_json", "snapshot_row":
	case "", "plugin_json", "plugin_snapshot_json", "manifest_json", "manifest_snapshot_json", "before_json", "after_json", "snapshot_json":
		column = "document"
	default:
		return nil
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return nil
	}
	if model.ValidateUniqueJSONFields(raw) != nil {
		return fmt.Errorf("invalid_reference_json")
	}
	return walk(column, value)
}
