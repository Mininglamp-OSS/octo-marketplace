package skill

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
)

func TestCanView(t *testing.T) {
	tests := []struct {
		name     string
		row      *skillrepo.SkillRow
		spaceID  string
		userID   string
		expected bool
	}{
		{
			name:     "public same space is visible",
			row:      &skillrepo.SkillRow{Visibility: "public", SpaceID: "s1", OwnerID: "u1"},
			spaceID:  "s1",
			userID:   "u2",
			expected: true,
		},
		{
			name:     "public cross-space is hidden",
			row:      &skillrepo.SkillRow{Visibility: "public", SpaceID: "s1", OwnerID: "u1"},
			spaceID:  "s2",
			userID:   "u2",
			expected: false,
		},
		{
			name:     "space same space",
			row:      &skillrepo.SkillRow{Visibility: "space", SpaceID: "s1", OwnerID: "u1"},
			spaceID:  "s1",
			userID:   "u2",
			expected: true,
		},
		{
			name:     "space different space is hidden",
			row:      &skillrepo.SkillRow{Visibility: "space", SpaceID: "s1", OwnerID: "u1"},
			spaceID:  "s2",
			userID:   "u2",
			expected: false,
		},
		{
			name:     "space different space even for owner",
			row:      &skillrepo.SkillRow{Visibility: "space", SpaceID: "s1", OwnerID: "u1"},
			spaceID:  "s2",
			userID:   "u1",
			expected: false,
		},
		{
			name:     "private owner",
			row:      &skillrepo.SkillRow{Visibility: "private", SpaceID: "s1", OwnerID: "u1"},
			spaceID:  "s1",
			userID:   "u1",
			expected: true,
		},
		{
			name:     "private non-owner",
			row:      &skillrepo.SkillRow{Visibility: "private", SpaceID: "s1", OwnerID: "u1"},
			spaceID:  "s1",
			userID:   "u2",
			expected: false,
		},
		{
			name:     "private cross-space owner",
			row:      &skillrepo.SkillRow{Visibility: "private", SpaceID: "s1", OwnerID: "u1"},
			spaceID:  "s2",
			userID:   "u1",
			expected: false,
		},
		{
			name:     "unknown visibility",
			row:      &skillrepo.SkillRow{Visibility: "unknown", SpaceID: "s1", OwnerID: "u1"},
			spaceID:  "s1",
			userID:   "u1",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canView(tt.row, tt.spaceID, tt.userID)
			if got != tt.expected {
				t.Errorf("canView() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestRowToItemFields(t *testing.T) {
	tags := json.RawMessage(`["tag1"]`)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	row := &skillrepo.SkillRow{
		ID:          "id-1",
		Name:        "Test Skill",
		Description: "desc",
		CategoryID:  "cat-1",
		Tags:        tags,
		OwnerID:     "owner-1",
		OwnerName:   "Owner",
		SpaceID:     "space-1",
		Visibility:  "public",
		Version:     "1.0.0",
		FileName:    "file.zip",
		FileURL:     "http://example.com/file.zip",
		FileSize:    1024,
		FileSHA256:  "abc123",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	svc := &Service{}
	item := svc.rowToItem(context.Background(), row)
	if item.ID != "id-1" {
		t.Errorf("ID = %q", item.ID)
	}
	if item.Name != "Test Skill" {
		t.Errorf("Name = %q", item.Name)
	}
	if item.Visibility != "public" {
		t.Errorf("Visibility = %q", item.Visibility)
	}
	if item.FileSize != 1024 {
		t.Errorf("FileSize = %d", item.FileSize)
	}
	if item.CreatedAt != "2026-07-14T12:00:00Z" {
		t.Errorf("CreatedAt = %q", item.CreatedAt)
	}
}

func TestRowToItemSanitizesReadmeContent(t *testing.T) {
	svc := &Service{}
	item := svc.rowToItem(context.Background(), &skillrepo.SkillRow{
		ID:            "id-1",
		Name:          "skill",
		ReadmeContent: "# Demo\n\n<script>alert(1)</script>\n<div>ok</div>",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	})
	if item.ReadmeContent == "" {
		t.Fatal("expected sanitized readme")
	}
	if strings.Contains(item.ReadmeContent, "<script>") {
		t.Fatalf("script tag should be removed, got %q", item.ReadmeContent)
	}
	if !strings.Contains(item.ReadmeContent, "&lt;div&gt;ok&lt;/div&gt;") {
		t.Fatalf("html should be escaped, got %q", item.ReadmeContent)
	}
}

func TestErrNotFound(t *testing.T) {
	if !errors.Is(ErrNotFound, ErrNotFound) {
		t.Error("ErrNotFound should be identifiable")
	}
}

func TestErrInvalidParseTask(t *testing.T) {
	if !errors.Is(ErrInvalidParseTask, ErrInvalidParseTask) {
		t.Error("ErrInvalidParseTask should be identifiable")
	}
}

func TestErrParseTaskConsumed(t *testing.T) {
	if !errors.Is(ErrParseTaskConsumed, ErrParseTaskConsumed) {
		t.Error("ErrParseTaskConsumed should be identifiable")
	}
}

func TestToVisibility(t *testing.T) {
	v := toVisibility("public")
	if string(v) != "public" {
		t.Errorf("toVisibility(\"public\") = %q", v)
	}
}

func TestToVisibilityPtr(t *testing.T) {
	v := toVisibilityPtr("private")
	if v == nil {
		t.Fatal("expected non-nil pointer")
	}
	if string(*v) != "private" {
		t.Errorf("toVisibilityPtr(\"private\") = %q", *v)
	}
}
