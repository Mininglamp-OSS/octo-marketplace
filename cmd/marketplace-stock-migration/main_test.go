package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/backfill/pinbackfill"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/backfill/stockmigration"
)

func TestLegacyUnsafeModesAreUnreachable(t *testing.T) {
	for _, args := range [][]string{
		{"--apply"},
		{"--rollback"},
		{"--all-spaces", "--apply"},
		{"--g4-stop-on-exceptions-approved"},
		{"preview", "--discover", "--apply"},
		{"preview", "--discover", "--rollback"},
		{"preview", "--discover", "--all-spaces"},
		{"apply", "--plan", "p.json", "--plan-digest", strings.Repeat("a", 64), "--authorization", "a.json", "--rollback"},
		{"apply", "--plan", "p.json", "--plan-digest", strings.Repeat("a", 64), "--authorization", "a.json", "--g4-stop-on-exceptions-approved"},
		{"resume", "--run-id", "r", "--authorization", "a.json", "--plan", "p.json"},
	} {
		var out bytes.Buffer
		if err := run(args, &out); err == nil {
			t.Fatalf("unsafe mode accepted: %v", args)
		}
	}
}

func TestPreviewRequiresExactlyOneMode(t *testing.T) {
	for _, args := range [][]string{
		{"preview"},
		{"preview", "--discover", "--freeze"},
		{"preview", "--freeze"},
	} {
		if err := run(args, new(bytes.Buffer)); err == nil {
			t.Fatalf("invalid preview accepted: %v", args)
		}
	}
}

func TestApplyRequiresFrozenPlanDigestAndAuthorization(t *testing.T) {
	for _, args := range [][]string{
		{"apply"},
		{"apply", "--plan", "p.json"},
		{"apply", "--plan", "p.json", "--plan-digest", strings.Repeat("a", 64)},
	} {
		if err := run(args, new(bytes.Buffer)); err == nil {
			t.Fatalf("incomplete apply accepted: %v", args)
		}
	}
}

