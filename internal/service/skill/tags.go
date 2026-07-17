package skill

import (
	"encoding/json"
	"strings"
)

func normalizeRawTags(raw json.RawMessage) (json.RawMessage, []string, error) {
	if raw == nil {
		return nil, nil, nil
	}
	var tags []string
	if err := json.Unmarshal(raw, &tags); err != nil {
		return nil, nil, err
	}
	tags = normalizeTags(tags)
	if tags == nil {
		tags = []string{}
	}
	out, err := json.Marshal(tags)
	if err != nil {
		return nil, nil, err
	}
	return json.RawMessage(out), tags, nil
}

func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

// ParseTagFilters normalizes comma-separated and repeated query tag filters.
func ParseTagFilters(values ...string) []string {
	var tags []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			tag := strings.TrimSpace(part)
			if tag != "" {
				tags = append(tags, tag)
			}
		}
	}
	return normalizeTags(tags)
}
