package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// capacityWireBytes is the client-facing contract: 20 MB spelled in decimal
// bytes, not 20 MiB. It is written as a literal so a change to the unit or the
// radius of the implementation constant fails these tests.
const capacityWireBytes = 20_000_000

type capacityDiagnostic interface {
	error
	CapacityDetails() map[string]any
}

func requireCapacityDiagnostic(t *testing.T, err error) map[string]any {
	t.Helper()
	var diagnostic capacityDiagnostic
	if !errors.As(err, &diagnostic) {
		t.Fatalf("error %v has no capacity diagnostics", err)
	}
	return diagnostic.CapacityDetails()
}

// storageEntry describes one attachment whose bytes live in object storage, so
// the package JSON stays metadata-sized just as it does in production.
func storageEntry(path, key, content string) string {
	return fmt.Sprintf(`{"path":%q,"content_type":"storage","storage_uri":%q,"content_size":%d}`, path, key, len(content))
}

// skillTextInstall wires the fixture's single Skill to a storage-backed
// SKILL.md and returns the captured Fleet call.
func skillTextInstall(t *testing.T, content string) (*Service, *fakeCapabilityInstaller) {
	t.Helper()
	svc, store, installer := capabilityFixture()
	key := "plugins/space-a/attachments/skill-entry.md"
	svc.storage = &importStorage{objects: map[string][]byte{key: []byte(content)}}
	store.plugins["skill-1"].Package = packageWith(rawAtt("skill/ref.json", fmt.Sprintf(`{"object_key":%q}`, key)))
	return svc, installer
}

func wireRequestBytes(t *testing.T, installer *fakeCapabilityInstaller) int {
	t.Helper()
	body, err := json.Marshal(installer.in)
	if err != nil {
		t.Fatal(err)
	}
	return len(body)
}

// A small configured attachment/archive limit is a transport policy for stored
// archives. It must never become the installation content budget.
func TestCapabilityInstallBudgetDoesNotShrinkWithConfiguredArchiveLimit(t *testing.T) {
	svc, store, installer := capabilityFixture()
	svc.maxArchiveBytes = 512
	content := strings.Repeat("c", 4096)
	store.plugins["skill-1"].Package = packageWith(rawAtt("SKILL.md", content))
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err != nil {
		t.Fatalf("configured archive limit reduced the install content budget: %v", err)
	}
	if installer.calls != 1 || installer.in.Definition.Skills[0].Content != content {
		t.Fatal("skill text was dropped or truncated")
	}
}

// One text asset larger than the retired 1 MiB per-text cap must still be sent
// byte-for-byte; only the aggregate wire capacity may reject it.
func TestCapabilityInstallKeepsSupportingFileAboveLegacyOneMiBLimit(t *testing.T) {
	svc, store, installer := capabilityFixture()
	key := "plugins/space-a/attachments/reference.md"
	content := strings.Repeat("r", (1<<20)+512)
	svc.storage = &importStorage{objects: map[string][]byte{key: []byte(content)}}
	store.plugins["skill-1"].Package = packageWith(rawAtt("SKILL.md", "# Deploy"), storageEntry("references/large.md", key, content))
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err != nil {
		t.Fatalf("supporting file above the retired 1 MiB cap was rejected: %v", err)
	}
	skill := installer.in.Definition.Skills[0]
	if len(skill.Files) != 1 || skill.Files[0].Path != "references/large.md" || skill.Files[0].Source.Content != content {
		t.Fatalf("large supporting file was dropped or altered: %d files", len(skill.Files))
	}
}

// The retired 50-per-Skill / 500-per-installation caps must not silently drop
// attachments: every valid path is materialized in the Fleet definition.
func TestCapabilityInstallKeepsAllSupportingFilesBeyondLegacyCaps(t *testing.T) {
	svc, store, installer := capabilityFixture()
	attachments := []string{rawAtt("SKILL.md", "# Deploy")}
	want := map[string]string{}
	for i := range 60 {
		path := fmt.Sprintf("references/section-%03d.md", i)
		body := fmt.Sprintf("section %03d", i)
		want[path] = body
		attachments = append(attachments, rawAtt(path, body))
	}
	store.plugins["skill-1"].Package = packageWith(attachments...)
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err != nil {
		t.Fatalf("supporting files above the retired per-Skill cap were rejected: %v", err)
	}
	skill := installer.in.Definition.Skills[0]
	if len(skill.Files) != len(want) {
		t.Fatalf("files=%d want=%d", len(skill.Files), len(want))
	}
	for _, file := range skill.Files {
		if want[file.Path] != file.Source.Content {
			t.Fatalf("path %q content changed", file.Path)
		}
	}
}