func TestApplyDigestMismatchIsPlanStaleNotHTTP(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	plan := stockmigration.MigrationPlan{
		SchemaVersion: stockmigration.PlanSchemaVersion, SourceKind: stockmigration.PlanSourceDB,
		SourceIdentity: "p@v", SourcePluginID: "p", SourceVersionID: "v",
		Resources: []stockmigration.MigrationPlanResource{}, Relations: []stockmigration.MigrationPlanRelation{},
	}
	raw, _ := json.Marshal(plan)
	if err := os.WriteFile(planPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run([]string{"apply", "--plan", planPath, "--plan-digest", strings.Repeat("a", 64), "--authorization", filepath.Join(dir, "missing.json")}, &out)
	var reason *reasonError
	if !errors.As(err, &reason) || reason.Code != exitPlanStale || reason.Reason != reasonPlanStale {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(out.String(), reasonPlanStale) || strings.Contains(out.String(), "409") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestApplyMissingTrustConfigurationIsAuthorizationInvalid(t *testing.T) {
	t.Setenv("MARKETPLACE_MIGRATION_TRUST_CONFIG", "")
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	plan := stockmigration.MigrationPlan{
		SchemaVersion: stockmigration.PlanSchemaVersion, SourceKind: stockmigration.PlanSourceDB,
		SourceIdentity: "p@v", SourcePluginID: "p", SourceVersionID: "v",
		Resources: []stockmigration.MigrationPlanResource{}, Relations: []stockmigration.MigrationPlanRelation{},
	}
	raw, _ := json.Marshal(plan)
	if err := os.WriteFile(planPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := stockmigration.PlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(dir, "auth.json")
	if err = os.WriteFile(authPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = run([]string{"apply", "--plan", planPath, "--plan-digest", digest, "--authorization", authPath}, &out)
	var reason *reasonError
	if !errors.As(err, &reason) || reason.Code != exitAuthorization || reason.Reason != reasonAuthorization {
		t.Fatalf("error: %v", err)
	}
}

func TestPreviewFixtureDiscoveryIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture.json")
	original := []byte(`{"resources":[],"versions":[],"releases":[]}`)
	if err := os.WriteFile(fixture, original, 0400); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	// An empty inventory has no migration unit; discovery must fail without
	// creating any output or modifying the operator file.
	if err := run([]string{"preview", "--discover", "--fixture", fixture, "--source-plugin-id", "missing"}, &out); err == nil {
		t.Fatal("empty inventory accepted")
	}
	after, _ := os.ReadFile(fixture)
	if !bytes.Equal(original, after) {
		t.Fatal("preview mutated the fixture")
	}
}

func pinBackfillTestPlan() pinbackfill.Plan {
	return pinbackfill.Plan{
		SchemaVersion: pinbackfill.PlanSchemaVersion, SourceScope: pinbackfill.PlanSourceScope,
		SourceIdentity: pinbackfill.PlanSourceIdentity, BatchID: pinbackfill.PlanBatchID,
		Sources: []pinbackfill.SourcePlan{}, Blocked: []pinbackfill.BlockedSource{},
	}
}

func TestPinBackfillRequiresExplicitModesAndArtifacts(t *testing.T) {
	for _, args := range [][]string{
		{"pin-backfill"},
		{"pin-backfill", "preview"},
		{"pin-backfill", "preview", "--output", ""},
		{"pin-backfill", "apply"},
		{"pin-backfill", "apply", "--plan", "p.json"},
		{"pin-backfill", "apply", "--plan", "p.json", "--plan-digest", strings.Repeat("a", 64)},
		{"pin-backfill", "resume"},
	} {
		if err := run(args, new(bytes.Buffer)); err == nil {
			t.Fatalf("invalid pin-backfill invocation accepted: %v", args)
		}
	}
}

func TestPinBackfillDigestMismatchIsPlanStaleBeforeAuthorization(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	raw, _ := json.Marshal(pinBackfillTestPlan())
	if err := os.WriteFile(planPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run([]string{"pin-backfill", "apply", "--plan", planPath,
		"--plan-digest", strings.Repeat("a", 64), "--authorization", filepath.Join(dir, "missing.json")}, &out)
	var reason *reasonError
	if !errors.As(err, &reason) || reason.Code != exitPlanStale || reason.Reason != reasonPlanStale {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(out.String(), reasonPlanStale) || strings.Contains(out.String(), "409") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestPinBackfillWrapperDigestMismatchIsPlanStale(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	plan := pinBackfillTestPlan()
	digest, err := pinbackfill.Digest(plan)
	if err != nil {
		t.Fatal(err)
	}
	wrapper, _ := json.Marshal(pinBackfillPreviewFile{PlanDigest: digest, Plan: plan})
	if err = os.WriteFile(planPath, wrapper, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = run([]string{"pin-backfill", "apply", "--plan", planPath,
		"--plan-digest", digest, "--authorization", filepath.Join(dir, "missing.json")}, &out)
	var reason *reasonError
	if !errors.As(err, &reason) || reason.Code != exitAuthorization {
		t.Fatalf("error: %v", err)
	}
	// Corrupting the embedded digest must be rejected as a stale plan before the
	// trust configuration is consulted.
	wrapper, _ = json.Marshal(pinBackfillPreviewFile{PlanDigest: strings.Repeat("b", 64), Plan: plan})
	if err = os.WriteFile(planPath, wrapper, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err = run([]string{"pin-backfill", "apply", "--plan", planPath,
		"--plan-digest", digest, "--authorization", filepath.Join(dir, "missing.json")}, &out)
	if !errors.As(err, &reason) || reason.Code != exitPlanStale {
		t.Fatalf("tampered wrapper digest: %v", err)
	}
}

func TestPinBackfillMissingTrustIsAuthorizationInvalid(t *testing.T) {
	t.Setenv("MARKETPLACE_MIGRATION_TRUST_CONFIG", "")
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	plan := pinBackfillTestPlan()
	digest, err := pinbackfill.Digest(plan)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(plan)
	if err = os.WriteFile(planPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = run([]string{"pin-backfill", "apply", "--plan", planPath,
		"--plan-digest", digest, "--authorization", filepath.Join(dir, "missing.json")}, &out)
	var reason *reasonError
	if !errors.As(err, &reason) || reason.Code != exitAuthorization || reason.Reason != reasonAuthorization {
		t.Fatalf("error: %v", err)
	}
}
