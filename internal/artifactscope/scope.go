// Package artifactscope defines managed attachment ownership independently of
// the caller. Callers must authorize the resource before resolving its objects.
package artifactscope

import (
	"path"
	"regexp"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

var segment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Prefix chooses the destination for new attachments, without inventing a Space
// for globally visible resources. Empty means the ownership cannot be resolved.
func Prefix(p *model.Plugin) string {
	if p == nil {
		return ""
	}
	if model.NormalizeLegacyVisibility(p.Visibility) == model.PluginVisibilitySystem {
		if !segment.MatchString(p.ID) {
			return ""
		}
		return "platform/plugins/" + p.ID + "/attachments/"
	}
	if p.SpaceID == nil || !segment.MatchString(*p.SpaceID) {
		return ""
	}
	return "plugins/" + *p.SpaceID + "/attachments/"
}

func within(key, prefix string) bool {
	return prefix != "" && len(key) <= 512 && !strings.ContainsAny(key, "\\\x00?#") && !strings.Contains(key, "://") && path.Clean(key) == key && strings.HasPrefix(key, prefix) && len(key) > len(prefix)
}

// Allows binds platform objects to their exact resource ID. Historical system
// rows with a real Space retain access to that owning Space's managed objects;
// this does not authorize another resource's platform prefix or foreign Space.
func Allows(p *model.Plugin, key string) bool {
	if p == nil {
		return false
	}
	if within(key, Prefix(p)) {
		return true
	}
	if model.NormalizeLegacyVisibility(p.Visibility) == model.PluginVisibilitySystem && p.SpaceID != nil && segment.MatchString(*p.SpaceID) {
		return within(key, "plugins/"+*p.SpaceID+"/attachments/")
	}
	return false
}

// IsManagedIconKey matches the shared upload namespaces used by the existing
// icon resolver. Upload IDs are independent of plugin IDs and Space IDs.
var iconKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

func IsManagedIconKey(key string) bool {
	return iconKeyPattern.MatchString(key) && !strings.Contains(key, "..") && (strings.HasPrefix(key, "icons/") || strings.HasPrefix(key, "mcp-icons/"))
}
