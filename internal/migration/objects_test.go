package migration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

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
}
