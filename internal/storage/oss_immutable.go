package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func (s *OSSStorage) PutImmutable(ctx context.Context, key string, r io.Reader, size int64, contentType string) (bool, error) {
	if !IsImmutableArtifactKey(key) || size < 0 {
		return false, ErrImmutableObject
	}
	input := &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.key(key)), Body: r, ContentLength: aws.Int64(size), ContentType: aws.String(contentType)}
	var options []func(*s3.Options)
	if s.cosEndpoint {
		// COS ignores S3 If-None-Match on uploads. Its native condition is only
		// safe on buckets that have never enabled versioning. Check once per
		// storage instance; operators must keep bucket settings stable during use.
		s.cosVersionOnce.Do(func() {
			versioning, err := s.client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(s.bucket)})
			if err != nil {
				s.cosVersionErr = fmt.Errorf("COS immutable uploads require readable bucket versioning status (cos:GetBucketVersioning): %w", err)
				return
			}
			if versioning.Status != "" {
				s.cosVersionErr = fmt.Errorf("COS immutable uploads require a bucket without versioning; status is %s", versioning.Status)
			}
		})
		if s.cosVersionErr != nil {
			return false, s.cosVersionErr
		}
		options = append(options, func(o *s3.Options) {
			o.APIOptions = append(o.APIOptions, smithyhttp.SetHeaderValue("x-cos-forbid-overwrite", "true"))
		})
	} else {
		input.IfNoneMatch = aws.String("*")
	}
	_, err := s.client.PutObject(ctx, input, options...)
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && ((!s.cosEndpoint && api.ErrorCode() == "PreconditionFailed") || (s.cosEndpoint && api.ErrorCode() == "FileAlreadyExists")) {
			return false, nil
		}
		// Unsupported conditional writes, ambiguous transport failures and conflicts
		// fail closed. Never retry with an unconditional overwrite.
		return false, fmt.Errorf("immutable artifact upload: %w", err)
	}
	return true, nil
}

// Use only canonical COS API hostnames. Custom endpoints retain the S3
// contract; an arbitrary hostname containing "cos" must not switch protocols.
func isCOSEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.HasSuffix(host, ".myqcloud.com") &&
		(strings.HasPrefix(host, "cos.") || strings.Contains(host, ".cos.") ||
			strings.HasPrefix(host, "cos-internal.") || strings.Contains(host, ".cos-internal."))
}
