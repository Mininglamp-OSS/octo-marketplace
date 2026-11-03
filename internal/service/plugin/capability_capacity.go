package plugin

import (
	"fmt"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
)

type capacityLocation struct {
	skill string
	path  string
}

type installationCapacityError struct {
	field       string
	limitBytes  int64
	actualBytes int64
	lowerBound  bool
	skill       string
	path        string
}

func newInstallationCapacityError(field string, actualBytes int64, lowerBound bool, location capacityLocation) *installationCapacityError {
	return &installationCapacityError{
		field: field, limitBytes: int64(fleet.MaxCapabilityRequestBytes), actualBytes: actualBytes,
		lowerBound: lowerBound, skill: location.skill, path: location.path,
	}
}

func installationContentTooLarge(field, skill, path string) *installationCapacityError {
	limit := int64(fleet.MaxCapabilityRequestBytes)
	return newInstallationCapacityError(field, limit+1, true, capacityLocation{skill: skill, path: path})
}

func (e *installationCapacityError) Error() string {
	actual := fmt.Sprintf("%d", e.actualBytes)
	if e.lowerBound {
		actual = ">=" + actual
	}
	message := fmt.Sprintf("installation exceeds %d-byte limit; actual %s bytes; field %s", e.limitBytes, actual, e.field)
	if e.skill != "" {
		message += "; skill " + e.skill
	}
	if e.path != "" {
		message += "; path " + e.path
	}
	return message
}

func (e *installationCapacityError) Unwrap() error { return ErrTooLarge }

func (e *installationCapacityError) CapacityDetails() map[string]any {
	details := map[string]any{
		"field": e.field, "limit_bytes": e.limitBytes, "actual_bytes": e.actualBytes,
	}
	if e.lowerBound {
		details["actual_bytes_lower_bound"] = true
	}
	if e.skill != "" {
		details["skill"] = e.skill
	}
	if e.path != "" {
		details["path"] = e.path
	}
	return details
}
func installationRequestCapacityError(actualBytes int64, request fleet.CapabilityInstallRequest) *installationCapacityError {
	var location capacityLocation
	for i := len(request.Definition.Skills) - 1; i >= 0 && location.path == ""; i-- {
		skill := request.Definition.Skills[i]
		for j := len(skill.Files) - 1; j >= 0; j-- {
			if skill.Files[j].Path != "" {
				location = capacityLocation{skill: skill.MarketSkillID, path: skill.Files[j].Path}
				break
			}
		}
	}
	return newInstallationCapacityError("definition", actualBytes, false, location)
}
