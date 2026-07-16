package blob

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPutSignsAndUploads runs the SigV4 PutObject against an in-process HTTP
// server so the signing + request shape is exercised without a real bucket.
func TestPutSignsAndUploads(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotSha    string
		gotBody   []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotSha = r.Header.Get("X-Amz-Content-Sha256")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewS3Client(S3Config{
		Endpoint:  srv.URL,
		Region:    "us-east-1",
		Bucket:    "icons",
		AccessKey: "AK",
		SecretKey: "SK",
		PathStyle: true,
	})

	url, err := c.Put(context.Background(), "mcp_icon/mcp/abc/1.png", "image/png", []byte("PNGDATA"))
	if err != nil {
		t.Fatalf("put failed: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/icons/mcp_icon/mcp/abc/1.png" {
		t.Fatalf("path = %q", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AK/") {
		t.Fatalf("authorization not SigV4: %q", gotAuth)
	}
	if !strings.Contains(gotAuth, "SignedHeaders=") || !strings.Contains(gotAuth, "Signature=") {
		t.Fatalf("authorization missing SigV4 parts: %q", gotAuth)
	}
	if gotSha == "" {
		t.Fatalf("missing X-Amz-Content-Sha256")
	}
	if string(gotBody) != "PNGDATA" {
		t.Fatalf("body = %q", gotBody)
	}
	if !strings.HasSuffix(url, "/icons/mcp_icon/mcp/abc/1.png") {
		t.Fatalf("returned url = %q", url)
	}
}

func TestPutPropagatesStorageError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<Error><Code>AccessDenied</Code></Error>"))
	}))
	defer srv.Close()

	c := NewS3Client(S3Config{
		Endpoint: srv.URL, Region: "us-east-1", Bucket: "icons",
		AccessKey: "AK", SecretKey: "SK", PathStyle: true,
	})
	if _, err := c.Put(context.Background(), "k.png", "image/png", []byte("x")); err == nil {
		t.Fatalf("expected error on non-2xx status")
	}
}

func TestPublicURLUsesBaseWhenSet(t *testing.T) {
	c := NewS3Client(S3Config{
		Endpoint: "https://minio:9000", Bucket: "icons",
		PublicBaseURL: "https://cdn.example.com/icons", PathStyle: true,
	})
	got := c.PublicURL("mcp_icon/mcp/a/1.png")
	if got != "https://cdn.example.com/icons/mcp_icon/mcp/a/1.png" {
		t.Fatalf("public url = %q", got)
	}
}
