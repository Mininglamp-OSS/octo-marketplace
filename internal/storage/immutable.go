package storage

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"
)

var ErrImmutableObject = errors.New("immutable artifact cannot be modified")

// IsImmutableArtifactKey identifies the host-reserved version namespace.
// Generic upload/copy/delete operations may not mutate these objects.
func IsImmutableArtifactKey(key string) bool {
	key = strings.TrimLeft(path.Clean(key), "/")
	return strings.HasPrefix(key, "plugins/") && strings.Contains(key, "/attachments/versions/")
}

// ImmutableWriter atomically creates an absent version object. A false result
// means it already exists and MUST be checked against the expected bytes.
// Keeping this separate from Storage avoids changing legacy upload contracts.
type ImmutableWriter interface {
	PutImmutable(context.Context, string, io.Reader, int64, string) (bool, error)
}
