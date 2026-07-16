package storage

import (
	"context"
	"io"
	"net/http"
	"time"
)

// Storage defines the object storage interface for skill file uploads.
type Storage interface {
	// PresignPut generates a presigned PUT URL for uploading an object.
	PresignPut(ctx context.Context, key string, contentType string, expires time.Duration) (url string, headers http.Header, err error)

	// PresignGet returns a public object URL when configured, otherwise a presigned GET URL.
	PresignGet(ctx context.Context, key string, expires time.Duration) (url string, err error)

	// GetObject retrieves an object from storage.
	GetObject(ctx context.Context, key string) (io.ReadCloser, error)

	// DeleteObject removes an object from storage.
	DeleteObject(ctx context.Context, key string) error

	// CopyObject copies an object from src to dst key. Used to relocate
	// uploaded files from temporary paths to permanent skill paths.
	CopyObject(ctx context.Context, srcKey, dstKey string) error
}
