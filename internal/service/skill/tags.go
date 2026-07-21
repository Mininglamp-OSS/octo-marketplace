package skill

import (
	"encoding/json"
	"strconv"
	"strings"
)

func rawTagsToStrings(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var tags []string
	if err := json.Unmarshal(raw, &tags); err != nil {
		return []string{}
	}
	return normalizeTags(tags)
}

func tagsToRaw(tags []string) (json.RawMessage, error) {
	if tags == nil {
		return nil, nil
	}
	normalized := normalizeTags(tags)
	if normalized == nil {
		normalized = []string{}
	}
	out, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func rawTagsToIDs(raw json.RawMessage) []int64 {
	if len(raw) == 0 {
		return []int64{}
	}
	var ids []int64
	if err := json.Unmarshal(raw, &ids); err != nil {
		return []int64{}
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func rawTagsToIDStrings(raw json.RawMessage) []string {
	ids := rawTagsToIDs(raw)
	if len(ids) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatInt(id, 10))
	}
	return out
}

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
