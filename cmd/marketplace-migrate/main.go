// marketplace-migrate converts Marketplace data in the current database.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/artifacts"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	"github.com/go-sql-driver/mysql"
)

type options struct {
	database, policy, stateFile string
	apply, verify               bool
}

func parseOptions(args []string) (options, error) {
	o := options{}
	f := flag.NewFlagSet("marketplace-migrate", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.database, "database", "", "current database name")
	f.StringVar(&o.policy, "policy", "", "Chinese frontend-dev source selection")
	f.StringVar(&o.stateFile, "state-file", "", "private resume file")
	f.BoolVar(&o.apply, "apply", false, "apply conversion")
	f.BoolVar(&o.verify, "verify", false, "verify a saved run without writes")
	if f.Parse(args) != nil || f.NArg() != 0 || o.database == "" || o.apply && o.verify || (o.apply || o.verify) && o.stateFile == "" {
		return o, fmt.Errorf("invalid_arguments")
	}
	return o, nil
}
func parseDSN(raw, expected string) (*mysql.Config, error) {
	c, e := mysql.ParseDSN(raw)
	if e != nil || expected == "" || c.DBName != expected || c.MultiStatements || c.AllowAllFiles || c.AllowCleartextPasswords || c.AllowFallbackToPlaintext {
		return nil, fmt.Errorf("unsafe_database_configuration")
	}
	c.ParseTime = true
	c.Loc = time.UTC
	if c.Params == nil {
		c.Params = map[string]string{}
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
func openDB(c *mysql.Config) (*sql.DB, error) {
	connector, e := mysql.NewConnector(c)
	if e != nil {
		return nil, e
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(4)
	return db, nil
}
func report(w io.Writer, v any) { _ = json.NewEncoder(w).Encode(v) }
func safeError(w io.Writer, code string) int {
	report(w, map[string]any{"error": code, "complete": false, "install_verified": false})
	return 2
}
func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout)) }
func run(args []string, env func(string) string, out io.Writer) int {
	return runWithDatabase(args, env, out, openDB)
}
func runWithDatabase(args []string, env func(string) string, out io.Writer, connect func(*mysql.Config) (*sql.DB, error)) int {
	o, e := parseOptions(args)
	if e != nil {
		return safeError(out, "invalid_arguments")
	}
	cfg, e := parseDSN(env("MYSQL_DSN"), o.database)
	if e != nil {
		return safeError(out, "unsafe_database_configuration")
	}
	policy, e := loadPolicy(o.policy)
	if e != nil {
		return safeError(out, "policy_invalid")
	}
	store, storageID, e := loadStorage(env)
	if e != nil {
		return safeError(out, "storage_configuration_failed")
	}
	db, e := connect(cfg)
	if e != nil {
		return safeError(out, "database_configuration_failed")
	}
	defer db.Close()
	ctx := context.Background()
	var identity migration.DatabaseIdentity
	if db.QueryRowContext(ctx, "SELECT DATABASE(), @@server_uuid").Scan(&identity.Name, &identity.ServerUUID) != nil || identity.Name != o.database || identity.ServerUUID == "" {
		return safeError(out, "database_identity_failed")
	}
	// Keep a dedicated connection so the advisory lock survives pool operations.
	lock, e := db.Conn(ctx)
	if e != nil {
		return safeError(out, "database_connection_failed")
	}
	defer lock.Close()
	lockName := "marketplace-migrate:" + digest([]byte(identity.Name))[:40]
	var locked int
	if lock.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockName).Scan(&locked) != nil || locked != 1 {
		return safeError(out, "migration_busy")
	}
	defer lock.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", lockName)
	if e = migration.CheckDatabaseSchema(ctx, db); e != nil {
		return safeError(out, "database_schema_not_ready")
	}
	policyJSON, _ := json.Marshal(policy)
	policyID := digest(policyJSON)
	state := runState{Version: 1, Database: identity, StorageID: storageID, PolicyID: policyID}
	loaded := false
	if o.apply || o.verify {
		state, e = loadState(o.stateFile)
		if e == nil {
			loaded = true
			if validateState(state, identity, storageID, policyID) != nil {
				return safeError(out, "run_state_binding_mismatch")
			}
		} else if !os.IsNotExist(e) || o.verify {
			return safeError(out, "run_state_invalid")
		}
	}
	if !loaded {
		rawCfg := cfg.Clone()
		rawCfg.ParseTime = false
		rawDB, err := connect(rawCfg)
		if err != nil {
			return safeError(out, "database_configuration_failed")
		}
		defer rawDB.Close()
		read := func(ctx context.Context, key string) ([]byte, error) {
			r, err := store.GetObject(ctx, key)
			if err != nil {
				return nil, err
			}
			defer r.Close()
			b, err := io.ReadAll(io.LimitReader(r, (100<<20)+1))
			if err == nil && len(b) > 100<<20 {
				err = fmt.Errorf("object_too_large")
			}
			return b, err
		}
		in, issues, err := migration.InputFromDatabase(ctx, rawDB, artifacts.Reader(read))
		if err != nil {
			return safeError(out, "database_input_failed")
		}
		p, err := migration.Build(in, policy)
		issues = append(issues, p.Issues...)
		if err != nil || len(issues) > 0 {
			report(out, map[string]any{"issues": issues, "complete": false, "install_verified": false})
			return 2
		}
		state.Plan = p
		if !o.apply {
			report(out, map[string]any{"plan": p, "dry_run": true, "install_verified": false})
			return 0
		}
		if saveState(o.stateFile, state) != nil {
			return safeError(out, "run_state_save_failed")
		}
	}
	if o.apply {
		if migration.PublishObjects(ctx, state.Plan, store) != nil {
			return safeError(out, "object_publish_failed")
		}
		if result, err := migration.Apply(ctx, db, state.Plan, identity); err != nil {
			report(out, map[string]any{"error": "conversion_apply_failed", "result": result, "complete": false, "install_verified": false})
			return 2
		}
	}
	if migration.VerifyObjects(ctx, state.Plan, store) != nil {
		return safeError(out, "object_verification_failed")
	}
	result, e := migration.Verify(ctx, db, state.Plan, identity)
	report(out, result)
	if e != nil || !result.Complete {
		return 2
	}
	return 0
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
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
	sort.Strings(p.FrontendChineseSourceIDs)
	return p, nil
}
func loadStorage(env func(string) string) (storage.Storage, string, error) {
	driver := env("STORAGE_DRIVER")
	if driver == "" {
		driver = "local"
	}
	switch driver {
	case "local":
		dir := env("LOCAL_STORAGE_DIR")
		if dir == "" {
			dir = "/tmp/marketplace-uploads"
		}
		abs, e := filepath.Abs(dir)
		if e != nil {
			return nil, "", e
		}
		resolved, e := filepath.EvalSymlinks(abs)
		if e != nil {
			return nil, "", e
		}
		return storage.NewLocal(resolved, ""), digest([]byte("local\x00" + resolved)), nil
	case "oss":
		region := env("OSS_REGION")
		if region == "" {
			region = "us-east-1"
		}
		cfg := storage.OSSConfig{Endpoint: env("OSS_ENDPOINT"), Bucket: env("OSS_BUCKET"), AccessKey: env("OSS_ACCESS_KEY"), SecretKey: env("OSS_SECRET_KEY"), Region: region, KeyPrefix: env("OSS_KEY_PREFIX"), PathStyle: env("OSS_PATH_STYLE") != "false"}
		s, e := storage.NewOSS(cfg)
		binding, _ := json.Marshal([]string{driver, cfg.Endpoint, cfg.Bucket, cfg.Region, cfg.KeyPrefix, fmt.Sprint(cfg.PathStyle)})
		return s, digest(binding), e
	default:
		return nil, "", fmt.Errorf("storage_driver_invalid")
	}
}
