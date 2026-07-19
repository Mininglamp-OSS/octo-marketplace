package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// OSSStorage implements Storage using an S3-compatible object store (Aliyun OSS, MinIO, AWS S3, etc.).
type OSSStorage struct {
	client          *s3.Client
	presignClient   *s3.Client
	bucket          string
	keyPrefix       string
	publicEndpoint  string
	publicPathStyle bool
	signingHost     string
	downloadSigned  bool
}

// OSSConfig holds the configuration for S3-compatible storage.
type OSSConfig struct {
	Endpoint        string
	Bucket          string
	AccessKey       string
	SecretKey       string
	Region          string
	KeyPrefix       string
	PathStyle       bool
	PublicEndpoint  string
	PublicPathStyle bool
	SigningHost     string
	DownloadSigned  bool
}

// NewOSS creates a Storage backed by an S3-compatible service.
func NewOSS(cfg OSSConfig) (*OSSStorage, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("OSS_ENDPOINT, OSS_BUCKET, OSS_ACCESS_KEY, and OSS_SECRET_KEY are required when STORAGE_DRIVER=oss")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}

	creds := credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")

	// Internal client for server-side operations
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(cfg.Endpoint),
		Region:       region,
		Credentials:  creds,
		UsePathStyle: cfg.PathStyle,
	})

	// Server-side operations always use the internal endpoint. Browser-side
	// uploads must be signed for the host the browser will actually call,
	// unless a CDN is explicitly configured to restore the origin Host header.
	presignEndpoint := cfg.Endpoint
	if cfg.PublicEndpoint != "" && strings.TrimSpace(cfg.SigningHost) == "" {
		presignEndpoint = cfg.PublicEndpoint
	}
	presignCli := s3.New(s3.Options{
		BaseEndpoint: aws.String(presignEndpoint),
		Region:       region,
		Credentials:  creds,
		UsePathStyle: cfg.PathStyle,
	})

	return &OSSStorage{
		client:          client,
		presignClient:   presignCli,
		bucket:          cfg.Bucket,
		keyPrefix:       strings.Trim(cfg.KeyPrefix, "/"),
		publicEndpoint:  strings.TrimRight(cfg.PublicEndpoint, "/"),
		publicPathStyle: cfg.PublicPathStyle,
		signingHost:     strings.TrimSpace(cfg.SigningHost),
		downloadSigned:  cfg.DownloadSigned,
	}, nil
}

// PresignPut generates a presigned PUT URL using the public endpoint.
func (s *OSSStorage) PresignPut(ctx context.Context, key string, contentType string, expires time.Duration) (string, http.Header, error) {
	pc := s3.NewPresignClient(s.presignClient)
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(key)),
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}

	result, err := pc.PresignPutObject(ctx, input, s3.WithPresignExpires(expires))
	if err != nil {
		return "", nil, fmt.Errorf("oss presign put: %w", err)
	}

	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	publicURL, err := s.publicPresignedURL(result.URL)
	if err != nil {
		return "", nil, err
	}
	return publicURL, h, nil
}

// PresignGet returns the public object URL when a public endpoint is configured.
// Otherwise it falls back to a presigned URL against the storage endpoint.
func (s *OSSStorage) PresignGet(ctx context.Context, key string, expires time.Duration) (string, error) {
	if s.publicEndpoint != "" && !s.downloadSigned {
		return s.publicObjectURL(key)
	}

	pc := s3.NewPresignClient(s.presignClient)
	result, err := pc.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(key)),
	}, s3.WithPresignExpires(expires))
	if err != nil {
		return "", fmt.Errorf("oss presign get: %w", err)
	}
	return s.publicPresignedURL(result.URL)
}

// GetObject downloads an object from storage (uses internal endpoint).
func (s *OSSStorage) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(key)),
	})
	if err != nil {
		return nil, fmt.Errorf("oss get object: %w", err)
	}
	return output.Body, nil
}

// PutObject uploads an object to storage from a reader.
func (s *OSSStorage) PutObject(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error {
	input := &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(s.key(key)),
		Body:          reader,
		ContentLength: aws.Int64(size),
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	_, err := s.client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("oss put object: %w", err)
	}
	return nil
}

// DeleteObject removes an object from storage.
func (s *OSSStorage) DeleteObject(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(key)),
	})
	if err != nil {
		return fmt.Errorf("oss delete object: %w", err)
	}
	return nil
}

// CopyObject copies an object from srcKey to dstKey within the same bucket.
func (s *OSSStorage) CopyObject(ctx context.Context, srcKey, dstKey string) error {
	copySource := fmt.Sprintf("%s/%s", s.bucket, s.key(srcKey))
	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(s.bucket),
		CopySource: aws.String(copySource),
		Key:        aws.String(s.key(dstKey)),
	})
	if err != nil {
		return fmt.Errorf("oss copy object: %w", err)
	}
	return nil
}

// PublicURL returns a persistent URL that points at the object on the public
// endpoint (CDN or custom domain), suitable for storing in DB columns. Unlike
// PresignGet, no signature is attached — the bucket policy must allow anonymous
// GET at this path. OSS_PUBLIC_ENDPOINT must be configured; otherwise there is
// no stable non-signed URL we can return.
func (s *OSSStorage) PublicURL(_ context.Context, key string) (string, error) {
	if s.publicEndpoint == "" {
		return "", fmt.Errorf("OSS_PUBLIC_ENDPOINT not configured; cannot construct a persistent public URL")
	}
	return s.publicObjectURL(key)
}

func (s *OSSStorage) key(key string) string {
	key = strings.TrimLeft(key, "/")
	if s.keyPrefix == "" {
		return key
	}
	return s.keyPrefix + "/" + key
}

func (s *OSSStorage) publicObjectURL(key string) (string, error) {
	public, err := url.Parse(s.publicEndpoint)
	if err != nil || public.Scheme == "" || public.Host == "" {
		return "", fmt.Errorf("invalid OSS_PUBLIC_ENDPOINT %q", s.publicEndpoint)
	}
	objectPath := strings.TrimLeft(s.key(key), "/")
	if s.publicPathStyle {
		objectPath = strings.Trim(s.bucket, "/") + "/" + objectPath
	}
	public.Path = strings.TrimRight(public.Path, "/") + "/" + objectPath
	public.RawPath = ""
	public.RawQuery = ""
	public.Fragment = ""
	return public.String(), nil
}

// publicPresignedURL preserves the COS-origin host in the signature while
// returning a browser-facing CDN/custom-domain URL for signed uploads. The CDN
// must restore the Host header to signingHost before COS verifies SigV4.
func (s *OSSStorage) publicPresignedURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse presigned URL: %w", err)
	}
	if s.signingHost != "" && !strings.EqualFold(u.Host, s.signingHost) {
		return "", fmt.Errorf("presigned signing host %q does not match OSS_SIGNING_HOST %q", u.Host, s.signingHost)
	}
	if s.publicEndpoint == "" {
		return u.String(), nil
	}
	public, err := url.Parse(s.publicEndpoint)
	if err != nil || public.Scheme == "" || public.Host == "" {
		return "", fmt.Errorf("invalid OSS_PUBLIC_ENDPOINT %q", s.publicEndpoint)
	}
	u.Scheme = public.Scheme
	u.Host = public.Host
	return u.String(), nil
}
