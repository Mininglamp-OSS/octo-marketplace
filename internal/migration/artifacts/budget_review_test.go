package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func stored(name, key string, body []byte) map[string]any {
	h := sha256.Sum256(body)
	return map[string]any{"path": name, "content_type": "storage", "storage_uri": key, "content_size": len(body), "content_hash": fmt.Sprintf("sha256:%x", h)}
}

func TestStorageAssertionsRequireDeclaredIntegrityBeforeRead(t *testing.T) {
	for _, field := range []string{"content_size", "content_hash"} {
		for _, value := range []any{nil, "bad", -1} {
			t.Run(fmt.Sprintf("%s/%v", field, value), func(t *testing.T) {
				a := stored("SKILL.md", "skills/x/SKILL.md", []byte("body"))
				if value == nil {
					delete(a, field)
				} else {
					a[field] = value
				}
				p := model.Plugin{ID: "x", Type: model.PluginTypeSkill, Package: pkg(a)}
				calls := 0
				_, err := Resolve(context.Background(), &p, func(context.Context, string, int64) ([]byte, error) { calls++; return []byte("body"), nil }, DefaultLimits())
				if err == nil || calls != 0 {
					t.Fatalf("invalid metadata accepted or read: err=%v calls=%d", err, calls)
				}
			})
		}
	}
	a := stored("SKILL.md", "skills/x/SKILL.md", []byte("body"))
	a["content_hash"] = fmt.Sprintf("%x", sha256.Sum256([]byte("body")))
	p := model.Plugin{ID: "x", Type: model.PluginTypeSkill, Package: pkg(a)}
	if _, err := Resolve(context.Background(), &p, func(context.Context, string, int64) ([]byte, error) { return []byte("body"), nil }, DefaultLimits()); err == nil {
		t.Fatal("noncanonical digest accepted")
	}
}

func TestResolveReadsEachKeyOnceWithinOneBudget(t *testing.T) {
	body := []byte("body")
	p := model.Plugin{ID: "x", Type: model.PluginTypeSkill, Package: pkg(stored("SKILL.md", "skills/x/body", body), stored("copy.md", "skills/x/body", body))}
	calls := 0
	limits := DefaultLimits()
	limits.MaxTotalBytes = 8
	files, err := Resolve(context.Background(), &p, func(context.Context, string, int64) ([]byte, error) { calls++; return body, nil }, limits)
	if err != nil || len(files) != 2 || calls != 1 {
		t.Fatalf("files=%d calls=%d err=%v", len(files), calls, err)
	}
}

func TestResolveStopsReadingBeforeAggregateBudgetExceeded(t *testing.T) {
	p := model.Plugin{ID: "x", Type: model.PluginTypeSkill, Package: pkg(stored("SKILL.md", "skills/x/a", []byte("aaaa")), stored("b.md", "skills/x/b", []byte("aaaa")), stored("c.md", "skills/x/c", []byte("aaaa")))}
	calls := 0
	limits := DefaultLimits()
	limits.MaxFileBytes = 4
	limits.MaxTotalBytes = 7
	_, err := Resolve(context.Background(), &p, func(context.Context, string, int64) ([]byte, error) { calls++; return []byte("aaaa"), nil }, limits)
	// A package that cannot fit must be rejected before fetching another object.
	if err == nil || calls > 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

// Count transport bytes rather than just checking the returned slice size.
type countedBody struct {
	io.Reader
	bytes  int
	closed bool
}

func (r *countedBody) Read(p []byte) (int, error) {
	n, e := r.Reader.Read(p)
	r.bytes += n
	return n, e
}
func (r *countedBody) Close() error { r.closed = true; return nil }
func TestBoundedReaderStopsBeforeFullObjectAllocation(t *testing.T) {
	body := &countedBody{Reader: bytes.NewReader(bytes.Repeat([]byte("x"), 1000))}
	read := NewReader(func(context.Context, string) (io.ReadCloser, error) { return body, nil })
	b, err := read(context.Background(), "skills/x/body", 4)
	if err == nil || b != nil || body.bytes != 5 || !body.closed {
		t.Fatalf("err=%v bytes=%d closed=%v", err, body.bytes, body.closed)
	}
}
func TestResolveBoundsStandaloneTransportByRemainingBudget(t *testing.T) {
	p := model.Plugin{ID: "x", Type: model.PluginTypeSkill, Package: pkg(raw("SKILL.md", "stub"), raw("skill/ref.json", `{"object_key":"skills/x/SKILL.md"}`))}
	body := &countedBody{Reader: bytes.NewReader(bytes.Repeat([]byte("x"), 1000))}
	read := NewReader(func(context.Context, string) (io.ReadCloser, error) { return body, nil })
	limits := DefaultLimits()
	limits.MaxFileBytes = 20
	limits.MaxTotalBytes = 7
	_, err := Resolve(context.Background(), &p, read, limits)
	if err == nil || body.bytes != 8 || !body.closed {
		t.Fatalf("err=%v bytes=%d closed=%v", err, body.bytes, body.closed)
	}
}

func TestResolveIncludesManagedIconInReadBudget(t *testing.T) {
	p := model.Plugin{ID: "x", Type: model.PluginTypeSkill, Icon: "icons/upload/logo.png", Package: pkg(raw("SKILL.md", "body"))}
	calls := 0
	_, err := Resolve(context.Background(), &p, func(_ context.Context, key string, _ int64) ([]byte, error) {
		calls++
		if key != p.Icon {
			t.Fatal("unexpected read")
		}
		return []byte("icon"), nil
	}, DefaultLimits())
	if err != nil || calls != 1 {
		t.Fatalf("icon not verified: err=%v calls=%d", err, calls)
	}
	limits := DefaultLimits()
	limits.MaxTotalBytes = 3
	if _, err := Resolve(context.Background(), &p, func(context.Context, string, int64) ([]byte, error) { return []byte("icon"), nil }, limits); err == nil {
		t.Fatal("oversized icon accepted")
	}
}

func TestAuthoritativeMarkdownSkipsSupersededStorageCarrier(t *testing.T) {
	for _, kind := range []model.PluginType{model.PluginTypeSkill, model.PluginTypeExpert, model.PluginTypeExpertTeam, model.PluginTypeConnector} {
		t.Run(string(kind), func(t *testing.T) {
			space := "a"
			a := stored("SKILL.md", "plugins/a/attachments/stale", []byte("stale"))
			atts := []map[string]any{a, raw("skill/ref.json", `{"object_key":"plugins/a/attachments/real"}`)}
			if kind != model.PluginTypeSkill {
				atts = append(atts, raw("AGENTS.md", "instructions"))
			}
			p := model.Plugin{ID: "x", Type: kind, SpaceID: &space, Package: pkg(atts...)}
			calls := 0
			files, err := Resolve(context.Background(), &p, func(_ context.Context, key string, _ int64) ([]byte, error) {
				calls++
				if key != "plugins/a/attachments/real" {
					return nil, fmt.Errorf("stale object unavailable")
				}
				return []byte("real body"), nil
			}, DefaultLimits())
			if err != nil || calls != 1 {
				t.Fatalf("stale object masked authoritative pointer: err=%v calls=%d", err, calls)
			}
			found := false
			for _, f := range files {
				if f.Path == "SKILL.md" {
					found = string(f.Data) == "real body"
				}
			}
			if !found {
				t.Fatal("authoritative Markdown lost")
			}
		})
	}
}
