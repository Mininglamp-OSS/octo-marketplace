package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	libplugin "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
)

type Mode string

const (
	ModeDryRun Mode = "dry-run"
	ModeApply  Mode = "apply"
	ModeVerify Mode = "verify"
)

type Options struct{ Mode Mode }
type Issue struct {
	Level  string `json:"level"`
	Code   string `json:"code"`
	Source string `json:"source,omitempty"`
	ID     string `json:"id,omitempty"`
	Detail string `json:"detail"`
}
type Counts struct {
	Categories         int `json:"categories"`
	Plugins            int `json:"plugins"`
	Relations          int `json:"relations"`
	Versions           int `json:"versions"`
	Audits             int `json:"audits"`
	CategoryPlacements int `json:"category_placements"`
	Placements         int `json:"plugin_placements"`
	Inserted           int `json:"inserted,omitempty"`
	Existing           int `json:"existing,omitempty"`
	Missing            int `json:"missing,omitempty"`
	Conflicts          int `json:"conflicts,omitempty"`
}
type Report struct {
	Mode         Mode      `json:"mode"`
	Expected     Counts    `json:"expected"`
	Observed     Counts    `json:"observed"`
	ExpectedHash string    `json:"expected_hash"`
	ObservedHash string    `json:"observed_hash,omitempty"`
	Issues       []Issue   `json:"issues"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
}
type catRow struct {
	id, name, icon, types string
	order, status         int
	created, updated      time.Time
	deleted               sql.NullTime
}
type plugRow struct {
	id, name, typ, cat, tags, publisher, owner, space, visibility, creator, by, botUID, botName, manifest, pkg, mhash, phash, versionID string
	embedded                                                                                                                            int
	status                                                                                                                              int
	created, updated                                                                                                                    time.Time
	deleted                                                                                                                             sql.NullTime
}
type verRow struct {
	id, pid, version, manifest, pkg, mhash, phash, relations, changelog, by string
	created                                                                 time.Time
}
type relRow struct {
	id, source, target, typ, data, by string
	order                             int
	status                            int
	created, updated                  time.Time
	deleted                           sql.NullTime
}
type cpRow struct {
	id, code, typ, cat string
	order              int
	created, updated   time.Time
}
type placeRow struct {
	id, code, plugin, cat string
	order                 int
	created, updated      time.Time
}
type plan struct {
	cats          []catRow
	plugins       []plugRow
	relations     []relRow
	versions      []verRow
	catPlacements []cpRow
	placements    []placeRow
	issues        []Issue
}
type Runner struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Runner {
	return &Runner{db: db, now: func() time.Time { return time.Now().UTC() }}
}
func (r *Runner) Run(ctx context.Context, o Options) (Report, error) {
	if o.Mode == "" {
		o.Mode = ModeDryRun
	}
	if o.Mode != ModeDryRun && o.Mode != ModeApply && o.Mode != ModeVerify {
		return Report{}, fmt.Errorf("invalid mode %q", o.Mode)
	}
	start := r.now()
	p, e := r.build(ctx)
	if e != nil {
		return Report{}, e
	}
	rep := Report{Mode: o.Mode, StartedAt: start, Issues: p.issues, Expected: Counts{Categories: len(p.cats), Plugins: len(p.plugins), Relations: len(p.relations), Versions: len(p.versions), Audits: len(p.plugins), CategoryPlacements: len(p.catPlacements), Placements: len(p.placements)}, ExpectedHash: p.hash()}
	if o.Mode == ModeApply {
		if e = r.apply(ctx, p, &rep); e != nil {
			return Report{}, e
		}
	}
	if o.Mode != ModeDryRun {
		inserted, existing := rep.Observed.Inserted, rep.Observed.Existing
		rep.Observed, rep.ObservedHash, rep.Issues, e = r.verify(ctx, p, rep.Issues)
		if e != nil {
			return Report{}, e
		}
		rep.Observed.Inserted, rep.Observed.Existing = inserted, existing
	}
	rep.FinishedAt = r.now()
	return rep, nil
}
func (r *Runner) build(ctx context.Context) (plan, error) {
	var p plan
	cats, e := r.categories(ctx, &p)
	if e != nil {
		return p, e
	}
	tags, e := r.tags(ctx, "skill_tags")
	if e != nil {
		return p, e
	}
	expertTags, e := r.tags(ctx, "expert_tags")
	if e != nil {
		return p, e
	}
	if e = r.skills(ctx, cats, tags, &p); e != nil {
		return p, e
	}
	if e = r.experts(ctx, cats, expertTags, &p); e != nil {
		return p, e
	}
	if e = r.squads(ctx, cats, expertTags, &p); e != nil {
		return p, e
	}
	if e = r.mcps(ctx, &p); e != nil {
		return p, e
	}
	if e = validatePlanReferences(p); e != nil {
		return p, fmt.Errorf("invalid generated references: %w", e)
	}
	if e = validateGraph(p.relations, 16, 500); e != nil {
		return p, fmt.Errorf("invalid generated relation graph: %w", e)
	}
	setVersionRelations(&p)
	if e = derivePlacements(&p); e != nil {
		return p, e
	}
	sort.Slice(p.cats, func(i, j int) bool { return p.cats[i].id < p.cats[j].id })
	sort.Slice(p.plugins, func(i, j int) bool { return p.plugins[i].id < p.plugins[j].id })
	sort.Slice(p.relations, func(i, j int) bool { return p.relations[i].id < p.relations[j].id })
	sort.Slice(p.versions, func(i, j int) bool { return p.versions[i].id < p.versions[j].id })
	return p, nil
}

// derivePlacements maps legacy market presence onto the confirmed "default"
// placement: every active category is registered for each of its plugin types,
// ordered by its legacy sort_order, and every active top-level (non-embedded)
// plugin is placed under its own category. All rows are deterministic and
// timestamped from their source records so re-runs preflight cleanly.
func derivePlacements(p *plan) error {
	activeCats := map[string]bool{}
	for _, c := range p.cats {
		if c.status != 1 || c.deleted.Valid {
			continue
		}
		activeCats[c.id] = true
		var types []string
		if err := json.Unmarshal([]byte(c.types), &types); err != nil {
			return fmt.Errorf("category %s plugin types: %w", c.id, err)
		}
		for _, typ := range types {
			p.catPlacements = append(p.catPlacements, cpRow{
				id:      DeterministicID("category_placement", "default#"+typ+"#"+c.id),
				code:    "default",
				typ:     typ,
				cat:     c.id,
				order:   c.order,
				created: c.created,
				updated: c.updated,
			})
		}
	}
	for _, x := range p.plugins {
		if x.embedded != 0 || x.status != 1 {
			continue
		}
		cat := x.cat
		if cat != "" && !activeCats[cat] {
			cat = ""
		}
		p.placements = append(p.placements, placeRow{
			id:      DeterministicID("plugin_placement", "default#"+x.id),
			code:    "default",
			plugin:  x.id,
			cat:     cat,
			created: x.created,
			updated: x.updated,
		})
	}
	sort.Slice(p.catPlacements, func(i, j int) bool { return p.catPlacements[i].id < p.catPlacements[j].id })
	sort.Slice(p.placements, func(i, j int) bool { return p.placements[i].id < p.placements[j].id })
	return nil
}

func active(d sql.NullTime) int {
	if d.Valid {
		return 0
	}
	return 1
}
func expertVisibility(v string) string {
	if v == "public" {
		return "space"
	}
	return v
}

// skillVisibility maps a legacy skill's own visibility into the unified enum.
// Legacy skills used `public` for "全平台可见" (unlike experts/mcp, whose
// `public` meant space-scoped), so a skill `public` becomes the unified global
// value `system`; `space`/`private` pass through. Embedded skills inherit their
// parent expert/squad's already-mapped visibility instead and never reach here.
func skillVisibility(v string) string {
	if v == "public" {
		return "system"
	}
	return v
}
func nstr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func ntime(t sql.NullTime) any {
	if t.Valid {
		return t.Time
	}
	return nil
}

// both computes the contract plugin_hash over a manifest/package pair via the
// lib's frozen formula (canonical concatenation, no separator).
func both(a, b []byte) string {
	hash, err := libplugin.ComputePluginHash(a, b)
	if err != nil {
		return ""
	}
	return hash
}
func (r *Runner) categories(ctx context.Context, p *plan) (map[string]string, error) {
	m := map[string]string{}
	for _, s := range []struct {
		table, family string
		types         []string
	}{{"categories", "skillcat", []string{"skill"}}, {"expert_categories", "expertcat", []string{"expert", "expert_team"}}} {
		rs, e := r.db.QueryContext(ctx, `SELECT id,name,icon_key,sort_order,created_at,updated_at,deleted_at FROM `+s.table)
		if e != nil {
			return nil, e
		}
		for rs.Next() {
			var old, n, i string
			var o int
			var c, u time.Time
			var d sql.NullTime
			if e = rs.Scan(&old, &n, &i, &o, &c, &u, &d); e != nil {
				rs.Close()
				return nil, e
			}
			id := DeterministicID(s.family, old)
			t, _ := json.Marshal(s.types)
			m[s.table+":"+old] = id
			p.cats = append(p.cats, catRow{id, n, i, string(t), o, active(d), c, u, d})
		}
		if e = rs.Err(); e != nil {
			rs.Close()
			return nil, e
		}
		rs.Close()
	}
	return m, nil
}
func (r *Runner) tags(ctx context.Context, table string) (map[int64]string, error) {
	if table != "skill_tags" && table != "expert_tags" {
		return nil, fmt.Errorf("unsupported tag table %q", table)
	}
	rs, e := r.db.QueryContext(ctx, `SELECT id,name FROM `+table)
	if e != nil {
		return nil, e
	}
	defer rs.Close()
	m := map[int64]string{}
	for rs.Next() {
		var id int64
		var n string
		if e = rs.Scan(&id, &n); e != nil {
			return nil, e
		}
		m[id] = n
	}
	return m, rs.Err()
}
func mkver(id, pid, v, ch, by string, c time.Time, m, p []byte) verRow {
	return verRow{id: id, pid: pid, version: v, manifest: string(m), pkg: string(p), mhash: hashJSON(m), phash: both(m, p), relations: "[]", changelog: ch, by: by, created: c}
}

func (r *Runner) skills(ctx context.Context, cats map[string]string, dict map[int64]string, p *plan) error {
	q := `SELECT id,name,display_name,icon_url,source_skill_id,current_version_id,description,category_id,tags,owner_id,owner_name,creator_id,creator_name,space_id,visibility,version,readme_content,file_name,file_url,file_size,file_sha256,created_at,updated_at,is_deleted FROM skills`
	rs, e := r.db.QueryContext(ctx, q)
	if e != nil {
		return e
	}
	defer rs.Close()
	for rs.Next() {
		var id, n, display, icon, source, current, desc, cat, raw, owner, ownerName, creatorID, creator, space, visibility, v, readme, fileName, fileURL, sha string
		var size int64
		var c, u time.Time
		var deleted bool
		if e = rs.Scan(&id, &n, &display, &icon, &source, &current, &desc, &cat, &raw, &owner, &ownerName, &creatorID, &creator, &space, &visibility, &v, &readme, &fileName, &fileURL, &size, &sha, &c, &u, &deleted); e != nil {
			return e
		}
		tags, e := namesFromTagIDs([]byte(raw), dict)
		if e != nil {
			p.issues = append(p.issues, Issue{"skip", "invalid_skill_tags", "skills", id, e.Error()})
			continue
		}
		pid := PluginID("skill", id)
		pluginName := display
		if pluginName == "" {
			pluginName = n
		}
		draft, _ := canonical(newPluginManifest(pluginName, "skill", n, desc, tags, nil))
		extras := make([]rawAttachment, 0, 2)
		if readme != "" {
			extras = append(extras, rawAttachment{path: "SKILL.md", mimeType: "text/markdown", content: readme})
		}
		// The top-level skill artifact lives in legacy object storage; keep its
		// stable pointer, hash, and size so the migrated Plugin still references
		// its downloadable content.
		if fileURL != "" || fileName != "" {
			ref, _ := jsonAttachment("skill/ref.json", map[string]any{"file_name": fileName, "file_sha256": sha, "file_size": size, "file_url": fileURL})
			extras = append(extras, ref)
		}
		docs, docErr := canonicalDocs(pluginName, "skill", tags, draft, extras, space)
		if docErr != nil {
			p.issues = append(p.issues, Issue{"skip", "invalid_skill_documents", "skills", id, docErr.Error()})
			continue
		}
		m, pkg := []byte(docs.Manifest), []byte(docs.Package)
		vs, selected, issue, e := r.skillVersions(ctx, id, pid, current, v, creatorID, c, m, pkg)
		if e != nil {
			return e
		}
		d := sql.NullTime{}
		if deleted {
			d = sql.NullTime{Time: u, Valid: true}
		}
		p.plugins = append(p.plugins, plugRow{
			id: pid, name: pluginName, typ: "skill", cat: cats["categories:"+cat], tags: string(docs.Tags),
			publisher: ownerName, owner: owner, space: space, visibility: skillVisibility(visibility),
			creator: creator, by: "human", manifest: selected.manifest, pkg: selected.pkg,
			mhash: selected.mhash, phash: selected.phash, versionID: selected.id,
			status: active(d), created: c, updated: u, deleted: d,
		})
		p.versions = append(p.versions, vs...)
		if issue != nil {
			p.issues = append(p.issues, *issue)
		}
	}
	return rs.Err()
}
func (r *Runner) skillVersions(ctx context.Context, sid, pid, currentSourceID, fallbackVersion, fallbackBy string, fallbackCreated time.Time, manifest, pkg []byte) ([]verRow, verRow, *Issue, error) {
	rs, err := r.db.QueryContext(ctx, `SELECT id,version,changelog,storage,changed_by,created_at FROM skill_versions WHERE skill_id=? ORDER BY created_at,id`, sid)
	if err != nil {
		return nil, verRow{}, nil, err
	}
	defer rs.Close()
	var out []verRow
	selectedIndex := -1
	for rs.Next() {
		var id, version, by string
		var changelog, storage sql.NullString
		var created time.Time
		if err = rs.Scan(&id, &version, &changelog, &storage, &by, &created); err != nil {
			return nil, verRow{}, nil, err
		}
		if storage.Valid && !json.Valid([]byte(storage.String)) {
			return nil, verRow{}, nil, fmt.Errorf("invalid skill version storage JSON")
		}
		// Legacy storage metadata cannot safely become an attachment without
		// the corresponding bytes and content hash. Every history row therefore
		// retains the same canonical snapshot package.
		if by == "" {
			by = fallbackBy
		}
		out = append(out, mkver(DeterministicID("skillver", id), pid, version, changelog.String, by, created, manifest, pkg))
		if id == currentSourceID {
			selectedIndex = len(out) - 1
		}
	}
	if err = rs.Err(); err != nil {
		return nil, verRow{}, nil, err
	}
	if len(out) == 0 {
		if currentSourceID != "" {
			return nil, verRow{}, nil, fmt.Errorf("skill %q current_version_id %q does not reference its version history", sid, currentSourceID)
		}
		if fallbackVersion == "" {
			fallbackVersion = "1.0.0"
		}
		row := mkver(DeterministicID("skillver", sid+":"+fallbackVersion), pid, fallbackVersion, "", fallbackBy, fallbackCreated, manifest, pkg)
		issue := Issue{"info", "synthetic_skill_version", "skills", sid, "no version rows; current snapshot created"}
		return []verRow{row}, row, &issue, nil
	}
	if currentSourceID != "" && selectedIndex < 0 {
		return nil, verRow{}, nil, fmt.Errorf("skill %q current_version_id %q does not reference its version history", sid, currentSourceID)
	}
	if selectedIndex < 0 {
		selectedIndex = len(out) - 1 // ORDER BY created_at,id makes this deterministic latest.
	}
	return out, out[selectedIndex], nil, nil
}

type legacySkillRef struct {
	SkillKey     string   `json:"skill_key"`
	Name         string   `json:"name"`
	ObjectKey    string   `json:"object_key"`
	ZipObjectKey string   `json:"zip_object_key"`
	FileName     string   `json:"file_name"`
	FileSize     int64    `json:"file_size"`
	Files        []string `json:"files"`
}

type legacyMember struct {
	MemberKey     string           `json:"member_key"`
	TemplateID    string           `json:"template_id"`
	Name          string           `json:"name"`
	Role          string           `json:"role"`
	IsLeader      bool             `json:"is_leader"`
	UsageExamples []string         `json:"usage_examples"`
	Instruction   string           `json:"instruction"`
	MCPConfig     string           `json:"mcp_config"`
	Skills        []legacySkillRef `json:"skills"`
}

func decodeArray(raw string, out any) error {
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return err
	}
	return nil
}

func appendPlugin(p *plan, x plugRow, v verRow) {
	p.plugins = append(p.plugins, x)
	p.versions = append(p.versions, v)
}

func snapshotSkill(parentID string, index, occurrence int, s legacySkillRef, owner, space, visibility, creator, by string, created, updated time.Time, deleted sql.NullTime) (plugRow, verRow, error) {
	raw, _ := canonical(s)
	source := "snapshot:" + hashJSON(raw)
	if s.SkillKey != "" {
		source = "skill_key:" + s.SkillKey
	} else if s.ObjectKey != "" {
		source = "object_key:" + s.ObjectKey
	} else if s.ZipObjectKey != "" {
		source = "zip_object_key:" + s.ZipObjectKey
	}
	id := DeterministicID("snapshotskill", fmt.Sprintf("%s:%s:%06d", parentID, source, occurrence))
	draft := embeddedSkillManifest(s)
	extra, _ := jsonAttachment("skill/ref.json", map[string]any{"file_name": s.FileName, "file_size": s.FileSize, "files": nonNilStrings(s.Files), "object_key": s.ObjectKey, "skill_key": skillPathKey(s), "zip_object_key": s.ZipObjectKey})
	// The contract requires the SKILL.md entry file; snapshot skills carry
	// their full document behind the ref.json pointer, so the inline entry is
	// a minimal deterministic stub (readers prefer the referenced object).
	entry := rawAttachment{path: "SKILL.md", mimeType: "text/markdown", content: entryMarkdown(s.Name, "")}
	docs, err := canonicalDocs(s.Name, "skill", nil, draft, []rawAttachment{entry, extra}, space)
	if err != nil {
		return plugRow{}, verRow{}, err
	}
	manifest, pkg := []byte(docs.Manifest), []byte(docs.Package)
	vid := DeterministicID("snapshotskillver", id)
	x := plugRow{id: id, name: s.Name, typ: "skill", embedded: 1, tags: string(docs.Tags), owner: owner, space: space, visibility: visibility, creator: creator, by: by, manifest: string(manifest), pkg: string(pkg), mhash: docs.ManifestHash, phash: docs.PluginHash, versionID: vid, status: active(deleted), created: created, updated: updated, deleted: deleted}
	return x, mkver(vid, id, "1.0.0", "", owner, created, manifest, pkg), nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func snapshotOccurrences[T any](items []T, key func(T) string) []int {
	seen := map[string]int{}
	out := make([]int, len(items))
	for i, item := range items {
		k := key(item)
		out[i] = seen[k]
		seen[k]++
	}
	return out
}

func skillPathKey(s legacySkillRef) string {
	if s.SkillKey != "" {
		return s.SkillKey
	}
	return DeterministicID("skillkey", skillIdentityKey(s))
}

func embeddedSkillManifest(s legacySkillRef) []byte {
	raw, _ := canonical(newPluginManifest(s.Name, "skill", s.Name, "", nil, nil))
	return raw
}

func skillIdentityKey(s legacySkillRef) string {
	if s.SkillKey != "" {
		return "skill_key:" + s.SkillKey
	}
	if s.ObjectKey != "" {
		return "object_key:" + s.ObjectKey
	}
	if s.ZipObjectKey != "" {
		return "zip_object_key:" + s.ZipObjectKey
	}
	raw, _ := canonical(s)
	return "snapshot:" + hashJSON(raw)
}

func memberIdentityKey(m legacyMember) string {
	if m.MemberKey != "" {
		return "member_key:" + m.MemberKey
	}
	raw, _ := canonical(m)
	return "snapshot:" + hashJSON(raw)
}

func relation(source, target, typ string, order int, data any, by string, created, updated time.Time, deleted sql.NullTime) relRow {
	raw, _ := canonical(data)
	return relRow{id: DeterministicID("relation", fmt.Sprintf("%s:%s:%06d:%s", source, typ, order, target)), source: source, target: target, typ: typ, order: order, data: string(raw), status: active(deleted), by: by, created: created, updated: updated, deleted: deleted}
}

func (r *Runner) experts(ctx context.Context, cats map[string]string, dict map[int64]string, p *plan) error {
	q := `SELECT id,short_name,name,summary,category_id,tags,publisher,owner_uid,creator_name,created_by_type,created_by_bot_uid,created_by_bot_name,space_id,visibility,instruction,mcp_config,skills_json,created_at,updated_at,deleted_at FROM experts`
	rs, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rs.Close()
	for rs.Next() {
		var id, short, name, summary, cat, rawTags, publisher, owner, creator, by, visibility, instruction, mcp, rawSkills string
		var botUID, botName, space sql.NullString
		var created, updated time.Time
		var deleted sql.NullTime
		if err = rs.Scan(&id, &short, &name, &summary, &cat, &rawTags, &publisher, &owner, &creator, &by, &botUID, &botName, &space, &visibility, &instruction, &mcp, &rawSkills, &created, &updated, &deleted); err != nil {
			return err
		}
		tags, tagErr := namesFromTagIDs([]byte(rawTags), dict)
		var skills []legacySkillRef
		safeMCP, mcpErr := SanitizeConnectorJSON([]byte(mcp))
		if tagErr != nil || decodeArray(rawSkills, &skills) != nil || mcpErr != nil {
			detail := "invalid skills_json"
			if tagErr != nil {
				detail = tagErr.Error()
			}
			if mcpErr != nil {
				detail = mcpErr.Error()
			}
			p.issues = append(p.issues, Issue{"skip", "invalid_expert_snapshot", "experts", id, detail})
			continue
		}
		pid := PluginID("expert", id)
		draft, _ := canonical(newPluginManifest(name, "expert", name, summary, tags, nil))
		var cfg any
		_ = json.Unmarshal(safeMCP, &cfg)
		mcpAttachment, _ := jsonAttachment("mcp.json", cfg)
		// The contract requires every expert package to carry the AGENTS.md
		// entry; experts without a legacy instruction get a minimal document
		// derived from their display fields.
		extras := []rawAttachment{mcpAttachment, {path: "AGENTS.md", mimeType: "text/markdown", content: expertAgentsMarkdown(name, summary, instruction)}}
		// Embedded skills are not copied into the expert package: each one is
		// promoted to a standalone skill Plugin and referenced through an
		// expert_skill relation, so related content has exactly one home.
		docs, docErr := canonicalDocs(name, "expert", tags, draft, extras, space.String)
		if docErr != nil {
			p.issues = append(p.issues, Issue{"skip", "invalid_expert_documents", "experts", id, docErr.Error()})
			continue
		}
		// Legacy experts embed MCP config inline with no foreign key into
		// mcp_servers, so an expert_connector relation cannot be derived; record
		// the gap instead of fabricating an edge by content matching.
		if string(safeMCP) != "{}" && string(safeMCP) != "null" {
			p.issues = append(p.issues, Issue{"info", "expert_connector_unlinked", "experts", id, "inline mcp_config preserved in mcp.json; legacy schema has no mcp_servers reference to materialize an expert_connector relation"})
		}
		manifest, pkg := []byte(docs.Manifest), []byte(docs.Package)
		vid := DeterministicID("expertver", id)
		x := plugRow{
			id: pid, name: name, typ: "expert", cat: cats["expert_categories:"+cat], tags: string(docs.Tags),
			publisher: publisher, owner: owner, space: space.String, visibility: expertVisibility(visibility),
			creator: creator, by: by, botUID: botUID.String, botName: botName.String,
			manifest: string(manifest), pkg: string(pkg), mhash: docs.ManifestHash, phash: docs.PluginHash,
			versionID: vid, status: active(deleted), created: created, updated: updated, deleted: deleted,
		}
		appendPlugin(p, x, mkver(vid, pid, "1.0.0", "", owner, created, manifest, pkg))
		skillOccurrences := snapshotOccurrences(skills, skillIdentityKey)
		for i, skill := range skills {
			sx, sv, snapErr := snapshotSkill(pid, i, skillOccurrences[i], skill, owner, space.String, expertVisibility(visibility), creator, by, created, updated, deleted)
			if snapErr != nil {
				p.issues = append(p.issues, Issue{"skip", "invalid_embedded_skill", "experts", id, fmt.Sprintf("skill %d: %v", i, snapErr)})
				continue
			}
			appendPlugin(p, sx, sv)
			p.relations = append(p.relations, relation(pid, sx.id, "expert_skill", i, map[string]any{"source_index": i}, owner, created, updated, deleted))
		}
	}
	return rs.Err()
}

func (r *Runner) squads(ctx context.Context, cats map[string]string, dict map[int64]string, p *plan) error {
	q := `SELECT id,short_name,name,summary,category_id,tags,publisher,owner_uid,creator_name,created_by_type,created_by_bot_uid,created_by_bot_name,space_id,visibility,leader,strategies_json,dependencies_json,permission,members_json,created_at,updated_at,deleted_at FROM expert_squads`
	rs, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rs.Close()
	for rs.Next() {
		var id, short, name, summary, cat, rawTags, publisher, owner, creator, by, visibility, leader, strategies, dependencies, permission, rawMembers string
		var botUID, botName, space sql.NullString
		var created, updated time.Time
		var deleted sql.NullTime
		if err = rs.Scan(&id, &short, &name, &summary, &cat, &rawTags, &publisher, &owner, &creator, &by, &botUID, &botName, &space, &visibility, &leader, &strategies, &dependencies, &permission, &rawMembers, &created, &updated, &deleted); err != nil {
			return err
		}
		tags, tagErr := namesFromTagIDs([]byte(rawTags), dict)
		var members []legacyMember
		var strategyValue, dependencyValue any
		jsonErr := json.Unmarshal([]byte(strategies), &strategyValue)
		if jsonErr == nil {
			jsonErr = json.Unmarshal([]byte(dependencies), &dependencyValue)
		}
		if jsonErr == nil {
			jsonErr = decodeArray(rawMembers, &members)
		}
		if tagErr != nil || jsonErr != nil {
			detail := "invalid squad JSON"
			if tagErr != nil {
				detail = tagErr.Error()
			} else if jsonErr != nil {
				detail = jsonErr.Error()
			}
			p.issues = append(p.issues, Issue{"skip", "invalid_squad_snapshot", "expert_squads", id, detail})
			continue
		}
		valid := true
		memberMCP := make([]any, len(members))
		for i := range members {
			safe, e := SanitizeConnectorJSON([]byte(members[i].MCPConfig))
			if e != nil {
				p.issues = append(p.issues, Issue{"skip", "unsafe_member_mcp_config", "expert_squads", id, fmt.Sprintf("member %d: %v", i, e)})
				valid = false
				break
			}
			_ = json.Unmarshal(safe, &memberMCP[i])
		}
		if !valid {
			continue
		}
		pid := PluginID("expertteam", id)
		draft, _ := canonical(newPluginManifest(name, "expert_team", name, summary, tags, nil))
		// Contract layout: the team package is exactly one AGENTS.md carrying
		// the collaboration prose, dispatch strategies, dependencies, and
		// permission; membership and leadership live in relations.
		extras := []rawAttachment{{path: "AGENTS.md", mimeType: "text/markdown", content: teamAgentsMarkdown(name, summary, leader, strategyValue, dependencyValue, permission)}}
		// Member content is not copied into the team package: each member is a
		// standalone snapshot Plugin referenced through an expert_team_expert
		// relation, whose relation_json carries role/is_leader/member_key.
		docs, docErr := canonicalDocs(name, "expert_team", tags, draft, extras, space.String)
		if docErr != nil {
			p.issues = append(p.issues, Issue{"skip", "invalid_squad_documents", "expert_squads", id, docErr.Error()})
			continue
		}
		manifest, pkg := []byte(docs.Manifest), []byte(docs.Package)
		vid := DeterministicID("expertteamver", id)
		x := plugRow{
			id: pid, name: name, typ: "expert_team", cat: cats["expert_categories:"+cat], tags: string(docs.Tags),
			publisher: publisher, owner: owner, space: space.String, visibility: expertVisibility(visibility),
			creator: creator, by: by, botUID: botUID.String, botName: botName.String,
			manifest: string(manifest), pkg: string(pkg), mhash: docs.ManifestHash, phash: docs.PluginHash,
			versionID: vid, status: active(deleted), created: created, updated: updated, deleted: deleted,
		}
		appendPlugin(p, x, mkver(vid, pid, "1.0.0", "", owner, created, manifest, pkg))
		memberOccurrences := snapshotOccurrences(members, memberIdentityKey)
		for i, member := range members {
			memberSource := memberIdentityKey(member)
			mid := DeterministicID("snapshotmember", fmt.Sprintf("%s:%s:%06d", pid, memberSource, memberOccurrences[i]))
			memberDraft, _ := canonical(newPluginManifest(member.Name, "expert", member.Name, member.Role, nil, member.UsageExamples))
			contextAttachment, _ := jsonAttachment("expert/context.json", map[string]any{"is_leader": member.IsLeader, "member_key": member.MemberKey, "role": member.Role, "template_id": member.TemplateID})
			mcpAttachment, _ := jsonAttachment("mcp.json", memberMCP[i])
			memberExtras := []rawAttachment{contextAttachment, mcpAttachment, {path: "AGENTS.md", mimeType: "text/markdown", content: expertAgentsMarkdown(member.Name, member.Role, member.Instruction)}}
			memberDocs, memberErr := canonicalDocs(member.Name, "expert", nil, memberDraft, memberExtras, space.String)
			if memberErr != nil {
				p.issues = append(p.issues, Issue{"skip", "invalid_member_documents", "expert_squads", id, fmt.Sprintf("member %d: %v", i, memberErr)})
				continue
			}
			mm, mp := []byte(memberDocs.Manifest), []byte(memberDocs.Package)
			mvid := DeterministicID("snapshotmemberver", mid)
			mx := plugRow{
				id: mid, name: member.Name, typ: "expert", embedded: 1, cat: cats["expert_categories:"+cat], tags: string(memberDocs.Tags),
				publisher: publisher, owner: owner, space: space.String, visibility: expertVisibility(visibility),
				creator: creator, by: by, botUID: botUID.String, botName: botName.String,
				manifest: string(mm), pkg: string(mp), mhash: memberDocs.ManifestHash, phash: memberDocs.PluginHash,
				versionID: mvid, status: active(deleted), created: created, updated: updated, deleted: deleted,
			}
			appendPlugin(p, mx, mkver(mvid, mid, "1.0.0", "", owner, created, mm, mp))
			p.relations = append(p.relations, relation(pid, mid, "expert_team_expert", i, map[string]any{"source_index": i, "member_key": member.MemberKey, "role": member.Role, "is_leader": member.IsLeader}, owner, created, updated, deleted))
			skillOccurrences := snapshotOccurrences(member.Skills, skillIdentityKey)
			for j, skill := range member.Skills {
				sx, sv, snapErr := snapshotSkill(mid, j, skillOccurrences[j], skill, owner, space.String, expertVisibility(visibility), creator, by, created, updated, deleted)
				if snapErr != nil {
					p.issues = append(p.issues, Issue{"skip", "invalid_member_skill", "expert_squads", id, fmt.Sprintf("member %d skill %d: %v", i, j, snapErr)})
					continue
				}
				appendPlugin(p, sx, sv)
				p.relations = append(p.relations, relation(mid, sx.id, "expert_skill", j, map[string]any{"source_index": j}, owner, created, updated, deleted))
			}
		}
	}
	return rs.Err()
}

func (r *Runner) mcps(ctx context.Context, p *plan) error {
	q := `SELECT id,name,slug,slogan,category,icon,icon_version,tags_json,tools_json,usage_examples_json,faqs_json,notes_json,visibility,owner_uid,space_id,creator_name,created_by_type,created_by_bot_uid,created_by_bot_name,transport,config_json,created_at,updated_at,deleted_at FROM mcp_servers`
	rs, e := r.db.QueryContext(ctx, q)
	if e != nil {
		return e
	}
	defer rs.Close()
	for rs.Next() {
		var id, n, slug, slogan, cat, icon, tags, tools, examples, faqs, notes, visibility, owner, creator, by, transport, config string
		var iconV int
		var space, bu, bn sql.NullString
		var c, u time.Time
		var d sql.NullTime
		if e = rs.Scan(&id, &n, &slug, &slogan, &cat, &icon, &iconV, &tags, &tools, &examples, &faqs, &notes, &visibility, &owner, &space, &creator, &by, &bu, &bn, &transport, &config, &c, &u, &d); e != nil {
			return e
		}
		safe, e := SanitizeConnectorJSON([]byte(config))
		if e != nil {
			p.issues = append(p.issues, Issue{"skip", "unsafe_connector_config", "mcp_servers", id, e.Error()})
			continue
		}
		var tv, exampleValues []string
		var tl, fq, nt, cfg any
		if json.Unmarshal([]byte(tags), &tv) != nil || json.Unmarshal([]byte(tools), &tl) != nil || json.Unmarshal([]byte(examples), &exampleValues) != nil || json.Unmarshal([]byte(faqs), &fq) != nil || json.Unmarshal([]byte(notes), &nt) != nil || json.Unmarshal(safe, &cfg) != nil {
			p.issues = append(p.issues, Issue{"skip", "invalid_connector_json", "mcp_servers", id, "invalid JSON column"})
			continue
		}
		canonicalName := slug
		if canonicalName == "" {
			canonicalName = n
		}
		draft, _ := canonical(newPluginManifest(n, "connector", canonicalName, slogan, tv, exampleValues))
		mcpDocument, mcpErr := connectorMCPDocument(safe, transport, canonicalName)
		if mcpErr != nil {
			p.issues = append(p.issues, Issue{"skip", "invalid_connector_config", "mcp_servers", id, mcpErr.Error()})
			continue
		}
		mcpAttachment, _ := jsonAttachment("mcp.json", mcpDocument)
		toolsAttachment, _ := jsonAttachment("connector/tools.json", tl)
		examplesAttachment, _ := jsonAttachment("connector/examples.json", exampleValues)
		faqsAttachment, _ := jsonAttachment("connector/faqs.json", fq)
		notesAttachment, _ := jsonAttachment("connector/notes.json", nt)
		descriptor := &packageConnector{Type: "mcp", Source: "connector." + canonicalName}
		docs, docErr := canonicalConnectorDocs(n, "connector", tv, draft, []rawAttachment{mcpAttachment, toolsAttachment, examplesAttachment, faqsAttachment, notesAttachment}, space.String, descriptor)
		if docErr != nil {
			p.issues = append(p.issues, Issue{"skip", "invalid_connector_documents", "mcp_servers", id, docErr.Error()})
			continue
		}
		m, pkg := []byte(docs.Manifest), []byte(docs.Package)
		pid := PluginID("connector", id)
		vid := DeterministicID("connectorver", id)
		p.plugins = append(p.plugins, plugRow{
			id: pid, name: n, typ: "connector", tags: string(docs.Tags), owner: owner,
			space: space.String, visibility: expertVisibility(visibility), creator: creator, by: by,
			botUID: bu.String, botName: bn.String, manifest: string(m), pkg: string(pkg),
			mhash: docs.ManifestHash, phash: docs.PluginHash, versionID: vid,
			status: active(d), created: c, updated: u, deleted: d,
		})
		p.versions = append(p.versions, mkver(vid, pid, "1.0.0", "", owner, c, m, pkg))
	}
	return rs.Err()
}

func validatePlanReferences(p plan) error {
	plugins := make(map[string]struct{}, len(p.plugins))
	categories := make(map[string]struct{}, len(p.cats))
	versions := make(map[string]string, len(p.versions))
	for _, category := range p.cats {
		categories[category.id] = struct{}{}
	}
	for _, plugin := range p.plugins {
		if _, exists := plugins[plugin.id]; exists {
			return fmt.Errorf("duplicate plugin id %q", plugin.id)
		}
		plugins[plugin.id] = struct{}{}
		if plugin.cat != "" {
			if _, exists := categories[plugin.cat]; !exists {
				return fmt.Errorf("plugin %q references missing category %q", plugin.id, plugin.cat)
			}
		}
	}
	for _, relation := range p.relations {
		if _, exists := plugins[relation.source]; !exists {
			return fmt.Errorf("relation %q references missing source plugin %q", relation.id, relation.source)
		}
		if _, exists := plugins[relation.target]; !exists {
			return fmt.Errorf("relation %q references missing target plugin %q", relation.id, relation.target)
		}
	}
	for _, version := range p.versions {
		if _, exists := plugins[version.pid]; !exists {
			return fmt.Errorf("version %q references missing plugin %q", version.id, version.pid)
		}
		versions[version.id] = version.pid
	}
	for _, plugin := range p.plugins {
		if versions[plugin.versionID] != plugin.id {
			return fmt.Errorf("plugin %q references missing current version %q", plugin.id, plugin.versionID)
		}
	}
	return nil
}

func validateGraph(relations []relRow, maxDepth, maxNodes int) error {
	nodes := map[string]bool{}
	adj := map[string][]string{}
	undirected := map[string][]string{}
	for _, rel := range relations {
		if rel.source == rel.target {
			return fmt.Errorf("self-cycle at %s", rel.source)
		}
		nodes[rel.source], nodes[rel.target] = true, true
		adj[rel.source] = append(adj[rel.source], rel.target)
		undirected[rel.source] = append(undirected[rel.source], rel.target)
		undirected[rel.target] = append(undirected[rel.target], rel.source)
	}
	keys := make([]string, 0, len(nodes))
	for node := range nodes {
		keys = append(keys, node)
		sort.Strings(adj[node])
		sort.Strings(undirected[node])
	}
	sort.Strings(keys)
	seen := map[string]bool{}
	for _, root := range keys {
		if seen[root] {
			continue
		}
		queue, count := []string{root}, 0
		seen[root] = true
		for len(queue) > 0 {
			n := queue[0]
			queue = queue[1:]
			count++
			for _, next := range undirected[n] {
				if !seen[next] {
					seen[next] = true
					queue = append(queue, next)
				}
			}
		}
		if count > maxNodes {
			return fmt.Errorf("component node count %d exceeds %d", count, maxNodes)
		}
	}
	state, longest := map[string]uint8{}, map[string]int{}
	var visit func(string) (int, error)
	visit = func(node string) (int, error) {
		if state[node] == 1 {
			return 0, fmt.Errorf("cycle at %s", node)
		}
		if state[node] == 2 {
			return longest[node], nil
		}
		state[node] = 1
		best := 1
		for _, target := range adj[node] {
			d, err := visit(target)
			if err != nil {
				return 0, err
			}
			if d+1 > best {
				best = d + 1
			}
		}
		state[node], longest[node] = 2, best
		if best > maxDepth {
			return 0, fmt.Errorf("depth %d exceeds %d at %s", best, maxDepth, node)
		}
		return best, nil
	}
	for _, node := range keys {
		if _, err := visit(node); err != nil {
			return err
		}
	}
	return nil
}

func setVersionRelations(p *plan) {
	bySourceRows := map[string][]relRow{}
	for _, rel := range p.relations {
		bySourceRows[rel.source] = append(bySourceRows[rel.source], rel)
	}
	bySource := map[string][]map[string]any{}
	for source, rows := range bySourceRows {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].order != rows[j].order {
				return rows[i].order < rows[j].order
			}
			return rows[i].id < rows[j].id
		})
		for _, rel := range rows {
			var data any
			_ = json.Unmarshal([]byte(rel.data), &data)
			bySource[source] = append(bySource[source], map[string]any{"relation_id": rel.id, "target_plugin_id": rel.target, "relation_type": rel.typ, "sort_order": rel.order, "relation": data})
		}
	}
	for i := range p.versions {
		rels := bySource[p.versions[i].pid]
		if rels == nil {
			rels = []map[string]any{}
		}
		raw, _ := canonical(rels)
		p.versions[i].relations = string(raw)
	}
}

func (p plan) hash() string {
	var l []string
	for _, x := range p.cats {
		l = append(l, "c:"+x.id+":"+x.name+":"+compact(x.types))
	}
	for _, x := range p.plugins {
		l = append(l, "p:"+x.id+":"+x.phash)
	}
	for _, x := range p.relations {
		l = append(l, fmt.Sprintf("r:%s:%s:%s:%s:%d:%s", x.id, x.source, x.target, x.typ, x.order, compact(x.data)))
	}
	for _, x := range p.versions {
		l = append(l, "v:"+x.id+":"+x.phash+":"+compact(x.relations))
	}
	for _, x := range p.catPlacements {
		l = append(l, fmt.Sprintf("cp:%s:%s:%s:%s:%d", x.id, x.code, x.typ, x.cat, x.order))
	}
	for _, x := range p.placements {
		l = append(l, fmt.Sprintf("pp:%s:%s:%s:%s:%d", x.id, x.code, x.plugin, x.cat, x.order))
	}
	return digestLines(l)
}
func applyRow(ctx context.Context, tx *sql.Tx, rep *Report, table, keyColumn string, key any, exactQuery, insertQuery string, exactArgs, insertArgs []any) error {
	var exact int
	if err := tx.QueryRowContext(ctx, exactQuery, exactArgs...).Scan(&exact); err != nil {
		return err
	}
	if exact == 1 {
		rep.Observed.Existing++
		return nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE `+keyColumn+`=?`, key).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return fmt.Errorf("backfill conflict: %s %v differs from deterministic plan", table, key)
	}
	if _, err := tx.ExecContext(ctx, insertQuery, insertArgs...); err != nil {
		return err
	}
	rep.Observed.Inserted++
	return nil
}

