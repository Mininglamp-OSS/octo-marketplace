package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsImmutableArtifactKeyStrictNamespace(t *testing.T) {
	positive := []string{
		"plugins/space-a/attachments/versions/hash",
		"./plugins/space-a/attachments/versions/hash",
		"/plugins/space-a/attachments/versions/hash",
		"plugins/space-a/attachments/./versions/hash",
	}
	for _, key := range positive {
		if !IsImmutableArtifactKey(key) {
			t.Fatalf("rejected canonical key %q", key)
		}
	}
	negative := []string{
		"plugins/space-a/attachments/x/versions/hash",
		"plugins/space-a/other/attachments/versions/hash",
		"plugins/space-a/attachments/versionsX/hash",
		"plugins/space-a/attachments/versions",
		"plugins/space-a/attachments/versions/",
		"plugins//attachments/versions/hash",
		"plugins/space a/attachments/versions/hash",
		"plugins/.hidden/attachments/versions/hash",
		"plugins/space-a/attachments/versions/../upload",
		"x/plugins/space-a/attachments/versions/hash",
	}
	for _, key := range negative {
		if IsImmutableArtifactKey(key) {
			t.Fatalf("accepted non-canonical key %q", key)
		}
	}
}

func TestLocalImmutableConcurrentCreateAndMutationGuards(t *testing.T) {
	s := NewLocal(t.TempDir(), "http://localhost")
	key := "plugins/space-a/attachments/versions/digest"
	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.PutImmutable(context.Background(), key, strings.NewReader("old bytes"), 9, "text/plain")
			if err != nil {
				t.Error(err)
			}
			if ok {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("created %d objects", created.Load())
	}
	if ok, err := s.PutImmutable(context.Background(), key, strings.NewReader("new bytes"), 9, "text/plain"); err != nil || ok {
		t.Fatalf("replaced existing bytes: %v %v", ok, err)
	}
	for _, alias := range []string{key, "./" + key, "/" + key, "plugins/space-a/attachments/./versions/digest"} {
		if err := s.WriteObject(alias, strings.NewReader("changed")); err == nil {
			t.Fatalf("write allowed at %s", alias)
		}
		if _, _, err := s.PresignPut(context.Background(), alias, "text/plain", time.Minute); err == nil {
			t.Fatalf("presign allowed at %s", alias)
		}
		if _, err := s.PresignGet(context.Background(), alias, time.Minute); err == nil {
			t.Fatalf("presign get allowed at %s", alias)
		}
		if _, err := s.PublicURL(context.Background(), alias); err == nil {
			t.Fatalf("public url allowed at %s", alias)
		}
		if err := s.DeleteObject(context.Background(), alias); err == nil {
			t.Fatalf("delete allowed at %s", alias)
		}
		if err := s.CopyObject(context.Background(), "source", alias); err == nil {
			t.Fatalf("copy allowed at %s", alias)
		}
	}
	body, err := s.GetObject(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	raw, _ := io.ReadAll(body)
	if string(raw) != "old bytes" {
		t.Fatalf("history changed: %s", raw)
	}
}

func TestLocalImmutableRejectsIncompleteUpload(t *testing.T) {
	s := NewLocal(t.TempDir(), "http://localhost")
	for i, body := range []string{"short", "too long"} {
		key := fmt.Sprintf("plugins/a/attachments/versions/f%d", i)
		if _, err := s.PutImmutable(context.Background(), key, strings.NewReader(body), 7, ""); err == nil {
			t.Fatalf("accepted wrong length %q", body)
		}
		if _, err := s.StatObject(context.Background(), key); err != ErrObjectNotFound {
			t.Fatalf("partial object exposed: %v", err)
		}
	}
}

func TestOSSImmutableUsesConditionalWriteAndGuards(t *testing.T) {
	var status atomic.Int32
	status.Store(200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.Header.Get("If-None-Match") != "*" {
			t.Errorf("unconditional request: %s %v", r.Method, r.Header)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		if status.Load() != 200 {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(int(status.Load()))
			fmt.Fprint(w, `<Error><Code>PreconditionFailed</Code></Error>`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	s, err := NewOSS(OSSConfig{Endpoint: server.URL, Region: "test", Bucket: "test", AccessKey: "test", SecretKey: "test", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	key := "plugins/a/attachments/versions/hash"
	if created, err := s.PutImmutable(context.Background(), key, strings.NewReader("bytes"), 5, "text/plain"); err != nil || !created {
		t.Fatalf("create=%v err=%v", created, err)
	}
	status.Store(412)
	if created, err := s.PutImmutable(context.Background(), key, strings.NewReader("bytes"), 5, "text/plain"); err != nil || created {
		t.Fatalf("existing=%v err=%v", created, err)
	}
	for _, alias := range []string{key, "/" + key, "./" + key} {
		if err := s.PutObject(context.Background(), alias, strings.NewReader("new"), 3, ""); err != ErrImmutableObject {
			t.Fatalf("generic write: %v", err)
		}
		if err := s.DeleteObject(context.Background(), alias); err != ErrImmutableObject {
			t.Fatalf("delete: %v", err)
		}
		if err := s.CopyObject(context.Background(), "source", alias); err != ErrImmutableObject {
			t.Fatalf("copy: %v", err)
		}
		if _, _, err := s.PresignPut(context.Background(), alias, "", time.Minute); err != ErrImmutableObject {
			t.Fatalf("presign: %v", err)
		}
		if _, err := s.PresignGet(context.Background(), alias, time.Minute); err != ErrImmutableObject {
			t.Fatalf("presign get: %v", err)
		}
		if _, err := s.PublicURL(context.Background(), alias); err != ErrImmutableObject {
			t.Fatalf("public url: %v", err)
		}
	}
}

func TestLocalImmutableCannotBeModifiedThroughSymlinkAlias(t *testing.T) {
	root := t.TempDir()
	s := NewLocal(root, "http://localhost")
	ctx := context.Background()
	key := "plugins/a/attachments/versions/hash"
	if _, err := s.PutImmutable(ctx, key, strings.NewReader("old"), 3, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, key), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteObject("alias", strings.NewReader("new")); err != ErrImmutableObject {
		t.Fatalf("aliased overwrite: %v", err)
	}
	if err := s.DeleteObject(ctx, "alias"); err != ErrImmutableObject {
		t.Fatalf("aliased delete: %v", err)
	}
}
