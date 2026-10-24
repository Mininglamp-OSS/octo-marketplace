package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/inventory"
)

func TestFixtureEndToEndIsReadOnly(t *testing.T) {
	// An unusable DSN verifies fixture mode does not open a database connection.
	t.Setenv("MARKETPLACE_INVENTORY_DSN", "invalid database configuration")
	original, err := os.ReadFile("../../internal/inventory/testdata/legacy.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(fixture, original, 0400); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{"--fixture", fixture}, &output); err != nil {
		t.Fatal(err)
	}
	var report inventory.Report
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Resources) != 3 || report.Counts["listed_publication_unknown"] != 2 || report.ObjectVerification != "not_checked" {
		t.Fatalf("unexpected report: %+v", report)
	}
	wantDistribution := []inventory.Distribution{
		{Visibility: "private", Listing: "draft", Identity: "top_level", Count: 1},
		{Visibility: "private", Listing: "published", Identity: "embedded", Count: 1},
		{Visibility: "private", Listing: "published", Identity: "top_level", Count: 1},
	}
	if !reflect.DeepEqual(report.Distribution, wantDistribution) {
		t.Fatalf("distribution: %+v", report.Distribution)
	}
	after, err := os.ReadFile(fixture)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("fixture changed")
	}
	var again bytes.Buffer
	if err := run([]string{"--fixture", fixture}, &again); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), again.Bytes()) {
		t.Fatal("report is not deterministic")
	}
}

func TestInvalidFixtureProducesNoPartialReport(t *testing.T) {
	for _, body := range []string{`{"plugins":[],"unknown":"secret"}`, `{} {}`, `{"plugins":`} {
		t.Run(body, func(t *testing.T) {
			fixture := filepath.Join(t.TempDir(), "input.json")
			if err := os.WriteFile(fixture, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := run([]string{"--fixture", fixture}, &output); err == nil || output.Len() != 0 {
				t.Fatal("invalid input produced report")
			}
		})
	}
}

func TestDatabaseModeRequiresOperatorAcknowledgment(t *testing.T) {
	var output bytes.Buffer
	if err := run(nil, &output); err == nil || output.Len() != 0 {
		t.Fatal("database mode must be explicit")
	}
}
