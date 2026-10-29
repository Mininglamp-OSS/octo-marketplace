package migration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

func TestVerifySourceObjectsDetectsChangedCapturedBytes(t *testing.T) {
	for _, original := range [][]byte{[]byte("old"), {}} {
		evidence := map[string]SourceEvidence{"source": {Objects: []SourceObject{{Key: "same-key", Hash: digest(original), Size: int64(len(original))}}}}
		current := append([]byte(nil), original...)
		read := func(context.Context, string, int64) ([]byte, error) { return current, nil }
		if err := VerifySourceObjects(context.Background(), evidence, read); err != nil {
			t.Fatal("unchanged captured bytes rejected:", err)
		}
		current = []byte("new")
		if err := VerifySourceObjects(context.Background(), evidence, read); err != errSourceObjects {
			t.Fatalf("changed source accepted: %v", err)
		}
	}
}

func TestVerifySourceObjectsReadsDuplicatesOnceInDeterministicOrder(t *testing.T) {
	ref := func(key string) SourceObject {
		return SourceObject{Key: key, Hash: digest([]byte(key)), Size: int64(len(key))}
	}
	evidence := map[string]SourceEvidence{
		"z": {Objects: []SourceObject{ref("last"), ref("shared")}},
		"a": {Objects: []SourceObject{ref("shared"), ref("first"), ref("shared")}},
	}
	var calls []string
	err := VerifySourceObjects(context.Background(), evidence, func(_ context.Context, key string, _ int64) ([]byte, error) {
		calls = append(calls, key)
		return []byte(key), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"first", "shared", "last"}) {
		t.Fatalf("read order or duplicate read: %v", calls)
	}
}

func TestVerifySourceObjectsFailsClosed(t *testing.T) {
	valid := SourceObject{Key: "secret-key", Hash: digest([]byte("abc")), Size: 3}
	for _, tc := range []struct {
		name string
		refs []SourceObject
	}{
		{"short hash", []SourceObject{{Key: valid.Key, Hash: "abc", Size: 3}}},
		{"invalid hex", []SourceObject{{Key: valid.Key, Hash: strings.Repeat("g", 64), Size: 3}}},
		{"uppercase hash", []SourceObject{{Key: valid.Key, Hash: strings.ToUpper(valid.Hash), Size: 3}}},
		{"negative size", []SourceObject{{Key: valid.Key, Hash: valid.Hash, Size: -1}}},
		{"empty key", []SourceObject{{Hash: valid.Hash, Size: 3}}},
		{"conflicting hash", []SourceObject{valid, {Key: valid.Key, Hash: digest([]byte("xyz")), Size: 3}}},
		{"conflicting size", []SourceObject{valid, {Key: valid.Key, Hash: valid.Hash, Size: 4}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := VerifySourceObjects(context.Background(), map[string]SourceEvidence{"source": {Objects: tc.refs}}, func(context.Context, string, int64) ([]byte, error) {
				calls++
				return []byte("abc"), nil
			})
			if err != errSourceObjects || calls != 0 {
				t.Fatalf("invalid metadata: err=%v reads=%d", err, calls)
			}
		})
	}
	evidence := map[string]SourceEvidence{"source": {Objects: []SourceObject{valid}}}
	if err := VerifySourceObjects(context.Background(), evidence, nil); err != errSourceObjects {
		t.Fatalf("nil reader accepted: %v", err)
	}
	for _, cause := range []error{errors.New("missing secret-key"), io.ErrUnexpectedEOF} {
		err := VerifySourceObjects(context.Background(), evidence, func(context.Context, string, int64) ([]byte, error) { return nil, cause })
		if err != errSourceObjects || strings.Contains(err.Error(), "secret-key") {
			t.Fatalf("read failure not sanitized: %v", err)
		}
	}
	if err := VerifySourceObjects(context.Background(), nil, nil); err != nil {
		t.Fatal("no objects requires reader:", err)
	}
	if err := VerifySourceObjects(context.Background(), map[string]SourceEvidence{"source": {}}, nil); err != nil {
		t.Fatal("empty object evidence requires reader:", err)
	}
}

