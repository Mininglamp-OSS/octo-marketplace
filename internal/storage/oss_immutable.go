package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

func (s *OSSStorage) PutImmutable(ctx context.Context, key string, r io.Reader, size int64, contentType string) (bool, error) {
	if !IsImmutableArtifactKey(key) || size < 0 {
		return false, ErrImmutableObject
	}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.key(key)), Body: r, ContentLength: aws.Int64(size), ContentType: aws.String(contentType), IfNoneMatch: aws.String("*")})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && api.ErrorCode() == "PreconditionFailed" {
			return false, nil
		}
		// Unsupported conditional writes, ambiguous transport failures and conflicts
		// fail closed. Never retry with an unconditional overwrite.
		return false, fmt.Errorf("immutable artifact upload: %w", err)
	}
	return true, nil
}