// The per-installation ceiling used to cap the combined supporting files from
// all referenced Skills. Two Skills with 251 valid files each must be complete.
func TestCapabilityInstallKeepsAllSupportingFilesBeyondLegacyAggregateCap(t *testing.T) {
	svc, store, installer := capabilityFixture()
	buildPackage := func(skill string) json.RawMessage {
		attachments := []string{rawAtt("SKILL.md", "# "+skill)}
		for i := range 251 {
			path := fmt.Sprintf("references/section-%03d.md", i)
			attachments = append(attachments, rawAtt(path, fmt.Sprintf("%s file %03d", skill, i)))
		}
		return packageWith(attachments...)
	}
	store.plugins["skill-1"].Package = buildPackage("Deploy")
	second := *store.plugins["skill-1"]
	second.ID, second.Name = "skill-2", "Reference"
	second.Package = buildPackage("Reference")
	store.plugins[second.ID] = &second
	store.relations["expert-1"] = append(store.relations["expert-1"], model.PluginRelation{
		ID: "r-skill-2", SourcePluginID: "expert-1", TargetPluginID: "skill-2",
		TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", Status: 1, SortOrder: 1,
	})

	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err != nil {
		t.Fatalf("supporting files above the retired per-install cap were rejected: %v", err)
	}
	if len(installer.in.Definition.Skills) != 2 {
		t.Fatalf("installed skills=%d want=2", len(installer.in.Definition.Skills))
	}
	total := 0
	for _, skill := range installer.in.Definition.Skills {
		if skill.MarketSkillID != "skill-1" && skill.MarketSkillID != "skill-2" {
			t.Fatalf("unexpected Skill identity %q", skill.MarketSkillID)
		}
		if len(skill.Files) != 251 {
			t.Fatalf("skill %s files=%d want=251", skill.MarketSkillID, len(skill.Files))
		}
		for i, file := range skill.Files {
			if file.Source.Content != fmt.Sprintf("%s file %03d", map[string]string{"skill-1": "Deploy", "skill-2": "Reference"}[skill.MarketSkillID], i) {
				t.Fatalf("skill %s file %q content changed", skill.MarketSkillID, file.Path)
			}
		}
		total += len(skill.Files)
	}
	if total != 502 {
		t.Fatalf("supporting files=%d want=502", total)
	}
}

// ZIP archives are the legacy source for published Skills: their entry count and
// per-entry size must follow the same content capacity, not the retired 51-entry
// and 1 MiB limits. Paths stay rooted at SKILL.md and nothing is dropped.
func TestCapabilityLegacyArchiveKeepsEveryValidEntry(t *testing.T) {
	files := map[string][]byte{"pkg/SKILL.md": []byte("# Real skill")}
	want := map[string]string{}
	for i := range 70 {
		path := fmt.Sprintf("pkg/references/deep/section-%03d.md", i)
		body := fmt.Sprintf("note %03d", i)
		files[path] = []byte(body)
		want[fmt.Sprintf("references/deep/section-%03d.md", i)] = body
	}
	svc, installer := capabilityArchiveFixture(zipWith(t, files))
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err != nil {
		t.Fatalf("archive above the retired entry cap was rejected: %v", err)
	}
	skill := installer.in.Definition.Skills[0]
	if len(skill.Files) != len(want) {
		t.Fatalf("files=%d want=%d", len(skill.Files), len(want))
	}
	for _, file := range skill.Files {
		if want[file.Path] != file.Source.Content {
			t.Fatalf("path %q content changed or was truncated", file.Path)
		}
	}
}

func TestCapabilityLegacyArchiveKeepsEntryAboveLegacyOneMiBLimit(t *testing.T) {
	content := strings.Repeat("b", (1<<20)+512)
	svc, installer := capabilityArchiveFixture(zipWith(t, map[string][]byte{
		"SKILL.md":          []byte("# Real skill"),
		"references/big.md": []byte(content),
	}))
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err != nil {
		t.Fatalf("archive entry above the retired 1 MiB cap was rejected: %v", err)
	}
	skill := installer.in.Definition.Skills[0]
	if len(skill.Files) != 1 || skill.Files[0].Path != "references/big.md" || skill.Files[0].Source.Content != content {
		t.Fatalf("archive entry was dropped or truncated: %d files", len(skill.Files))
	}
}