func (r *Runner) apply(ctx context.Context, p plan, rep *Report) error {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, x := range p.cats {
		a := []any{x.id, x.name, x.icon, x.types, x.order, x.status, x.created, x.updated, ntime(x.deleted)}
		e = applyRow(ctx, tx, rep, "plugin_categories", "category_id", x.id, `SELECT COUNT(*) FROM plugin_categories WHERE category_id=? AND name<=>? AND icon_key<=>? AND plugin_types_json<=>CAST(? AS JSON) AND sort_order<=>? AND status<=>? AND created_at<=>? AND updated_at<=>? AND deleted_at<=>?`, `INSERT INTO plugin_categories(category_id,name,icon_key,plugin_types_json,sort_order,status,created_at,updated_at,deleted_at) VALUES(?,?,?,?,?,?,?,?,?)`, a, a)
		if e != nil {
			return e
		}
	}
	versionNames := make(map[string]string, len(p.versions))
	for _, v := range p.versions {
		versionNames[v.id] = v.version
	}
	for _, x := range p.plugins {
		// Historical rows were listed under the pre-listing_state rules, so they
		// backfill as 'published' — the same grandfathering the schema migration
		// applies. Written explicitly rather than left to the column DEFAULT, which
		// is the fail-closed 'draft'.
		a := []any{x.id, x.name, x.typ, x.embedded, nstr(x.cat), x.tags, x.publisher, x.owner, nstr(x.space), x.visibility, "published", x.creator, x.by, nstr(x.botUID), nstr(x.botName), x.manifest, x.pkg, x.mhash, x.phash, x.versionID, nstr(versionNames[x.versionID]), x.status, x.created, x.updated, ntime(x.deleted)}
		e = applyRow(ctx, tx, rep, "plugins", "plugin_id", x.id, `SELECT COUNT(*) FROM plugins WHERE plugin_id=? AND plugin_name<=>? AND plugin_type<=>? AND is_embedded<=>? AND category_id<=>? AND tags_json<=>CAST(? AS JSON) AND publisher<=>? AND owner_uid<=>? AND space_id<=>? AND visibility<=>? AND listing_state<=>? AND creator_name<=>? AND created_by_type<=>? AND created_by_bot_uid<=>? AND created_by_bot_name<=>? AND manifest_json<=>CAST(? AS JSON) AND plugin_json<=>CAST(? AS JSON) AND manifest_hash<=>? AND plugin_hash<=>? AND current_version_id<=>? AND current_version<=>? AND status<=>? AND created_at<=>? AND updated_at<=>? AND deleted_at<=>?`, `INSERT INTO plugins(plugin_id,plugin_name,plugin_type,is_embedded,category_id,tags_json,publisher,owner_uid,space_id,visibility,listing_state,creator_name,created_by_type,created_by_bot_uid,created_by_bot_name,manifest_json,plugin_json,manifest_hash,plugin_hash,current_version_id,current_version,status,created_at,updated_at,deleted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a, a)
		if e != nil {
			return e
		}
		aid := DeterministicID("audit", x.id)
		aa := []any{aid, x.id, "import", x.owner, x.creator, "historical-backfill:" + x.id, nil, x.phash, x.manifest, x.pkg, "deterministic historical backfill", x.created}
		e = applyRow(ctx, tx, rep, "plugin_audit_logs", "audit_log_id", aid, `SELECT COUNT(*) FROM plugin_audit_logs WHERE audit_log_id=? AND plugin_id<=>? AND action<=>? AND operator_id<=>? AND operator_name<=>? AND request_id<=>? AND before_hash<=>? AND after_hash<=>? AND manifest_snapshot_json<=>CAST(? AS JSON) AND plugin_snapshot_json<=>CAST(? AS JSON) AND remark<=>? AND created_at<=>?`, `INSERT INTO plugin_audit_logs(audit_log_id,plugin_id,action,operator_id,operator_name,request_id,before_hash,after_hash,manifest_snapshot_json,plugin_snapshot_json,remark,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, aa, aa)
		if e != nil {
			return e
		}
	}
	for _, x := range p.relations {
		a := []any{x.id, x.source, x.target, x.typ, x.order, x.data, x.status, x.by, x.created, x.updated, ntime(x.deleted)}
		e = applyRow(ctx, tx, rep, "plugin_relations", "relation_id", x.id, `SELECT COUNT(*) FROM plugin_relations WHERE relation_id=? AND source_plugin_id<=>? AND target_plugin_id<=>? AND relation_type<=>? AND sort_order<=>? AND relation_json<=>CAST(? AS JSON) AND status<=>? AND created_by<=>? AND created_at<=>? AND updated_at<=>? AND deleted_at<=>?`, `INSERT INTO plugin_relations(relation_id,source_plugin_id,target_plugin_id,relation_type,sort_order,relation_json,status,created_by,created_at,updated_at,deleted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, a, a)
		if e != nil {
			return e
		}
	}
	for _, x := range p.versions {
		a := []any{x.id, x.pid, x.version, x.manifest, x.pkg, x.mhash, x.phash, x.relations, nstr(x.changelog), x.by, x.created}
		e = applyRow(ctx, tx, rep, "plugin_versions", "version_id", x.id, `SELECT COUNT(*) FROM plugin_versions WHERE version_id=? AND plugin_id<=>? AND version<=>? AND manifest_json<=>CAST(? AS JSON) AND plugin_json<=>CAST(? AS JSON) AND manifest_hash<=>? AND plugin_hash<=>? AND relations_json<=>CAST(? AS JSON) AND changelog<=>? AND created_by<=>? AND created_at<=>?`, `INSERT INTO plugin_versions(version_id,plugin_id,version,manifest_json,plugin_json,manifest_hash,plugin_hash,relations_json,changelog,created_by,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, a, a)
		if e != nil {
			return e
		}
	}
	for _, x := range p.catPlacements {
		a := []any{x.id, x.code, x.typ, x.cat, 1, x.order, x.created, x.updated}
		e = applyRow(ctx, tx, rep, "plugin_category_placements", "placement_id", x.id, `SELECT COUNT(*) FROM plugin_category_placements WHERE placement_id=? AND placement_code<=>? AND plugin_type<=>? AND category_id<=>? AND visible<=>? AND sort_order<=>? AND created_at<=>? AND updated_at<=>?`, `INSERT INTO plugin_category_placements(placement_id,placement_code,plugin_type,category_id,visible,sort_order,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, a, a)
		if e != nil {
			return e
		}
	}
	for _, x := range p.placements {
		a := []any{x.id, x.code, x.plugin, nstr(x.cat), 1, x.order, x.created, x.updated}
		e = applyRow(ctx, tx, rep, "plugin_placements", "placement_id", x.id, `SELECT COUNT(*) FROM plugin_placements WHERE placement_id=? AND placement_code<=>? AND plugin_id<=>? AND category_id<=>? AND visible<=>? AND sort_order<=>? AND created_at<=>? AND updated_at<=>?`, `INSERT INTO plugin_placements(placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, a, a)
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (r *Runner) verify(ctx context.Context, p plan, issues []Issue) (Counts, string, []Issue, error) {
	var c Counts
	var l []string
	for _, x := range p.cats {
		var n, t string
		e := r.db.QueryRowContext(ctx, `SELECT name,plugin_types_json FROM plugin_categories WHERE category_id=?`, x.id).Scan(&n, &t)
		if errors.Is(e, sql.ErrNoRows) {
			c.Missing++
			continue
		}
		if e != nil {
			return c, "", issues, e
		}
		c.Categories++
		l = append(l, "c:"+x.id+":"+n+":"+compact(t))
	}
	for _, x := range p.plugins {
		var typ, h string
		var embedded int
		e := r.db.QueryRowContext(ctx, `SELECT plugin_type,is_embedded,plugin_hash FROM plugins WHERE plugin_id=?`, x.id).Scan(&typ, &embedded, &h)
		if errors.Is(e, sql.ErrNoRows) {
			c.Missing += 2
			continue
		}
		if e != nil {
			return c, "", issues, e
		}
		if typ != x.typ || embedded != x.embedded || h != x.phash {
			c.Conflicts++
			issues = append(issues, Issue{"error", "plugin_conflict", "plugins", x.id, "type, embedded flag, or hash differs"})
		}
		c.Plugins++
		l = append(l, "p:"+x.id+":"+h)
		var pid, action, operator, operatorName, requestID, after, manifest, pkg string
		e = r.db.QueryRowContext(ctx, `SELECT plugin_id,action,operator_id,operator_name,request_id,after_hash,manifest_snapshot_json,plugin_snapshot_json FROM plugin_audit_logs WHERE audit_log_id=?`, DeterministicID("audit", x.id)).Scan(&pid, &action, &operator, &operatorName, &requestID, &after, &manifest, &pkg)
		if errors.Is(e, sql.ErrNoRows) {
			c.Missing++
		} else if e != nil {
			return c, "", issues, e
		} else {
			if pid != x.id || action != "import" || operator != x.owner || operatorName != x.creator || requestID != "historical-backfill:"+x.id || after != x.phash || compact(manifest) != compact(x.manifest) || compact(pkg) != compact(x.pkg) {
				c.Conflicts++
				issues = append(issues, Issue{"error", "audit_conflict", "plugin_audit_logs", DeterministicID("audit", x.id), "identity, provenance, hash, or snapshot differs"})
			}
			c.Audits++
		}
	}
	for _, x := range p.relations {
		var source, target, typ, data string
		var order int
		e := r.db.QueryRowContext(ctx, `SELECT source_plugin_id,target_plugin_id,relation_type,sort_order,relation_json FROM plugin_relations WHERE relation_id=?`, x.id).Scan(&source, &target, &typ, &order, &data)
		if errors.Is(e, sql.ErrNoRows) {
			c.Missing++
			continue
		}
		if e != nil {
			return c, "", issues, e
		}
		if source != x.source || target != x.target || typ != x.typ || order != x.order || compact(data) != compact(x.data) {
			c.Conflicts++
			issues = append(issues, Issue{"error", "relation_conflict", "plugin_relations", x.id, "endpoints, type, order, or payload differs"})
		}
		c.Relations++
		l = append(l, fmt.Sprintf("r:%s:%s:%s:%s:%d:%s", x.id, source, target, typ, order, compact(data)))
	}
	for _, x := range p.versions {
		var h, relations string
		e := r.db.QueryRowContext(ctx, `SELECT plugin_hash,relations_json FROM plugin_versions WHERE version_id=?`, x.id).Scan(&h, &relations)
		if errors.Is(e, sql.ErrNoRows) {
			c.Missing++
			continue
		}
		if e != nil {
			return c, "", issues, e
		}
		if h != x.phash || compact(relations) != compact(x.relations) {
			c.Conflicts++
			issues = append(issues, Issue{"error", "version_conflict", "plugin_versions", x.id, "hash or relation snapshot differs"})
		}
		c.Versions++
		l = append(l, "v:"+x.id+":"+h+":"+compact(relations))
	}
	for _, x := range p.catPlacements {
		var code, typ, cat string
		var order int
		e := r.db.QueryRowContext(ctx, `SELECT placement_code,plugin_type,category_id,sort_order FROM plugin_category_placements WHERE placement_id=?`, x.id).Scan(&code, &typ, &cat, &order)
		if errors.Is(e, sql.ErrNoRows) {
			c.Missing++
			continue
		}
		if e != nil {
			return c, "", issues, e
		}
		if code != x.code || typ != x.typ || cat != x.cat || order != x.order {
			c.Conflicts++
			issues = append(issues, Issue{"error", "category_placement_conflict", "plugin_category_placements", x.id, "placement, type, category, or order differs"})
		}
		c.CategoryPlacements++
		l = append(l, fmt.Sprintf("cp:%s:%s:%s:%s:%d", x.id, code, typ, cat, order))
	}
	for _, x := range p.placements {
		var code, plugin string
		var cat sql.NullString
		var order int
		e := r.db.QueryRowContext(ctx, `SELECT placement_code,plugin_id,category_id,sort_order FROM plugin_placements WHERE placement_id=?`, x.id).Scan(&code, &plugin, &cat, &order)
		if errors.Is(e, sql.ErrNoRows) {
			c.Missing++
			continue
		}
		if e != nil {
			return c, "", issues, e
		}
		// The enrich phase may fill a category the deterministic plan leaves
		// empty (legacy connectors carry no category rows); treat that as equal
		// and project the planned value so plan and observed hashes line up.
		observedCat := cat.String
		if x.cat == "" && observedCat != "" {
			observedCat = x.cat
		}
		if code != x.code || plugin != x.plugin || observedCat != x.cat || order != x.order {
			c.Conflicts++
			issues = append(issues, Issue{"error", "placement_conflict", "plugin_placements", x.id, "placement, plugin, category, or order differs"})
		}
		c.Placements++
		l = append(l, fmt.Sprintf("pp:%s:%s:%s:%s:%d", x.id, code, plugin, observedCat, order))
	}
	return c, digestLines(l), issues, nil
}
func compact(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		b, _ := json.Marshal(v)
		return string(b)
	}
	return s
}
