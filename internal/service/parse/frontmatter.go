package parse

import (
	"bufio"
	"bytes"
	"strings"

	"gopkg.in/yaml.v3"
)

// FrontmatterResult holds the parsed frontmatter metadata from SKILL.md.
type FrontmatterResult struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Version     string   `yaml:"version"`
	Tags        []string `yaml:"tags"`
}

// ParseFrontmatter extracts YAML frontmatter from a Markdown file.
// Returns the parsed result and the remaining body content.
// If no frontmatter is found, it infers name/description from the Markdown.
func ParseFrontmatter(content []byte) (FrontmatterResult, string) {
	text := string(content)
	scanner := bufio.NewScanner(strings.NewReader(text))

	// Check for YAML frontmatter delimiters
	var lines []string
	inFrontmatter := false
	frontmatterStarted := false
	var fmLines []string
	var bodyLines []string

	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
	}

	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		inFrontmatter = true
		frontmatterStarted = true
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				inFrontmatter = false
				bodyLines = lines[i+1:]
				break
			}
			fmLines = append(fmLines, lines[i])
		}
		if inFrontmatter {
			// Never closed — no valid frontmatter
			frontmatterStarted = false
			bodyLines = lines
		}
	} else {
		bodyLines = lines
	}

	body := strings.Join(bodyLines, "\n")

	if frontmatterStarted && len(fmLines) > 0 {
		var result FrontmatterResult
		fmContent := strings.Join(fmLines, "\n")
		if err := yaml.Unmarshal([]byte(fmContent), &result); err == nil {
			if result.Version == "" {
				result.Version = "1.0.0"
			}
			return result, strings.TrimSpace(body)
		}
	}

	// No frontmatter — infer from Markdown
	return inferFromMarkdown(body), strings.TrimSpace(text)
}

// inferFromMarkdown extracts name from the first heading and description from the first paragraph.
func inferFromMarkdown(content string) FrontmatterResult {
	result := FrontmatterResult{Version: "1.0.0"}
	scanner := bufio.NewScanner(bytes.NewBufferString(content))

	foundTitle := false
	var descLines []string

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if !foundTitle && strings.HasPrefix(trimmed, "# ") {
			result.Name = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			foundTitle = true
			continue
		}

		if foundTitle {
			if trimmed == "" {
				if len(descLines) > 0 {
					break // end of first paragraph
				}
				continue // skip blank lines before paragraph
			}
			// Stop at next heading
			if strings.HasPrefix(trimmed, "#") {
				break
			}
			descLines = append(descLines, trimmed)
		}
	}

	if len(descLines) > 0 {
		result.Description = strings.Join(descLines, " ")
		// Limit description length
		if len(result.Description) > 500 {
			result.Description = result.Description[:500]
		}
	}
	return result
}