func objectPlan(t *testing.T) Plan {
	t.Helper()
	r := resource("a", "binary", "space-id", "body")
	r.Files = append(r.Files, File{Path: "image.bin", MIME: "application/octet-stream", Data: []byte{0, 255}})
	p, e := Build(Input{SnapshotID: "s", Resources: []Resource{r}}, Policy{})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestPublishObjectsImmutableAndVerifyReadOnly(t *testing.T) {
	ctx := context.Background()
	p := objectPlan(t)
	local := storage.NewLocal(t.TempDir(), "http://local")
	if e := VerifyObjects(ctx, p, local); e == nil {
		t.Fatal("missing object verified")
	}
	if e := PublishObjects(ctx, p, local); e != nil {
		t.Fatal(e)
	}
	if e := PublishObjects(ctx, p, local); e != nil {
		t.Fatal("identical resume:", e)
	}
	if e := VerifyObjects(ctx, p, local); e != nil {
		t.Fatal(e)
	}
	key := p.Nodes[0].Files[1].ObjectKey
	if e := local.DeleteObject(ctx, key); e != storage.ErrImmutableObject {
		t.Fatal("object is mutable:", e)
	}
}
func TestPublishObjectsRejectsInvalidPlanBeforeAnyWrite(t *testing.T) {
	for _, mutate := range []struct {
		name string
		f    func(*Plan)
	}{
		{"foreign scope", func(p *Plan) {
			p.Nodes[0].Files[1].ObjectKey = "plugins/foreign/attachments/versions/" + digest(p.Nodes[0].Files[1].Data)
		}},
		{"wrong digest", func(p *Plan) { p.Nodes[0].Files[1].Data = []byte("changed") }},
		{"missing artifacts", func(p *Plan) { p.Nodes[0].Artifacts = nil }},
		{"missing file object key", func(p *Plan) { p.Nodes[0].Files[1].ObjectKey = "" }},
		{"wrong artifact digest", func(p *Plan) {
			var a []map[string]any
			json.Unmarshal(p.Nodes[0].Artifacts, &a)
			a[1]["digest"] = "sha256:wrong"
			p.Nodes[0].Artifacts, _ = json.Marshal(a)
		}},
		{"wrong attachment key", func(p *Plan) { p.Nodes[0].Plugin.AttachmentKeys = json.RawMessage(`{"image.bin":"foreign"}`) }},
		{"wrong package digest", func(p *Plan) {
			p.Nodes[0].Plugin.Package = json.RawMessage(strings.ReplaceAll(string(p.Nodes[0].Plugin.Package), "sha256:", "bad:"))
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			p := objectPlan(t)
			mutate.f(&p)
			s := &objectProbe{Storage: storage.NewLocal(t.TempDir(), "http://local")}
			if e := PublishObjects(context.Background(), p, s); e == nil {
				t.Fatal("invalid object accepted")
			}
			if s.writes != 0 {
				t.Fatal("wrote before validation")
			}
		})
	}
}

type objectProbe struct {
	storage.Storage
	writes   int
	corrupt  bool
	existing bool
}

func (s *objectProbe) PutImmutable(ctx context.Context, key string, r io.Reader, size int64, mime string) (bool, error) {
	s.writes++
	if s.corrupt || s.existing {
		return !s.existing, nil
	}
	return s.Storage.(storage.ImmutableWriter).PutImmutable(ctx, key, r, size, mime)
}
func (s *objectProbe) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	if s.corrupt || s.existing {
		return io.NopCloser(strings.NewReader("wrong")), nil
	}
	return s.Storage.GetObject(ctx, key)
}
func TestPublishObjectsAlwaysChecksWrittenAndExistingContent(t *testing.T) {
	for _, existing := range []bool{false, true} {
		s := &objectProbe{Storage: storage.NewLocal(t.TempDir(), "http://local"), corrupt: true, existing: existing}
		p := objectPlan(t)
		if e := PublishObjects(context.Background(), p, s); e == nil {
			t.Fatal("corrupt object accepted")
		}
		if s.writes != 1 {
			t.Fatal("write not attempted")
		}
	}
}
func TestVerifyObjectsNeverWrites(t *testing.T) {
	s := &objectProbe{Storage: storage.NewLocal(t.TempDir(), "http://local"), corrupt: true}
	if e := VerifyObjects(context.Background(), objectPlan(t), s); e == nil {
		t.Fatal("corruption accepted")
	}
	if s.writes != 0 {
		t.Fatal("verify wrote")
	}
}
func TestPublishObjectsRequiresAtomicWriter(t *testing.T) {
	local := storage.NewLocal(t.TempDir(), "http://local")
	s := struct{ storage.Storage }{local}
	if e := PublishObjects(context.Background(), objectPlan(t), s); e == nil {
		t.Fatal("non-atomic writer accepted")
	}
}

type interruptedObjectWriter struct {
	storage.Storage
	attempts int
	created  int
	failAt   int
}

func (s *interruptedObjectWriter) PutImmutable(ctx context.Context, key string, r io.Reader, size int64, mime string) (bool, error) {
	s.attempts++
	if s.attempts == s.failAt {
		return false, errors.New("injected interruption")
	}
	created, err := s.Storage.(storage.ImmutableWriter).PutImmutable(ctx, key, r, size, mime)
	if created {
		s.created++
	}
	return created, err
}

