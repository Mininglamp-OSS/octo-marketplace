package storage

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestOSSKeyPrefix(t *testing.T) {
	s := &OSSStorage{keyPrefix: "im-test/marketplace"}
	if got := s.key("/skills/a.zip"); got != "im-test/marketplace/skills/a.zip" {
		t.Fatalf("key=%q", got)
	}
}

func TestCOSPublicDownloadUsesUnsignedCDNURL(t *testing.T) {
	s, err := NewOSS(OSSConfig{
		Endpoint:       "https://cos.ap-beijing.myqcloud.com",
		Region:         "ap-beijing",
		Bucket:         "im-data-1255521909",
		AccessKey:      "test-access-key",
		SecretKey:      "test-secret-key",
		KeyPrefix:      "im-test/marketplace",
		PathStyle:      false,
		PublicEndpoint: "https://cdn.deepminer.com.cn",
		SigningHost:    "im-data-1255521909.cos.ap-beijing.myqcloud.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.PresignGet(context.Background(), "skills/demo.zip", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Host != "cdn.deepminer.com.cn" {
		t.Fatalf("host=%q", u.Host)
	}
	if u.Path != "/im-test/marketplace/skills/demo.zip" {
		t.Fatalf("path=%q", u.Path)
	}
	if u.RawQuery != "" {
		t.Fatalf("download URL must not be signed: %s", raw)
	}
}

func TestCOSDownloadFallsBackToPresignedOriginURL(t *testing.T) {
	s, err := NewOSS(OSSConfig{
		Endpoint:  "https://cos.ap-beijing.myqcloud.com",
		Region:    "ap-beijing",
		Bucket:    "im-data-1255521909",
		AccessKey: "test-access-key",
		SecretKey: "test-secret-key",
		PathStyle: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.PresignGet(context.Background(), "skills/demo.zip", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Host != "im-data-1255521909.cos.ap-beijing.myqcloud.com" {
		t.Fatalf("host=%q", u.Host)
	}
	if u.Query().Get("X-Amz-Signature") == "" {
		t.Fatalf("missing fallback signature: %s", raw)
	}
}

func TestCOSDownloadCanUseSignedCDNURL(t *testing.T) {
	s, err := NewOSS(OSSConfig{
		Endpoint:       "https://cos.ap-beijing.myqcloud.com",
		Region:         "ap-beijing",
		Bucket:         "im-data-1255521909",
		AccessKey:      "test-access-key",
		SecretKey:      "test-secret-key",
		PathStyle:      false,
		PublicEndpoint: "https://cdn.deepminer.com.cn",
		SigningHost:    "im-data-1255521909.cos.ap-beijing.myqcloud.com",
		DownloadSigned: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.PresignGet(context.Background(), "skills/demo.zip", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Host != "cdn.deepminer.com.cn" || u.Query().Get("X-Amz-Signature") == "" {
		t.Fatalf("signed CDN URL=%q", raw)
	}
}

func TestPresignPutUsesPublicEndpointWhenNoSigningHost(t *testing.T) {
	s, err := NewOSS(OSSConfig{
		Endpoint:        "http://minio:9000",
		Region:          "us-east-1",
		Bucket:          "octo-marketplace",
		AccessKey:       "test-access-key",
		SecretKey:       "test-secret-key",
		PathStyle:       true,
		PublicEndpoint:  "http://127.0.0.1:29000",
		PublicPathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, headers, err := s.PresignPut(context.Background(), "skills/demo.zip", "application/zip", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Host != "127.0.0.1:29000" {
		t.Fatalf("host=%q", u.Host)
	}
	if u.Path != "/octo-marketplace/skills/demo.zip" {
		t.Fatalf("path=%q", u.Path)
	}
	if u.Query().Get("X-Amz-Signature") == "" {
		t.Fatalf("missing signature: %s", raw)
	}
	if headers.Get("Content-Type") != "application/zip" {
		t.Fatalf("content-type header=%q", headers.Get("Content-Type"))
	}
}

func TestMinIOPublicDownloadUsesPathStyleBucketURL(t *testing.T) {
	s, err := NewOSS(OSSConfig{
		Endpoint:        "http://minio:9000",
		Region:          "us-east-1",
		Bucket:          "octo-marketplace",
		AccessKey:       "test-access-key",
		SecretKey:       "test-secret-key",
		PathStyle:       true,
		PublicEndpoint:  "http://127.0.0.1:29000",
		PublicPathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := s.PresignGet(context.Background(), "skills/demo.zip", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Host != "127.0.0.1:29000" {
		t.Fatalf("host=%q", u.Host)
	}
	if u.Path != "/octo-marketplace/skills/demo.zip" {
		t.Fatalf("path=%q", u.Path)
	}
	if u.RawQuery != "" {
		t.Fatalf("download URL must not be signed: %s", raw)
	}
}

func TestPublicPresignedURLRewritesOnlyOrigin(t *testing.T) {
	s := &OSSStorage{
		publicEndpoint: "https://cdn.example.com",
		signingHost:    "bucket.cos.ap-beijing.myqcloud.com",
	}
	raw := "https://bucket.cos.ap-beijing.myqcloud.com/im-test/marketplace/a.zip?X-Amz-Signature=abc"
	got, err := s.publicPresignedURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(got)
	if u.Host != "cdn.example.com" || u.Path != "/im-test/marketplace/a.zip" || u.Query().Get("X-Amz-Signature") != "abc" {
		t.Fatalf("rewritten URL=%q", got)
	}
}

func TestPublicPresignedURLRejectsSigningHostMismatch(t *testing.T) {
	s := &OSSStorage{signingHost: "expected.example.com"}
	_, err := s.publicPresignedURL("https://wrong.example.com/a?X-Amz-Signature=abc")
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err=%v", err)
	}
}
