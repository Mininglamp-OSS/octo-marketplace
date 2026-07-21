package skill

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	categoryrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/category"
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
			name:     "public cross-space is visible",
			row:      &skillrepo.SkillRow{Visibility: "public", SpaceID: "s1", OwnerID: "u1"},
			spaceID:  "s2",
			userID:   "u2",
			expected: true,
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

func TestToListResultResolvesTagNamesInOneBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := &Service{repo: skillrepo.New(db)}
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	result := &skillrepo.ListResult{
		Items: []skillrepo.SkillRow{
			{ID: "skill-1", Name: "Skill 1", Tags: tagIDJSON(1, 2), CreatedAt: now, UpdatedAt: now},
			{ID: "skill-2", Name: "Skill 2", Tags: tagIDJSON(2, 3), CreatedAt: now, UpdatedAt: now},
		},
	}
	expectResolveTagNames(mock, []int64{1, 2, 3}, []string{"alpha", "beta", "gamma"})

	items := svc.toListResult(context.Background(), result).Items
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	if got := strings.Join(items[0].Tags, ","); got != "alpha,beta" {
		t.Fatalf("first tags = %q", got)
	}
	if got := strings.Join(items[1].Tags, ","); got != "beta,gamma" {
		t.Fatalf("second tags = %q", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteDeletesAllVersionArtifacts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := &fakeStorage{}
	svc := New(skillrepo.New(db), categoryrepo.New(db), store, func() string { return "id" })
	now := time.Now()

	currentStorage := `{"type":"s3","zip_object_key":"skills/user-skill/v2/skill.zip","skill_md_object_key":"skills/user-skill/v2/SKILL.md"}`
	oldStorage := `{"type":"s3","zip_object_key":"skills/user-skill/v1/skill.zip","skill_md_object_key":"skills/user-skill/v1/SKILL.md"}`
	legacyStorage := `{"type":"s3","object_key":"skills/user-skill/v0/legacy.zip"}`

	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("user-skill").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
			"description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at",
			"resolved_version", "version_storage",
			"view_count", "download_count",
		}).AddRow(
			"user-skill", "octo-style", "octo-style", "", "", "v2",
			"desc", "cat1", []byte(`[]`),
			"user-1", "User One", "space-1", "space", "2.0.0",
			"", "skill.zip", "skills/user-skill/v2/skill.zip", int64(2048), "sha2",
			now, now,
			"2.0.0", currentStorage,
			int64(0), int64(0),
		))
	mock.ExpectQuery("SELECT id, skill_id, version, changelog, storage, changed_by, created_at").
		WithArgs("user-skill").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "skill_id", "version", "changelog", "storage", "changed_by", "created_at",
		}).
			AddRow("v2", "user-skill", "2.0.0", "", currentStorage, "user-1", now).
			AddRow("v1", "user-skill", "1.0.0", "", oldStorage, "user-1", now.Add(-time.Hour)).
			AddRow("v0", "user-skill", "0.9.0", "", legacyStorage, "user-1", now.Add(-2*time.Hour)))
	mock.ExpectExec("UPDATE skills").
		WithArgs("user-skill").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := svc.Delete(context.Background(), "user-skill", "user-1", "space-1"); err != nil {
		t.Fatalf("Delete error = %v", err)
	}

	want := map[string]bool{
		"skills/user-skill/v2/skill.zip":  true,
		"skills/user-skill/v2/SKILL.md":   true,
		"skills/user-skill/v1/skill.zip":  true,
		"skills/user-skill/v1/SKILL.md":   true,
		"skills/user-skill/v0/legacy.zip": true,
	}
	if len(store.deleteKeys) != len(want) {
		t.Fatalf("deleteKeys=%v, want %d keys", store.deleteKeys, len(want))
	}
	for _, key := range store.deleteKeys {
		if !want[key] {
			t.Fatalf("unexpected delete key %q in %v", key, store.deleteKeys)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("missing delete keys: %v", want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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

func TestNormalizeVisibility(t *testing.T) {
	v, err := normalizeVisibility("", "space")
	if err != nil {
		t.Fatalf("normalizeVisibility default error = %v", err)
	}
	if string(v) != "space" {
		t.Fatalf("normalizeVisibility default = %q, want space", v)
	}
	if _, err := normalizeVisibility("system", ""); !errors.Is(err, ErrInvalidVisibility) {
		t.Fatalf("normalizeVisibility invalid error = %v, want ErrInvalidVisibility", err)
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

func TestRowToItem_LegacyObjectKeyFallback(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	row := &skillrepo.SkillRow{
		ID:              "id-legacy",
		Name:            "Legacy V2",
		Version:         "1.0.0",
		ResolvedVersion: "1.0.0",
		VersionStorage:  `{"type":"s3","object_key":"skills/id-legacy/v1.0.0/skill.zip"}`,
		FileName:        "",
		FileURL:         "",
		FileSize:        0,
		FileSHA256:      "",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	svc := &Service{}
	item := svc.rowToItem(context.Background(), row)

	if item.FileURL != "skills/id-legacy/v1.0.0/skill.zip" {
		t.Errorf("FileURL = %q, want %q", item.FileURL, "skills/id-legacy/v1.0.0/skill.zip")
	}
	if item.FileName != "skill.zip" {
		t.Errorf("FileName = %q, want %q", item.FileName, "skill.zip")
	}
}

func TestErrIDMismatch(t *testing.T) {
	if !errors.Is(ErrIDMismatch, ErrIDMismatch) {
		t.Error("ErrIDMismatch should be identifiable")
	}
}
