package stockmigration

import (
	"context"
	"database/sql"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// SkillResolver must read only operator-authorized storage; no network URL
// fallback, writes, script execution, or normalization of original bytes.
type SkillResolver func(context.Context, *model.Plugin) (map[string]string, error)

func ReadWithResolver(ctx context.Context, db *sql.DB, limit int, resolver SkillResolver) (Dataset, error) {
	d, e := Read(ctx, db, limit)
	if e != nil {
		return Dataset{}, e
	}
	return Hydrate(ctx, d, resolver), nil
}

// Hydrate freezes successfully verified source bytes into the preview digest.
// Apply re-reads them with source rows locked; only new packages contain copies.
func Hydrate(ctx context.Context, d Dataset, resolver SkillResolver) Dataset {
	if resolver == nil {
		return d
	}
	d.ResolvedSkillFiles = map[string]map[string]string{}
	d.FileExceptions = map[string]string{}
	needed := map[string]bool{}
	retired := map[string]bool{}
	for _, id := range d.MigratedSourceIDs {
		retired[id] = true
	}
	for _, r := range d.Resources {
		for _, e := range r.Relations {
			if e.Type == "expert_skill" && e.TargetVersionID == nil && e.Status == 1 && e.DeletedAt == nil {
				needed[e.TargetPluginID] = true
			}
		}
	}
	total := 0
	for _, r := range d.Resources {
		p := r.Plugin
		if p.Type != model.PluginTypeSkill || p.DeletedAt != nil || p.Status != 1 || retired[p.ID] || (!p.IsEmbedded && !needed[p.ID]) {
			continue
		}
		files, e := resolver(ctx, &p)
		if e != nil {
			d.FileExceptions[p.ID] = "artifact_verification_failed"
			continue
		}
		size := 0
		for path, content := range files {
			size += len(path) + len(content)
		}
		if total+size > 32<<20 {
			d.FileExceptions[p.ID] = "artifact_verification_failed"
			continue
		}
		total += size
		d.ResolvedSkillFiles[p.ID] = files
	}
	return d
}
