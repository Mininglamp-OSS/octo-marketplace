// marketplace-migrate performs offline planning and explicit isolated rehearsals.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	dbschema "github.com/Mininglamp-OSS/octo-marketplace/internal/db"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/artifacts"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/snapshot"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	"github.com/go-sql-driver/mysql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type options struct {
	command, dir, policy, namespace, expected, targetStorage, fingerprint string
	apply                                                                 bool
}

func parseOptions(args []string) (options, error) {
	o := options{}
	if len(args) == 0 {
		return o, fmt.Errorf("invalid_arguments")
	}
	o.command = args[0]
	if o.command != "snapshot" && o.command != "plan" && o.command != "rehearse" && o.command != "verify" {
		return o, fmt.Errorf("invalid_arguments")
	}
	f := flag.NewFlagSet(o.command, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.dir, "dir", "", "")
	f.StringVar(&o.policy, "policy", "", "")
	switch o.command {
	case "snapshot":
		f.StringVar(&o.fingerprint, "source-fingerprint", "", "")
	case "rehearse", "verify":
		f.StringVar(&o.namespace, "namespace", "", "")
		f.StringVar(&o.expected, "expected-snapshot", "", "")
		f.StringVar(&o.targetStorage, "target-storage-dir", "", "")
		if o.command == "rehearse" {
			f.BoolVar(&o.apply, "apply", false, "")
		}
	}
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || o.dir == "" {
		return o, fmt.Errorf("invalid_arguments")
	}
	if o.command == "snapshot" && o.fingerprint == "" {
		return o, fmt.Errorf("source_fingerprint_required")
	}
	if o.command == "rehearse" || o.command == "verify" {
		if o.namespace != "dmwork-test" {
			return o, fmt.Errorf("target_namespace_rejected")
		}
		if len(o.expected) != 64 {
			return o, fmt.Errorf("expected_snapshot_required")
		}
		if _, e := hex.DecodeString(o.expected); e != nil {
			return o, fmt.Errorf("expected_snapshot_required")
		}
		if o.targetStorage == "" {
			return o, fmt.Errorf("target_storage_required")
		}
	}
	return o, nil
}
func parseDSN(raw string, target bool) (*mysql.Config, error) {
	c, e := mysql.ParseDSN(raw)
	if e != nil || c.DBName == "" || c.MultiStatements || c.AllowAllFiles || c.AllowCleartextPasswords || c.AllowFallbackToPlaintext {
		return nil, fmt.Errorf("unsafe_database_configuration")
	}
	if target && (!strings.HasPrefix(c.DBName, "octo_marketplace_rehearsal_") || len(c.DBName) <= len("octo_marketplace_rehearsal_") || strings.Contains(strings.ToLower(c.DBName), "prod")) {
		return nil, fmt.Errorf("target_database_rejected")
	}
	c.ParseTime = target
	c.Loc = time.UTC
	// Loc only controls Go time values; TIMESTAMP conversion uses the server
	// session. Override any caller time_zone for every migration connection.
	if c.Params == nil {
		c.Params = make(map[string]string)
	}
	c.Params["time_zone"] = "'+00:00'"
	if c.Timeout == 0 {
		c.Timeout = 15 * time.Second
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = 5 * time.Minute
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = 5 * time.Minute
	}
	return c, nil
}

