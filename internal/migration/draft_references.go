package migration

import (
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func validateNodeReferenceMetadata(n Node) error {
	// An independent unfinished draft has no executable reference graph. Any
	// actual reference, and every release, still uses the strict contract.
	if !n.Release && n.Plugin.ListingState == model.PluginListingStateDraft && len(n.Relations) == 0 {
		return nil
	}
	return pluginrepo.ValidateReferenceMetadata(n.Plugin.Type, n.Relations)
}