// ZIP framing and stored (uncompressed) entries can make the archive object one
// byte larger than the text it carries. Transport bytes are not content bytes:
// the archive limit bounds the download, the content budget bounds the extracted
// text.
func TestCapabilityLegacyArchiveTransportBytesAreNotContentBudget(t *testing.T) {
	const budget = 4096
	archive := storedZip(t, [][2]string{
		{"SKILL.md", "x"},
		{"references/ref.md", strings.Repeat("y", 4000)},
	})
	if len(archive) <= budget {
		t.Fatalf("fixture archive=%d must exceed the %d-byte content budget", len(archive), budget)
	}
	svc, store, _ := capabilityFixture()
	key := "plugins/space-a/attachments/pack.zip"
	svc.storage = &importStorage{objects: map[string][]byte{key: archive}}
	store.plugins["skill-1"].Package = packageWith(rawAtt("SKILL.md", "stub"), rawAtt("skill/ref.json", fmt.Sprintf(`{"zip_object_key":%q}`, key)))
	builder := capabilityBuilder{service: svc, caller: testCaller, remaining: budget}
	files, err := builder.skillFiles(context.Background(), store.plugins["skill-1"])
	if err != nil {
		t.Fatalf("stored archive larger than the remaining text budget was rejected: %v", err)
	}
	if len(files) != 2 || len(files["references/ref.md"]) != 4000 || files["SKILL.md"] != "x" {
		t.Fatal("archive contents were not materialized")
	}
}

// The assembled Fleet JSON — definition, bindings, metadata, escaping and
// framing — is the capacity unit. Exactly 20,000,000 bytes is deliverable and
// one byte more is rejected before Fleet with an actionable error.
func TestCapabilityInstallWireLimitIsDecimalTwentyMegabytes(t *testing.T) {
	calibration, installer := skillTextInstall(t, strings.Repeat("x", 1000))
	if _, err := calibration.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err != nil {
		t.Fatalf("calibration install failed: %v", err)
	}
	overhead := wireRequestBytes(t, installer) - 1000
	if overhead <= 0 {
		t.Fatalf("unexpected request overhead %d", overhead)
	}
	content := strings.Repeat("x", capacityWireBytes-overhead)

	svc, installer := skillTextInstall(t, content)
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err != nil {
		t.Fatalf("exactly %d wire bytes were rejected: %v", capacityWireBytes, err)
	}
	if got := wireRequestBytes(t, installer); got != capacityWireBytes {
		t.Fatalf("wire bytes=%d want=%d", got, capacityWireBytes)
	}
	if installer.in.Definition.Skills[0].Content != content {
		t.Fatal("skill content was truncated inside the capacity boundary")
	}

	svc, installer = skillTextInstall(t, content+"x")
	_, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams())
	diagnostics := requireCapacityDiagnostic(t, err)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("one byte over the limit: err=%v", err)
	}
	if installer.calls != 0 {
		t.Fatal("oversized request reached Fleet")
	}
	if diagnostics["field"] != "definition" ||
		diagnostics["limit_bytes"] != int64(capacityWireBytes) ||
		diagnostics["actual_bytes"] != int64(capacityWireBytes+1) {
		t.Fatalf("capacity diagnostics=%v", diagnostics)
	}
	message := err.Error()
	for _, want := range []string{"20000000", "20000001", "definition"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error message %q missing %q", message, want)
		}
	}
}

// The capacity unit is serialized JSON, so a raw text total below the limit can
// still exceed it through escaping. The reported actual size must be the wire
// size, not the unescaped content length.
func TestCapabilityInstallWireLimitCountsJSONEscaping(t *testing.T) {
	calibration, installer := skillTextInstall(t, strings.Repeat("x", 1000))
	if _, err := calibration.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err != nil {
		t.Fatalf("calibration install failed: %v", err)
	}
	overhead := wireRequestBytes(t, installer) - 1000
	// Each quote doubles on the wire, so the escaping alone crosses the limit
	// while the raw text stays below it.
	count := (capacityWireBytes-overhead)/2 + 1
	content := strings.Repeat(`"`, count)
	if len(content) >= capacityWireBytes {
		t.Fatalf("fixture content must stay below the raw budget: %d", len(content))
	}
	svc, installer := skillTextInstall(t, content)
	_, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams())
	diagnostics := requireCapacityDiagnostic(t, err)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("escaped body above the limit was accepted: err=%v", err)
	}
	if installer.calls != 0 {
		t.Fatal("oversized escaped request reached Fleet")
	}
	wantActual := int64(overhead + 2*count)
	actual, ok := diagnostics["actual_bytes"].(int64)
	if !ok || actual != wantActual || actual <= int64(capacityWireBytes) {
		t.Fatalf("actual=%v want=%d", diagnostics["actual_bytes"], wantActual)
	}
	if diagnostics["field"] != "definition" || diagnostics["limit_bytes"] != int64(capacityWireBytes) {
		t.Fatalf("capacity diagnostics=%v", diagnostics)
	}
}

