package storage

import (
	"context"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
)

var ErrImmutableObject = errors.New("immutable artifact cannot be modified")

// immutableSpaceSegment mirrors service/plugin.safeObjectSegment. Keeping the
// shape check here as well makes this storage primitive safe on its own: every
// caller, including future ones, gets the same canonical namespace definition.
var immutableSpaceSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// IsImmutableArtifactKey identifies the host-reserved version namespace:
//
//	plugins/<space>/attachments/versions/<non-empty>
//
// The key is normalized first so aliases such as "./plugins/..." and
// "/plugins/..." cannot bypass the guards. A substring occurrence elsewhere
// (".../x/versions/..." or ".../versions-x/...") is deliberately NOT the
// reserved namespace; only the canonical prefix is.
func IsImmutableArtifactKey(key string) bool {
	key = strings.TrimLeft(path.Clean(key), "/")
	rest, ok := strings.CutPrefix(key, "plugins/")
	if !ok {
		return false
	}
	space, tail, ok := strings.Cut(rest, "/")
	if !ok || !immutableSpaceSegment.MatchString(space) {
		return false
	}
	digest, ok := strings.CutPrefix(tail, "attachments/versions/")
	return ok && digest != ""
}

// ImmutableWriter atomically creates an absent version object. A false result
// means it already exists and MUST be checked against the expected bytes.
// Keeping this separate from Storage avoids changing legacy upload contracts.
type ImmutableWriter interface {
	PutImmutable(context.Context, string, io.Reader, int64, string) (bool, error)
}
