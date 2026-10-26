package model

import "encoding/json"

// ConnectorToolCount derives optional display metadata from frozen content.
// Approval must not copy the tool count from a newer editing revision.
func ConnectorToolCount(pkg json.RawMessage) int {
	var doc struct {
		Attachments []struct {
			Path string `json:"path"`
			Type string `json:"content_type"`
			Raw  string `json:"raw_content"`
		} `json:"attachments"`
	}
	if json.Unmarshal(pkg, &doc) != nil {
		return 0
	}
	for _, a := range doc.Attachments {
		if a.Path == "connector/tools.json" && a.Type == "raw" {
			var tools []json.RawMessage
			if json.Unmarshal([]byte(a.Raw), &tools) != nil {
				return 0
			}
			return len(tools)
		}
	}
	return 0
}
