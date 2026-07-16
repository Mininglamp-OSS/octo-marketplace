// Package blob is the object-storage boundary for octo-marketplace. It uploads
// small binary assets (currently MCP icons) to an S3-compatible bucket
// (MinIO / AWS S3 / any SigV4 store) and exposes the public URL used to serve
// them back. The service layer depends on the Uploader interface so the icon
// upload flow is unit-testable without a live bucket; production wiring uses
// the SigV4 client in s3.go.
//
// Only what the icon feature needs lives here (a single PutObject). A richer
// surface (presigned GETs, deletes, download proxying) is deliberately absent
// until a concrete need appears (AGENTS.md: no speculative abstractions).
package blob

import "context"

// Uploader stores an object and returns the public URL a client should use to
// fetch it. Implementations must be safe for concurrent use.
type Uploader interface {
	// Put writes data at key with the given content type and returns the
	// public URL for that object. key is a storage path like
	// "mcp_icon/ab/<id>/3.png"; the implementation prefixes the bucket and
	// public base URL. It is idempotent — re-Putting the same key overwrites.
	Put(ctx context.Context, key, contentType string, data []byte) (url string, err error)
}