// restoreConfig keeps MySQL DATETIME/DATE bytes intact for snapshot checks;
// Apply/Verify retain their independent typed-time connector.
func restoreConfig(typed *mysql.Config) *mysql.Config {
	raw := typed.Clone()
	raw.ParseTime = false
	return raw
}
func openDB(c *mysql.Config) (*sql.DB, error) {
	connector, e := mysql.NewConnector(c)
	if e != nil {
		return nil, fmt.Errorf("database_configuration_failed")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(4)
	return db, nil
}
func report(out io.Writer, value any) { _ = json.NewEncoder(out).Encode(value) }
func safeError(out io.Writer, code string) int {
	report(out, map[string]any{"error": code, "complete": false, "install_verified": false})
	return 2
}
func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout)) }
func run(args []string, env func(string) string, out io.Writer) int {
	o, e := parseOptions(args)
	if e != nil {
		return safeError(out, e.Error())
	}
	ctx := context.Background()
	if o.command == "rehearse" || o.command == "verify" {
		for _, k := range []string{"MIGRATION_SOURCE_DSN", "MIGRATION_SOURCE_OSS_ACCESS_KEY", "MIGRATION_SOURCE_OSS_SECRET_KEY"} {
			if env(k) != "" {
				return safeError(out, "source_credentials_present")
			}
		}
		if strings.EqualFold(env("SKIP_MIGRATION"), "true") {
			return safeError(out, "schema_migration_disabled")
		}
	}
	if o.command == "snapshot" {
		c, e := parseDSN(env("MIGRATION_SOURCE_DSN"), false)
		if e != nil {
			return safeError(out, "unsafe_source_database_configuration")
		}
		db, e := openDB(c)
		if e != nil {
			return safeError(out, "source_database_open_failed")
		}
		defer db.Close()
		opener, e := sourceOpener(env)
		if e != nil {
			return safeError(out, "source_storage_configuration_failed")
		}
		b, e := snapshot.Export(ctx, db, o.dir, o.fingerprint)
		if e != nil {
			return safeError(out, "snapshot_export_failed")
		}
		b, e = artifacts.Collect(ctx, b, opener, artifacts.DefaultObjectLimits())
		if e != nil {
			return safeError(out, "snapshot_object_collection_failed")
		}
		report(out, map[string]any{"snapshot_id": b.ID, "tables": len(b.Manifest.Tables), "objects": len(b.Manifest.Objects) - 1, "database_objects_atomic": false, "complete": true, "install_verified": false})
		return 0
	}
	b, e := snapshot.Load(o.dir)
	if e != nil {
		return safeError(out, "snapshot_integrity_failed")
	}
	if o.expected != "" && b.ID != o.expected {
		return safeError(out, "snapshot_identity_mismatch")
	}
	read, e := artifacts.BundleReader(b)
	if e != nil {
		return safeError(out, "snapshot_object_index_failed")
	}
	policy, e := loadPolicy(o.policy)
	if e != nil {
		return safeError(out, "policy_invalid")
	}
	in, issues, e := migration.InputFromSnapshot(ctx, b, read)
	if e != nil {
		return safeError(out, "snapshot_input_failed")
	}
	if e = migration.ValidateSnapshotReferences(b); e != nil {
		return safeError(out, "snapshot_reference_validation_failed")
	}
	p, buildErr := migration.Build(in, policy)
	issues = append(issues, p.Issues...)
	if len(issues) > 0 || buildErr != nil {
		report(out, map[string]any{"snapshot_id": b.ID, "issues": issues, "source_resources": len(in.Resources), "planned_nodes": len(p.Nodes), "source_mappings": len(p.Mappings), "complete": false, "install_verified": false})
		return 2
	}
	if o.command == "plan" || o.command == "rehearse" && !o.apply {
		report(out, struct {
			Plan            migration.Plan `json:"plan"`
			DryRun          bool           `json:"dry_run"`
			InstallVerified bool           `json:"install_verified"`
		}{p, true, false})
		return 0
	}
	c, e := parseDSN(env("MIGRATION_TARGET_DSN"), true)
	if e != nil {
		return safeError(out, "unsafe_target_database_configuration")
	}
	if storageOverlaps(o.targetStorage, o.dir) {
		return safeError(out, "target_storage_overlaps_snapshot")
	}
	if e = validateTargetStorage(o.targetStorage, b.ID); e != nil {
		return safeError(out, "target_storage_rejected")
	}
	db, e := openDB(c)
	if e != nil {
		return safeError(out, "target_database_open_failed")
	}
	defer db.Close()
	if e = verifyTargetIdentity(ctx, db, c.DBName, b); e != nil {
		return safeError(out, "target_database_identity_failed")
	}
	if o.command == "verify" {
		marker, markerErr := readStorageMarker(o.targetStorage)
		if markerErr != nil || marker.SnapshotID != b.ID || marker.PlanID != p.ID {
			return safeError(out, "target_storage_identity_failed")
		}
		if e = verifyProgress(ctx, db, b, p, o.targetStorage); e != nil {
			return safeError(out, "rehearsal_identity_failed")
		}
		if e = verifyObjects(ctx, b, p, o.targetStorage); e != nil {
			return safeError(out, "target_objects_failed")
		}
		r, e := migration.Verify(ctx, db, p)
		report(out, r)
		if e != nil || !r.Complete {
			return 2
		}
		return 0
	}
	rawDB, rawErr := openDB(restoreConfig(c))
	if rawErr != nil {
		return safeError(out, "target_restore_database_open_failed")
	}
	defer rawDB.Close()
	if e = verifyTargetIdentity(ctx, rawDB, c.DBName, b); e != nil {
		return safeError(out, "target_restore_database_identity_failed")
	}
	if e = rehearse(ctx, rawDB, db, b, p, o.targetStorage); e != nil {
		report(out, map[string]any{"error": e.Error(), "snapshot_id": b.ID, "run_id": p.ID, "source_resources": len(in.Resources), "planned_nodes": len(p.Nodes), "source_mappings": len(p.Mappings), "complete": false, "install_verified": false})
		return 2
	}
	r, e := migration.Verify(ctx, db, p)
	report(out, r)
	if e != nil || !r.Complete {
		return 2
	}
	return 0
}
func loadPolicy(name string) (migration.Policy, error) {
	p := migration.Policy{}
	if name == "" {
		return p, nil
	}
	f, e := os.Open(name)
	if e != nil {
		return p, e
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if e = d.Decode(&p); e != nil {
		return p, e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return p, fmt.Errorf("policy_invalid")
	}
	return p, nil
}
func sourceOpener(env func(string) string) (artifacts.ObjectOpener, error) {
	switch env("MIGRATION_SOURCE_STORAGE_DRIVER") {
	case "local":
		dir := env("MIGRATION_SOURCE_LOCAL_DIR")
		if dir == "" {
			return nil, fmt.Errorf("local_root_required")
		}
		return func(_ context.Context, key string) (io.ReadCloser, error) { return artifacts.OpenLocalObject(dir, key) }, nil
	case "oss":
		s, e := storage.NewOSS(storage.OSSConfig{Endpoint: env("MIGRATION_SOURCE_OSS_ENDPOINT"), Bucket: env("MIGRATION_SOURCE_OSS_BUCKET"), AccessKey: env("MIGRATION_SOURCE_OSS_ACCESS_KEY"), SecretKey: env("MIGRATION_SOURCE_OSS_SECRET_KEY"), Region: env("MIGRATION_SOURCE_OSS_REGION"), KeyPrefix: env("MIGRATION_SOURCE_OSS_KEY_PREFIX"), PathStyle: env("MIGRATION_SOURCE_OSS_PATH_STYLE") == "true"})
		if e != nil {
			return nil, fmt.Errorf("oss_configuration_failed")
		}
		return s.GetObject, nil
	default:
		return nil, fmt.Errorf("source_storage_driver_required")
	}
}

const progressTable = "marketplace_rehearsal_progress"

func storageIdentity(dir string) string {
	abs, _ := filepath.Abs(dir)
	h := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(h[:])
}
func validateTargetStorage(dir, id string) error {
	abs, e := filepath.Abs(dir)
	if len(id) < 12 || e != nil || !strings.Contains(filepath.Base(abs), "rehearsal") || !strings.Contains(filepath.Base(abs), id[:12]) {
		return fmt.Errorf("target_storage_rejected")
	}
	st, e := os.Lstat(abs)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("target_storage_rejected")
	}
	entries, e := os.ReadDir(abs)
	if e != nil {
		return fmt.Errorf("target_storage_rejected")
	}
	marker := false
	for _, entry := range entries {
		if entry.Name() == storageMarker {
			marker = true
		}
	}
	if len(entries) > 0 && !marker {
		return fmt.Errorf("target_storage_unowned")
	}
	if marker {
		m, e := readStorageMarker(abs)
		if e != nil || m.SnapshotID != id {
			return fmt.Errorf("target_storage_identity_failed")
		}
	}
	return nil
}