func TestPublishObjectsResumesAfterPartialWriteWithoutReplacingObjects(t *testing.T) {
	r := resource("a", "binary", "space-id", "body")
	r.Files = append(r.Files, File{Path: "image.bin", MIME: "application/octet-stream", Data: []byte{0, 255}}, File{Path: "second.bin", MIME: "application/octet-stream", Data: []byte{1, 2, 3}})
	p, err := Build(Input{SnapshotID: "s", Resources: []Resource{r}}, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	s := &interruptedObjectWriter{Storage: storage.NewLocal(t.TempDir(), "http://local"), failAt: 2}
	ctx := context.Background()
	if err := PublishObjects(ctx, p, s); err == nil {
		t.Fatal("injected failure accepted")
	}
	if s.created != 1 {
		t.Fatalf("created %d objects before interruption", s.created)
	}
	entries, err := InspectPlanObjects(ctx, p, s)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]int{}
	for _, entry := range entries {
		statuses[entry.Status]++
	}
	if len(entries) != 2 || statuses["verified"] != 1 || statuses["missing"] != 1 || s.attempts != 2 {
		t.Fatalf("partial publication inventory=%+v attempts=%d", entries, s.attempts)
	}
	if entries[0].Key >= entries[1].Key {
		t.Fatalf("inventory keys are not sorted: %+v", entries)
	}
	if err := VerifyObjects(ctx, p, s); err == nil {
		t.Fatal("incomplete plan verified")
	}
	if err := PublishObjects(ctx, p, s); err != nil {
		t.Fatal(err)
	}
	if s.created != 2 {
		t.Fatalf("retry replaced or duplicated an object: %d", s.created)
	}
	if err := VerifyObjects(ctx, p, s); err != nil {
		t.Fatal(err)
	}
	entries, err = InspectPlanObjects(ctx, p, s)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Status != "verified" {
			t.Fatalf("resumed inventory: %+v", entries)
		}
	}
}

type inventoryObjectProbe struct {
	storage.Storage
	statErr, readErr error
	corrupt          bool
	stats, reads     []string
}

func (s *inventoryObjectProbe) StatObject(ctx context.Context, key string) (storage.ObjectInfo, error) {
	s.stats = append(s.stats, key)
	if s.statErr != nil {
		return storage.ObjectInfo{}, s.statErr
	}
	return s.Storage.StatObject(ctx, key)
}

func (s *inventoryObjectProbe) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	s.reads = append(s.reads, key)
	if s.readErr != nil {
		return nil, s.readErr
	}
	if s.corrupt {
		return io.NopCloser(strings.NewReader("wrong")), nil
	}
	return s.Storage.GetObject(ctx, key)
}

func TestInspectPlanObjectsSanitizesFailuresWithoutCallingThemMissing(t *testing.T) {
	p := objectPlan(t)
	local := storage.NewLocal(t.TempDir(), "http://local")
	if err := PublishObjects(context.Background(), p, local); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		probe *inventoryObjectProbe
	}{
		{"stat failure", &inventoryObjectProbe{Storage: local, statErr: errors.New("secret connection failure")}},
		{"read failure", &inventoryObjectProbe{Storage: local, readErr: errors.New("secret read failure")}},
		{"disappeared after stat", &inventoryObjectProbe{Storage: local, readErr: storage.ErrObjectNotFound}},
		{"corruption", &inventoryObjectProbe{Storage: local, corrupt: true}},
		{"nil store", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var store storage.Storage
			if tc.probe != nil {
				store = tc.probe
			}
			entries, err := InspectPlanObjects(context.Background(), p, store)
			if err != nil || len(entries) != 1 || entries[0].Status != "verification_failed" {
				t.Fatalf("inventory=%+v err=%v", entries, err)
			}
			encoded, err := json.Marshal(entries)
			if err != nil || strings.Contains(string(encoded), "secret") {
				t.Fatalf("unsafe inventory: %s err=%v", encoded, err)
			}
		})
	}
}

func TestInspectPlanObjectsValidatesBeforeReadsAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	p := objectPlan(t)
	local := storage.NewLocal(t.TempDir(), "http://local")
	probe := &inventoryObjectProbe{Storage: local}
	bad := objectPlan(t)
	bad.Nodes[0].Files[1].Data = []byte("changed")
	if _, err := InspectPlanObjects(ctx, bad, probe); err != errObjects || len(probe.stats) != 0 || len(probe.reads) != 0 {
		t.Fatalf("invalid plan read storage: err=%v stats=%v reads=%v", err, probe.stats, probe.reads)
	}
	if err := PublishObjects(ctx, p, local); err != nil {
		t.Fatal(err)
	}
	p.Nodes = append(p.Nodes, p.Nodes[0])
	entries, err := InspectPlanObjects(ctx, p, probe)
	f := p.Nodes[0].Files[1]
	want := []ObjectInventoryEntry{{Key: f.ObjectKey, Hash: digest(f.Data), Size: int64(len(f.Data)), Status: "verified"}}
	if err != nil || !reflect.DeepEqual(entries, want) || len(probe.stats) != 1 || len(probe.reads) != 1 {
		t.Fatalf("duplicate inventory=%+v err=%v stats=%v reads=%v", entries, err, probe.stats, probe.reads)
	}
	entries, err = InspectPlanObjects(ctx, Plan{}, nil)
	if err != nil || entries == nil || len(entries) != 0 {
		t.Fatalf("empty inventory=%+v err=%v", entries, err)
	}
}
