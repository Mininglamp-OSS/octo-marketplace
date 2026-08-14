package expert

import (
	"context"
	"strings"
)

// metricCounts is one resource's counters from resource_metrics.
type metricCounts struct {
	View    int64
	Install int64
}

// loadMetrics returns the view/install counters for the given ids of one
// resource type, keyed by resource id. Ids with no metrics row are absent from
// the map, so callers indexing into it get zero counts for free.
func (r *Repo) loadMetrics(ctx context.Context, resourceType Entity, ids []string) (map[string]metricCounts, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	q := `SELECT resource_id, view_count, install_count FROM resource_metrics
		WHERE resource_type = ? AND resource_id IN (` + marks + `)`
	args := make([]any, 0, len(ids)+1)
	args = append(args, string(resourceType))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]metricCounts, len(ids))
	for rows.Next() {
		var id string
		var c metricCounts
		if err := rows.Scan(&id, &c.View, &c.Install); err != nil {
			return nil, err
		}
		counts[id] = c
	}
	return counts, rows.Err()
}
