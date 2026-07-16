package parse

import (
	"testing"
)

func TestParseFrontmatter_WithValidYAML(t *testing.T) {
	content := []byte(`---
name: my-skill
description: A test skill
version: 2.0.0
tags:
  - ai
  - testing
---

# My Skill

This is the body content.
`)
	result, body := ParseFrontmatter(content)

	if result.Name != "my-skill" {
		t.Errorf("Name = %q, want %q", result.Name, "my-skill")
	}
	if result.Description != "A test skill" {
		t.Errorf("Description = %q, want %q", result.Description, "A test skill")
	}
	if result.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", result.Version, "2.0.0")
	}
	if len(result.Tags) != 2 || result.Tags[0] != "ai" || result.Tags[1] != "testing" {
		t.Errorf("Tags = %v, want [ai testing]", result.Tags)
	}
	if body != "# My Skill\n\nThis is the body content." {
		t.Errorf("body = %q", body)
	}
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	content := []byte(`# Hello World

This is a description paragraph.

## Section 2

More content.
`)
	result, body := ParseFrontmatter(content)

	if result.Name != "Hello World" {
		t.Errorf("Name = %q, want %q", result.Name, "Hello World")
	}
	if result.Description != "This is a description paragraph." {
		t.Errorf("Description = %q, want %q", result.Description, "This is a description paragraph.")
	}
	if result.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", result.Version, "1.0.0")
	}
	if len(result.Tags) != 0 {
		t.Errorf("Tags = %v, want empty", result.Tags)
	}
	if body == "" {
		t.Error("body should not be empty")
	}
}

func TestParseFrontmatter_EmptyVersion(t *testing.T) {
	content := []byte(`---
name: skill-no-version
description: No version
---

Content here.
`)
	result, _ := ParseFrontmatter(content)
	if result.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", result.Version, "1.0.0")
	}
}

func TestParseFrontmatter_UnclosedFrontmatter(t *testing.T) {
	content := []byte(`---
name: broken
description: never closed
`)
	result, _ := ParseFrontmatter(content)
	// Should infer from markdown since frontmatter is invalid
	if result.Name != "" {
		t.Errorf("Name = %q, want empty (no heading)", result.Name)
	}
}

func TestParseFrontmatter_EmptyContent(t *testing.T) {
	content := []byte("")
	result, body := ParseFrontmatter(content)
	if result.Name != "" {
		t.Errorf("Name = %q, want empty", result.Name)
	}
	if result.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", result.Version, "1.0.0")
	}
	if body != "" {
		t.Errorf("body = %q, want empty", body)
	}
}

func TestParseFrontmatter_MultiParagraphInfer(t *testing.T) {
	content := []byte(`# Tool Name

First paragraph of description
that spans multiple lines.

Second paragraph not included.
`)
	result, _ := ParseFrontmatter(content)
	if result.Name != "Tool Name" {
		t.Errorf("Name = %q, want %q", result.Name, "Tool Name")
	}
	expected := "First paragraph of description that spans multiple lines."
	if result.Description != expected {
		t.Errorf("Description = %q, want %q", result.Description, expected)
	}
}
