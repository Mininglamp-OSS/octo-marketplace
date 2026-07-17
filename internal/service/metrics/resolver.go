package metrics

import "context"

// Caller represents the identity of the user making the request.
type Caller struct {
	UID     string
	SpaceID string
}

// ResourceResolver determines whether a resource exists and is visible
// to the caller.
type ResourceResolver interface {
	CanView(ctx context.Context, resourceID string, caller Caller) (bool, error)
}

var resolvers = make(map[string]ResourceResolver)

// RegisterResolver registers a ResourceResolver for the given resource type.
func RegisterResolver(resourceType string, r ResourceResolver) {
	resolvers[resourceType] = r
}

// GetResolver returns the registered ResourceResolver for the given resource type.
func GetResolver(resourceType string) (ResourceResolver, bool) {
	r, ok := resolvers[resourceType]
	return r, ok
}

// ResetResolvers clears the resolver registry. For testing only.
func ResetResolvers() {
	resolvers = make(map[string]ResourceResolver)
}