// A final wire-size rejection identifies an involved supporting file and reports
// the exact serialized byte count without exposing any file content.
func TestCapabilityInstallReportsSkillFileLocationWhenBudgetExhausted(t *testing.T) {
	const entryKey = "plugins/space-a/attachments/entry.md"
	note := strings.Repeat("n", 200)
	build := func(entry string) (*Service, *fakeCapabilityInstaller) {
		svc, store, installer := capabilityFixture()
		svc.storage = &importStorage{objects: map[string][]byte{entryKey: []byte(entry)}}
		store.plugins["skill-1"].Package = packageWith(
			rawAtt("skill/ref.json", fmt.Sprintf(`{"object_key":%q}`, entryKey)),
			rawAtt("references/note.md", note),
		)
		return svc, installer
	}
	calibration, calibrationInstaller := build(strings.Repeat("x", 1000))
	if _, err := calibration.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err != nil {
		t.Fatalf("calibration install failed: %v", err)
	}
	overhead := wireRequestBytes(t, calibrationInstaller) - 1000 - len(note)
	if overhead <= 0 {
		t.Fatalf("unexpected request overhead %d", overhead)
	}

	entry := strings.Repeat("x", capacityWireBytes-overhead-len(note)+1)
	svc, installer := build(entry)
	_, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams())
	diagnostics := requireCapacityDiagnostic(t, err)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("budget exhaustion err=%v", err)
	}
	var attempt *InstallationAttemptError
	if errors.As(err, &attempt) {
		t.Fatal("pre-dispatch capacity rejection was classified as an attempted installation")
	}
	if installer.calls != 0 {
		t.Fatal("over-budget assets reached Fleet")
	}
	if diagnostics["field"] != "definition" || diagnostics["skill"] != "skill-1" || diagnostics["path"] != "references/note.md" {
		t.Fatalf("capacity error location=%v", diagnostics)
	}
	if diagnostics["limit_bytes"] != int64(capacityWireBytes) ||
		diagnostics["actual_bytes"] != int64(capacityWireBytes+1) ||
		diagnostics["actual_bytes_lower_bound"] == true {
		t.Fatalf("capacity error bytes=%v", diagnostics)
	}
	message := err.Error()
	for _, want := range []string{"20000000", "20000001", "skill-1", "references/note.md"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error message %q missing %q", message, want)
		}
	}
	if strings.Contains(message, strings.Repeat("x", 64)) || strings.Contains(message, strings.Repeat("n", 64)) {
		t.Fatal("error message leaks file content")
	}
}

// Unknown-size objects are read through a limit+1 boundary. The error reports
// that lower bound and identifies the exact attachment without exposing bytes.
func TestCapabilityInstallReportsLowerBoundForOversizedUnknownObject(t *testing.T) {
	svc, store, installer := capabilityFixture()
	key := "plugins/space-a/attachments/large.md"
	content := strings.Repeat("n", capacityWireBytes)
	svc.storage = &importStorage{objects: map[string][]byte{key: []byte(content)}}
	store.plugins["skill-1"].Package = packageWith(
		rawAtt("SKILL.md", "x"),
		fmt.Sprintf(`{"path":"references/large.md","content_type":"storage","storage_uri":%q}`, key),
	)
	_, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams())
	diagnostics := requireCapacityDiagnostic(t, err)
	if !errors.Is(err, ErrTooLarge) || installer.calls != 0 {
		t.Fatalf("oversized object err=%v Fleet calls=%d", err, installer.calls)
	}
	if diagnostics["field"] != "definition.skills.files" ||
		diagnostics["skill"] != "skill-1" ||
		diagnostics["path"] != "references/large.md" ||
		diagnostics["limit_bytes"] != int64(capacityWireBytes) ||
		diagnostics["actual_bytes"] != int64(capacityWireBytes+1) ||
		diagnostics["actual_bytes_lower_bound"] != true {
		t.Fatalf("capacity diagnostics=%v", diagnostics)
	}
	for _, want := range []string{"20000000", "20000001", "skill-1", "references/large.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error message %q missing %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), strings.Repeat("n", 64)) {
		t.Fatal("error message leaks object content")
	}
}

// storedZip writes every entry with zip.Store so the archive object is larger
// than the bytes it carries, exercising the transport/content distinction.
func storedZip(t *testing.T, entries [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: entry[0], Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entry[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
