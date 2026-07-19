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

func TestRowToItem_UsesVersionStorage(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	row := &skillrepo.SkillRow{
		ID:              "id-v",
		Name:            "Versioned Skill",
		Version:         "1.0.0",
		ResolvedVersion: "2.0.0",
		VersionStorage:  `{"type":"s3","zip_object_key":"skills/id-v/v2.0.0/skill.zip","skill_md_object_key":"skills/id-v/v2.0.0/SKILL.md","zip_file_name":"skill.zip","zip_size":2048,"zip_sha256":"newsha"}`,
		FileName:        "old.zip",
		FileURL:         "skills/id-v/v1.0.0/old.zip",
		FileSize:        512,
		FileSHA256:      "oldsha",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	svc := &Service{}
	item := svc.rowToItem(context.Background(), row)

	if item.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", item.Version, "2.0.0")
	}
	if item.FileName != "skill.zip" {
		t.Errorf("FileName = %q, want %q", item.FileName, "skill.zip")
	}
	if item.FileURL != "skills/id-v/v2.0.0/skill.zip" {
		t.Errorf("FileURL = %q, want %q", item.FileURL, "skills/id-v/v2.0.0/skill.zip")
	}
	if item.FileSize != 2048 {
		t.Errorf("FileSize = %d, want %d", item.FileSize, 2048)
	}
	if item.FileSHA256 != "newsha" {
		t.Errorf("FileSHA256 = %q, want %q", item.FileSHA256, "newsha")
	}
}

func TestRowToItem_FallbackWhenNoVersionStorage(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	row := &skillrepo.SkillRow{
		ID:              "id-old",
		Name:            "Legacy Skill",
		Version:         "1.0.0",
		ResolvedVersion: "",
		VersionStorage:  "",
		FileName:        "legacy.zip",
		FileURL:         "skills/id-old/v1.0.0/legacy.zip",
		FileSize:        1024,
		FileSHA256:      "legsha",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	svc := &Service{}
	item := svc.rowToItem(context.Background(), row)

	if item.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", item.Version, "1.0.0")
	}
	if item.FileURL != "skills/id-old/v1.0.0/legacy.zip" {
		t.Errorf("FileURL = %q, want %q", item.FileURL, "skills/id-old/v1.0.0/legacy.zip")
	}
	if item.FileSize != 1024 {
		t.Errorf("FileSize = %d, want %d", item.FileSize, 1024)
	}
	if item.FileSHA256 != "legsha" {
		t.Errorf("FileSHA256 = %q, want %q", item.FileSHA256, "legsha")
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

func TestExtractReadmeBody(t *testing.T) {
	tests := []struct {
		name string
		md   []byte
		want string
	}{
		{
			name: "with frontmatter",
			md:   []byte("---\nname: test\n---\n# Hello\nWorld"),
			want: "# Hello\nWorld",
		},
		{
			name: "no frontmatter",
			md:   []byte("# No FM\nContent"),
			want: "# No FM\nContent",
		},
		{
			name: "empty body after frontmatter",
			md:   []byte("---\nname: test\n---\n"),
			want: "",
		},
		{
			name: "nil input",
			md:   nil,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractReadmeBody(tt.md)
			if got != tt.want {
				t.Errorf("extractReadmeBody() = %q, want %q", got, tt.want)
			}
		})
	}
}
