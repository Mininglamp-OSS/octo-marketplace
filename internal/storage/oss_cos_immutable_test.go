package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type cosTestTransport struct{ target *url.URL }

func (c cosTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = c.target.Scheme, c.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

func cosTestStore(t *testing.T, handler http.HandlerFunc) *OSSStorage {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	endpoint := "https://cos.ap-beijing.myqcloud.com"
	s, err := NewOSS(OSSConfig{Endpoint: endpoint, Bucket: "test", AccessKey: "test", SecretKey: "test", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	s.client = s3.New(s3.Options{BaseEndpoint: aws.String(endpoint), Region: "test", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), UsePathStyle: true, RetryMaxAttempts: 1, HTTPClient: &http.Client{Transport: cosTestTransport{target}}})
	return s
}

func TestCOSImmutableNativeCreateAndConflict(t *testing.T) {
	var original []byte
	versionChecks, puts := 0, 0
	s := cosTestStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Query().Has("versioning") {
			versionChecks++
			fmt.Fprint(w, `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`)
			return
		}
		puts++
		if r.Method != "PUT" || r.Header.Get("x-cos-forbid-overwrite") != "true" || r.Header.Get("If-None-Match") != "" {
			t.Errorf("expected COS-native conditional upload, got %s headers %v", r.Method, r.Header)
		}
		if !strings.Contains(r.Header.Get("Authorization"), "x-cos-forbid-overwrite") {
			t.Error("native condition must be signed")
		}
		body, _ := io.ReadAll(r.Body)
		if original != nil {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `<Error><Code>FileAlreadyExists</Code></Error>`)
			return
		}
		original = body
	})
	key := "platform/plugins/skill/attachments/versions/" + strings.Repeat("a", 64)
	if created, err := s.PutImmutable(context.Background(), key, strings.NewReader("old"), 3, "text/plain"); err != nil || !created {
		t.Fatalf("create=%v err=%v", created, err)
	}
	if created, err := s.PutImmutable(context.Background(), key, strings.NewReader("new"), 3, "text/plain"); err != nil || created {
		t.Fatalf("existing=%v err=%v", created, err)
	}
	if !bytes.Equal(original, []byte("old")) || versionChecks != 1 || puts != 2 {
		t.Fatalf("body=%q versionChecks=%d puts=%d", original, versionChecks, puts)
	}
}

func TestCOSImmutableRejectsUnsafeVersioningBeforeUpload(t *testing.T) {
	for _, mode := range []string{"Enabled", "Suspended", "denied", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			puts := 0
			s := cosTestStore(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" && r.URL.Query().Has("versioning") {
					if mode == "denied" {
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprint(w, `<Error><Code>AccessDenied</Code></Error>`)
					} else if mode == "malformed" {
						fmt.Fprint(w, `<invalid`)
					} else {
						fmt.Fprintf(w, `<VersioningConfiguration><Status>%s</Status></VersioningConfiguration>`, mode)
					}
					return
				}
				puts++
			})
			created, err := s.PutImmutable(context.Background(), "plugins/space/attachments/versions/hash", strings.NewReader("new"), 3, "text/plain")
			if err == nil || created || puts != 0 {
				t.Fatalf("must fail before PUT: created=%v error=%v puts=%d", created, err, puts)
			}
		})
	}
}

func TestCOSEndpointDetection(t *testing.T) {
	for _, endpoint := range []string{
		"https://cos.ap-beijing.myqcloud.com",
		"https://bucket-123.cos.ap-shanghai.myqcloud.com",
		"https://bucket-123.cos-internal.ap-shanghai.myqcloud.com",
	} {
		if !isCOSEndpoint(endpoint) {
			t.Errorf("COS endpoint not recognized: %s", endpoint)
		}
	}
	for _, endpoint := range []string{
		"https://s3.amazonaws.com", "http://localhost:9000",
		"https://cos.ap-beijing.myqcloud.com.example.org",
		"https://myqcloud.com", "https://example.org/cos.ap-beijing.myqcloud.com",
	} {
		if isCOSEndpoint(endpoint) {
			t.Errorf("non-COS endpoint misclassified: %s", endpoint)
		}
	}
}

func TestCOSImmutableDoesNotFallbackAfterUploadFailure(t *testing.T) {
	for _, code := range []string{"AccessDenied", "InvalidArgument", "PreconditionFailed", "NotModified"} {
		t.Run(code, func(t *testing.T) {
			puts := 0
			s := cosTestStore(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" && r.URL.Query().Has("versioning") {
					fmt.Fprint(w, `<VersioningConfiguration/>`)
					return
				}
				puts++
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprintf(w, `<Error><Code>%s</Code></Error>`, code)
			})
			created, err := s.PutImmutable(context.Background(), "plugins/space/attachments/versions/hash", strings.NewReader("new"), 3, "text/plain")
			if err == nil || created || puts != 1 {
				t.Fatalf("must fail without fallback: created=%v err=%v puts=%d", created, err, puts)
			}
		})
	}
}