const storageMarker = ".marketplace-rehearsal.json"

type storageReceipt struct{ SnapshotID, PlanID string }

func storageOverlaps(a, b string) bool {
	var e error
	a, e = filepath.EvalSymlinks(a)
	if e != nil {
		return true
	}
	b, e = filepath.EvalSymlinks(b)
	if e != nil {
		return true
	}
	a, e = filepath.Abs(a)
	if e != nil {
		return true
	}
	b, e = filepath.Abs(b)
	if e != nil {
		return true
	}
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		rel, e := filepath.Rel(pair[0], pair[1])
		if e != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
func readStorageMarker(dir string) (storageReceipt, error) {
	m := storageReceipt{}
	r, e := artifacts.OpenLocalObject(dir, storageMarker)
	if e != nil {
		return m, e
	}
	defer r.Close()
	raw, e := io.ReadAll(io.LimitReader(r, 4097))
	if e != nil || len(raw) > 4096 || json.Unmarshal(raw, &m) != nil {
		return m, fmt.Errorf("storage_identity_failed")
	}
	return m, nil
}
func ensureStorageMarker(dir string, b snapshot.Bundle, p migration.Plan) error {
	if m, e := readStorageMarker(dir); e == nil {
		if m.SnapshotID != b.ID || m.PlanID != p.ID {
			return fmt.Errorf("storage_identity_failed")
		}
		return nil
	}
	raw, _ := json.Marshal(storageReceipt{b.ID, p.ID})
	h := sha256.Sum256(raw)
	return artifacts.WriteLocalObject(dir, storageMarker, bytes.NewReader(raw), int64(len(raw)), hex.EncodeToString(h[:]))
}
func verifyProgress(ctx context.Context, db *sql.DB, b snapshot.Bundle, p migration.Plan, storageDir string) error {
	var sid, pid, store string
	if db.QueryRowContext(ctx, "SELECT snapshot_id, plan_id, storage_identity FROM "+progressTable+" WHERE slot = 1").Scan(&sid, &pid, &store) != nil || sid != b.ID || pid != p.ID || store != storageIdentity(storageDir) {
		return fmt.Errorf("rehearsal_identity_failed")
	}
	return nil
}
func ensureRestored(ctx context.Context, db *sql.DB, b snapshot.Bundle, p migration.Plan, dir string) error {
	var count int
	if db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", progressTable).Scan(&count) != nil {
		return fmt.Errorf("target_inspection_failed")
	}
	if count > 0 {
		return verifyProgress(ctx, db, b, p, dir)
	}
	if e := snapshot.Restore(ctx, db, b); e != nil {
		return fmt.Errorf("snapshot_restore_failed")
	}
	if _, e := db.ExecContext(ctx, "CREATE TABLE "+progressTable+" (slot TINYINT PRIMARY KEY, snapshot_id CHAR(64) NOT NULL, plan_id CHAR(64) NOT NULL, storage_identity CHAR(64) NOT NULL) ENGINE=InnoDB AS SELECT 1 AS slot, ? AS snapshot_id, ? AS plan_id, ? AS storage_identity", b.ID, p.ID, storageIdentity(dir)); e != nil {
		return fmt.Errorf("progress_identity_failed")
	}
	return nil
}
func rehearse(ctx context.Context, rawDB, db *sql.DB, b snapshot.Bundle, p migration.Plan, dir string) error {
	c, e := db.Conn(ctx)
	if e != nil {
		return fmt.Errorf("target_connection_failed")
	}
	defer c.Close()
	var locked int
	if c.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", "marketplace-rehearsal").Scan(&locked) != nil || locked != 1 {
		return fmt.Errorf("rehearsal_busy")
	}
	defer c.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", "marketplace-rehearsal")
	if e = ensureRestored(ctx, rawDB, b, p, dir); e != nil {
		return e
	}
	if e = ensureStorageMarker(dir, b, p); e != nil {
		return fmt.Errorf("target_storage_identity_failed")
	}
	if _, e = dbschema.RunMigrations(db); e != nil {
		return fmt.Errorf("schema_upgrade_failed")
	}
	if e = artifacts.CopyBundleObjects(ctx, b, dir); e != nil {
		return fmt.Errorf("original_objects_copy_failed")
	}
	for _, n := range p.Nodes {
		for _, f := range n.Files {
			if f.ObjectKey == "" {
				continue
			}
			h := sha256.Sum256(f.Data)
			if e = artifacts.WriteLocalObject(dir, f.ObjectKey, bytes.NewReader(f.Data), int64(len(f.Data)), hex.EncodeToString(h[:])); e != nil {
				return fmt.Errorf("converted_objects_copy_failed")
			}
		}
	}
	if _, e = migration.Apply(ctx, db, p); e != nil {
		return fmt.Errorf("conversion_apply_failed")
	}
	return nil
}
func verifyObjects(ctx context.Context, b snapshot.Bundle, p migration.Plan, dir string) error {
	read, e := artifacts.BundleReader(b)
	if e != nil {
		return e
	}

	// CopyBundleObjects is immutable: existing bytes are only read and checked.
	// Verification must never create missing objects; check every source key first.
	for _, t := range b.Manifest.Tables {
		e = snapshot.ReadTable(b, t.Name, func(cols []string, vals []*[]byte) error {
			for i, v := range vals {
				if v == nil {
					continue
				}
				keys, e := artifacts.ReferencedKeys(cols[i], *v)
				if e != nil {
					return e
				}
				for _, key := range keys {
					expected, e := read(ctx, key)
					if e != nil {
						return e
					}
					if e = verifyLocalBytes(dir, key, expected); e != nil {
						return e
					}
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	for _, n := range p.Nodes {
		for _, f := range n.Files {
			if f.ObjectKey != "" {
				if e = verifyLocalBytes(dir, f.ObjectKey, f.Data); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
func verifyLocalBytes(dir, key string, expected []byte) error {
	r, e := artifacts.OpenLocalObject(dir, key)
	if e != nil {
		return e
	}
	defer r.Close()
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(r, int64(len(expected))+1))
	want := sha256.Sum256(expected)
	if e != nil || n != int64(len(expected)) || hex.EncodeToString(h.Sum(nil)) != hex.EncodeToString(want[:]) {
		return fmt.Errorf("target_object_mismatch")
	}
	return nil
}

func verifyTargetIdentity(ctx context.Context, db *sql.DB, expected string, b snapshot.Bundle) error {
	var actual, server string
	if db.QueryRowContext(ctx, "SELECT DATABASE(), @@server_uuid").Scan(&actual, &server) != nil || actual != expected || actual == b.Manifest.Source.DBName && server == b.Manifest.Source.ServerUUID {
		return fmt.Errorf("target_identity_failed")
	}
	return nil
}
