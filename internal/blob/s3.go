package blob

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// S3Config configures the SigV4 S3-compatible client. It is populated from
// environment (see internal/config). Endpoint points at the storage API
// (e.g. https://minio.internal:9000 for MinIO, https://s3.amazonaws.com for
// AWS); PublicBaseURL is what clients hit to read objects back (a CDN or the
// same bucket host) and may differ from Endpoint behind a gateway.
type S3Config struct {
	Endpoint      string // storage API base, no trailing slash
	Region        string
	Bucket        string
	AccessKey     string
	SecretKey     string
	PublicBaseURL string // public base for GET, no trailing slash; defaults to Endpoint/Bucket
	// PathStyle forces path-style addressing (https://host/bucket/key), which
	// MinIO and most self-hosted gateways require. Virtual-host style
	// (https://bucket.host/key) is used when false.
	PathStyle bool
}

// S3Client is a minimal SigV4 PutObject client built on net/http, so the
// service depends on no third-party object-storage SDK (AGENTS.md: no
// dependency until needed). It signs each PUT with AWS Signature V4, which
// MinIO, AWS S3, and compatible stores all accept.
type S3Client struct {
	cfg  S3Config
	http *http.Client
}

// NewS3Client returns an S3-compatible uploader for cfg.
func NewS3Client(cfg S3Config) *S3Client {
	return &S3Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

const unsignedPayload = "UNSIGNED-PAYLOAD"

// Put uploads data to the bucket at key and returns its public URL.
func (c *S3Client) Put(ctx context.Context, key, contentType string, data []byte) (string, error) {
	endpoint := c.objectURL(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = int64(len(data))

	payloadHash := sha256Hex(data)
	if err := c.sign(req, payloadHash, time.Now().UTC()); err != nil {
		return "", err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("s3 put %s: status %d: %s", key, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return c.PublicURL(key), nil
}

// PublicURL returns the read URL for key. When PublicBaseURL is set it is
// joined with the key; otherwise the same host/addressing as uploads is used.
func (c *S3Client) PublicURL(key string) string {
	if c.cfg.PublicBaseURL != "" {
		return strings.TrimRight(c.cfg.PublicBaseURL, "/") + "/" + key
	}
	return c.objectURL(key)
}

// objectURL builds the storage API URL for key using path- or virtual-host
// style per the config.
func (c *S3Client) objectURL(key string) string {
	base := strings.TrimRight(c.cfg.Endpoint, "/")
	if c.cfg.PathStyle {
		return base + "/" + c.cfg.Bucket + "/" + escapePath(key)
	}
	// virtual-host style: bucket becomes a subdomain of the endpoint host.
	if u, err := url.Parse(base); err == nil {
		u.Host = c.cfg.Bucket + "." + u.Host
		u.Path = "/" + escapePath(key)
		return u.String()
	}
	return base + "/" + escapePath(key)
}

// sign applies AWS Signature Version 4 to req in place, following the S3
// canonical-request → string-to-sign → signature chain.
func (c *S3Client) sign(req *http.Request, payloadHash string, now time.Time) error {
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if req.Header.Get("Host") == "" {
		req.Header.Set("Host", req.URL.Host)
	}

	signedHeaders, canonicalHeaders := canonicalHeaders(req)
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQuery(req.URL),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, c.cfg.Region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := c.signingKey(dateStamp)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	auth := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.cfg.AccessKey, scope, signedHeaders, signature,
	)
	req.Header.Set("Authorization", auth)
	return nil
}

func (c *S3Client) signingKey(dateStamp string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+c.cfg.SecretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(c.cfg.Region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	return hmacSHA256(kService, []byte("aws4_request"))
}

// canonicalHeaders returns the semicolon-joined signed-header list and the
// canonical header block. Host, X-Amz-Date, and X-Amz-Content-Sha256 are
// always signed; Content-Type is signed when present.
func canonicalHeaders(req *http.Request) (signed, canonical string) {
	headers := map[string]string{
		"host":                 req.URL.Host,
		"x-amz-date":           req.Header.Get("X-Amz-Date"),
		"x-amz-content-sha256": req.Header.Get("X-Amz-Content-Sha256"),
	}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		headers["content-type"] = ct
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(":")
		b.WriteString(strings.TrimSpace(headers[k]))
		b.WriteString("\n")
	}
	return strings.Join(keys, ";"), b.String()
}

func canonicalURI(u *url.URL) string {
	if u.Path == "" {
		return "/"
	}
	// EscapedPath preserves the already-escaped object key path segments.
	return u.EscapedPath()
}

func canonicalQuery(u *url.URL) string {
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// escapePath escapes each key segment for use in a URL path while keeping the
// slash separators, so "mcp_icon/ab/id/1.png" stays a nested path.
func escapePath(key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}
