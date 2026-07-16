package model

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCategoryJSON(t *testing.T) {
	c := Category{
		ID:        "cat-1",
		Name:      "Automation",
		IconKey:   "robot",
		SortOrder: 1,
		CreatedAt: time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal Category: %v", err)
	}
	var got Category
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal Category: %v", err)
	}
	if got.ID != c.ID || got.Name != c.Name || got.IconKey != c.IconKey || got.SortOrder != c.SortOrder {
		t.Fatalf("Category round-trip mismatch: got=%+v", got)
	}
}

func TestSkillJSON(t *testing.T) {
	tags := json.RawMessage(`["go","automation"]`)
	s := Skill{
		ID:            "skill-1",
		Name:          "My Skill",
		Description:   "A test skill",
		CategoryID:    "cat-1",
		Tags:          tags,
		OwnerName:     "Alice",
		Visibility:    VisibilityPublic,
		Version:       "1.0.0",
		ReadmeContent: "# README",
		FileName:      "skill.tar.gz",
		FileSize:      1024,
		CreatedAt:     time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal Skill: %v", err)
	}
	var got Skill
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal Skill: %v", err)
	}
	if got.ID != s.ID || got.Name != s.Name || got.Visibility != VisibilityPublic {
		t.Fatalf("Skill round-trip mismatch: got=%+v", got)
	}
	var gotTags []string
	if err := json.Unmarshal(got.Tags, &gotTags); err != nil {
		t.Fatalf("Unmarshal tags: %v", err)
	}
	if len(gotTags) != 2 || gotTags[0] != "go" {
		t.Fatalf("tags mismatch: %v", gotTags)
	}
}

func TestParseTaskJSON(t *testing.T) {
	desc := "parsed desc"
	readme := "# Skill"
	pt := ParseTask{
		ID:                "task-1",
		UploadID:          "upload-1",
		FileURL:           "https://cdn.example.com/upload.tar.gz",
		Status:            ParseStatusPending,
		ErrorCode:         "",
		ErrorMessage:      "",
		ResultName:        "",
		ResultDescription: &desc,
		ResultVersion:     "",
		ResultTags:        json.RawMessage(`["tag1"]`),
		ResultReadme:      &readme,
		OwnerID:           "user-1",
		SpaceID:           "space-1",
		CreatedAt:         time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
		UpdatedAt:         time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(pt)
	if err != nil {
		t.Fatalf("Marshal ParseTask: %v", err)
	}
	var got ParseTask
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal ParseTask: %v", err)
	}
	if got.Status != ParseStatusPending || got.UploadID != "upload-1" {
		t.Fatalf("ParseTask round-trip mismatch: got=%+v", got)
	}
}

func TestApiResponseJSON(t *testing.T) {
	resp := ApiResponse{
		Code:    200,
		Message: "ok",
		Data:    map[string]string{"key": "value"},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal ApiResponse: %v", err)
	}
	var got ApiResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal ApiResponse: %v", err)
	}
	if got.Code != 200 || got.Message != "ok" {
		t.Fatalf("ApiResponse round-trip mismatch: got=%+v", got)
	}
}

func TestPagedDataJSON(t *testing.T) {
	pd := PagedData{
		Items:      []string{"a", "b"},
		Total:      100,
		Page:       1,
		PageSize:   10,
		TotalPages: 10,
	}
	data, err := json.Marshal(pd)
	if err != nil {
		t.Fatalf("Marshal PagedData: %v", err)
	}
	var got PagedData
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal PagedData: %v", err)
	}
	if got.Total != 100 || got.Page != 1 || got.TotalPages != 10 {
		t.Fatalf("PagedData round-trip mismatch: got=%+v", got)
	}
}

func TestVisibilityConstants(t *testing.T) {
	if VisibilityPublic != "public" {
		t.Fatalf("VisibilityPublic=%q", VisibilityPublic)
	}
	if VisibilitySpace != "space" {
		t.Fatalf("VisibilitySpace=%q", VisibilitySpace)
	}
	if VisibilityPrivate != "private" {
		t.Fatalf("VisibilityPrivate=%q", VisibilityPrivate)
	}
}

func TestParseStatusConstants(t *testing.T) {
	if ParseStatusPending != "pending" {
		t.Fatalf("ParseStatusPending=%q", ParseStatusPending)
	}
	if ParseStatusParsing != "parsing" {
		t.Fatalf("ParseStatusParsing=%q", ParseStatusParsing)
	}
	if ParseStatusSuccess != "success" {
		t.Fatalf("ParseStatusSuccess=%q", ParseStatusSuccess)
	}
	if ParseStatusFailed != "failed" {
		t.Fatalf("ParseStatusFailed=%q", ParseStatusFailed)
	}
}
