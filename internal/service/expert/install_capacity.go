package expert

import (
	"fmt"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
)

type installCapacityError struct {
	actualBytes int64
	skill       string
	path        string
}

func (e *installCapacityError) Error() string {
	return fmt.Sprintf("installation JSON exceeds %d bytes; actual at least %d bytes", fleet.MaxCapabilityRequestBytes, e.actualBytes)
}
func (e *installCapacityError) Unwrap() error { return ErrInstallTooLarge }
func (e *installCapacityError) CapacityDetails() map[string]any {
	details := map[string]any{"field": "definition", "limit_bytes": int64(fleet.MaxCapabilityRequestBytes), "actual_bytes": e.actualBytes, "actual_bytes_lower_bound": true}
	if e.skill != "" {
		details["skill"] = e.skill
	}
	if e.path != "" {
		details["path"] = e.path
	}
	return details
}
