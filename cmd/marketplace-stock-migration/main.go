package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/backfill/stockmigration"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migrationauth"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	"github.com/go-sql-driver/mysql"
)

const (
	exitPlanStale       = 3
	exitAuthorization   = 4
	exitBlocked         = 5
	reasonPlanStale     = "plan_stale"
	reasonAuthorization = "authorization_invalid"
	reasonRunConflict   = "run_conflict"
	reasonBlocked       = "blocked"
	maxPlanBytes        = 32 << 20
	maxFixtureBytes     = 32 << 20
)

// reasonError carries the CLI reason and non-zero exit code. It deliberately
// never maps to an HTTP status or a new HTTP error code.
type reasonError struct {
	Reason string
	Code   int
	RunID  string
	Err    error
}

func (e *reasonError) Error() string {
	if e.Err == nil {
		return e.Reason
	}
	return e.Reason + ": " + e.Err.Error()
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		var reason *reasonError
		if errors.As(err, &reason) {
			fmt.Fprintln(os.Stderr, reason.Error())
			os.Exit(reason.Code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: marketplace-stock-migration preview|apply|resume|pin-backfill [flags]")
	}
	switch args[0] {
	case "preview":
		return runPreview(args[1:], out)
	case "apply":
		return runApply(args[1:], out)
	case "resume":
		return runResume(args[1:], out)
	case "pin-backfill":
		return runPinBackfill(args[1:], out)
	default:
		return rejectLegacyFlags(args) // old --apply/--rollback/--all-spaces modes are not commands
	}
}

func runPreview(args []string, out io.Writer) error {
	f := flag.NewFlagSet("preview", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	discover := f.Bool("discover", false, "read-only discovery inventory")
	freeze := f.Bool("freeze", false, "freeze a complete proposed plan")
	proposed := f.String("proposed-plan", "", "operator-completed proposed plan JSON")
	sourcePlugin := f.String("source-plugin-id", "", "one database source root")
	fixture := f.String("fixture", "", "offline dataset JSON (discover-only test aid)")
	artifactDir := f.String("artifact-dir", "", "explicit operator-authorized local object root (read only)")
	limit := f.Int("limit", 10000, "maximum rows per inventory table")
	if err := parseStrict(f, args); err != nil {
		return err
	}
	if *discover == *freeze {
		return fmt.Errorf("preview requires exactly one of --discover or --freeze")
	}
	if *freeze && strings.TrimSpace(*proposed) == "" {
		return fmt.Errorf("--freeze requires --proposed-plan")
	}
	if *freeze && *fixture != "" {
		return fmt.Errorf("--fixture cannot freeze; use the database source")
	}
	resolver, err := resolverFromFlag(*artifactDir)
	if err != nil {
		return err
	}
	if *fixture != "" {
		d, err := readFixture(*fixture, resolver)
		if err != nil {
			return err
		}
		root := strings.TrimSpace(*sourcePlugin)
		if root == "" {
			return fmt.Errorf("--fixture discovery requires --source-plugin-id")
		}
		preview, err := stockmigration.PreviewDiscovery(d, root)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(preview)
	}
	proposal, err := readPlan(*proposed)
	if err != nil && *freeze {
		return err
	}
	db, err := openDatabase()
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	d, err := stockmigration.ReadWithResolver(ctx, db, *limit, resolver)
	if err != nil {
		return err
	}
	if *discover {
		if *sourcePlugin != "" {
			preview, err := stockmigration.PreviewDiscovery(d, *sourcePlugin)
			if err != nil {
				return err
			}
			return writeJSON(out, preview)
		}
		roots := map[string]bool{}
		for _, resource := range d.Resources {
			if resource.Plugin.Type == "expert" || resource.Plugin.Type == "expert_team" || resource.Plugin.Type == "skill" {
				roots[resource.Plugin.ID] = true
			}
		}
		previews := []stockmigration.MigrationPreview{}
		for root := range roots {
			preview, err := stockmigration.PreviewDiscovery(d, root)
			if err != nil {
				continue
			}
			previews = append(previews, preview)
		}
		return writeJSON(out, map[string]any{"phase": "discovery", "previews": previews})
	}
	frozen, digest, err := stockmigration.Freeze(d, proposal)
	if err != nil {
		return err
	}
	preview := stockmigration.MigrationPreview{
		Phase: "frozen", InputDigest: frozen.InputDigest, SourceKind: frozen.SourceKind,
		SourceIdentity: frozen.SourceIdentity, SourceScope: frozen.SourceScope,
		SourcePluginID: frozen.SourcePluginID, SourceVersionID: frozen.SourceVersionID,
		ExpectedEditRevision: frozen.ExpectedEditRevision,
		CanApply:             true, Plan: &frozen, PlanDigest: digest,
	}
	return writeJSON(out, preview)
}

func runApply(args []string, out io.Writer) error {
	f := flag.NewFlagSet("apply", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	planPath := f.String("plan", "", "frozen plan JSON")
	planDigest := f.String("plan-digest", "", "SHA-256(JCS(plan)) lowercase hex")
	authorizationPath := f.String("authorization", "", "offline authorization artifact")
	artifactDir := f.String("artifact-dir", "", "explicit operator-authorized local object root (read only)")
	limit := f.Int("limit", 10000, "maximum rows per inventory table")
	if err := parseStrict(f, args); err != nil {
		return err
	}
	if strings.TrimSpace(*planPath) == "" || strings.TrimSpace(*planDigest) == "" || strings.TrimSpace(*authorizationPath) == "" {
		return fmt.Errorf("apply requires --plan, --plan-digest and --authorization")
	}
	if !isLowerHex64(*planDigest) {
		return returnReason(out, reasonPlanStale, exitPlanStale, "", fmt.Errorf("--plan-digest must be lowercase SHA-256 hex"))
	}
	plan, err := readPlan(*planPath)
	if err != nil {
		return err
	}
	computed, err := stockmigration.PlanDigest(plan)
	if err != nil || computed != *planDigest {
		return returnReason(out, reasonPlanStale, exitPlanStale, "", fmt.Errorf("plan digest does not match the frozen plan"))
	}
	if plan.SourceKind != stockmigration.PlanSourceDB || plan.SourcePluginID == "" || plan.SourceIdentity == "" {
		return returnReason(out, reasonPlanStale, exitPlanStale, "", fmt.Errorf("unsupported or incomplete frozen source"))
	}
	trust, envelope, err := readTrustAndAuthorization(*authorizationPath)
	if err != nil {
		return returnReason(out, reasonAuthorization, exitAuthorization, "", err)
	}
	payload, err := trust.Verify(envelope, migrationauth.Expected{
		SourceScope: plan.SourceScope, SourceIdentity: plan.SourceIdentity, PlanDigest: *planDigest,
	}, time.Now().UTC())
	if err != nil {
		return returnReason(out, reasonAuthorization, exitAuthorization, "", err)
	}
	resolver, err := resolverFromFlag(*artifactDir)
	if err != nil {
		return err
	}
	db, err := openDatabase()
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	d, err := stockmigration.ReadWithResolver(ctx, db, *limit, resolver)
	if err != nil {
		return err
	}
	// Re-freeze the current source with the frozen plan as the proposal. Any
	// hand edit, source change, file change or Connector effective drift changes
	// the digest and is refused before a run or business row is created.
	recomputedPlan, recomputedDigest, err := stockmigration.Freeze(d, plan)
	if err != nil || recomputedDigest != *planDigest {
		return returnReason(out, reasonPlanStale, exitPlanStale, "", fmt.Errorf("source or plan changed; re-run preview"))
	}
	run, _, err := stockmigration.OpenRun(ctx, db, stockmigration.OpenRunOptions{
		Plan: recomputedPlan, PlanDigest: recomputedDigest, Environment: trust.EnvironmentID,
		Authorization: *payload, Signature: envelope.Signature,
	})
	if errors.Is(err, stockmigration.ErrRunAuthorization) || errors.Is(err, stockmigration.ErrAuthorizationReused) {
		return returnReason(out, reasonAuthorization, exitAuthorization, "", err)
	}
	if err != nil {
		return returnReason(out, reasonRunConflict, exitBlocked, "", err)
	}
	return executeAndReport(ctx, db, run, d, trust, payload, envelope.Signature, out, false)
}

func runResume(args []string, out io.Writer) error {
	f := flag.NewFlagSet("resume", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	runID := f.String("run-id", "", "persisted run id")
	authorizationPath := f.String("authorization", "", "offline authorization artifact")
	artifactDir := f.String("artifact-dir", "", "explicit operator-authorized local object root (read only)")
	limit := f.Int("limit", 10000, "maximum rows per inventory table")
	if err := parseStrict(f, args); err != nil {
		return err
	}
	if strings.TrimSpace(*runID) == "" || strings.TrimSpace(*authorizationPath) == "" {
		return fmt.Errorf("resume requires --run-id and --authorization")
	}
	trust, envelope, err := readTrustAndAuthorization(*authorizationPath)
	if err != nil {
		return returnReason(out, reasonAuthorization, exitAuthorization, "", err)
	}
	db, err := openDatabase()
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	run, err := stockmigration.LoadRun(ctx, db, *runID)
	if err != nil {
		return returnReason(out, reasonRunConflict, exitBlocked, "", err)
	}
	if run.Status == stockmigration.RunBlocked && run.BlockedReason != nil && *run.BlockedReason == reasonPlanStale {
		return returnReason(out, reasonPlanStale, exitPlanStale, run.RunID, fmt.Errorf("run is blocked on plan_stale; re-run preview"))
	}
	payload, err := trust.Verify(envelope, migrationauth.Expected{
		SourceScope: run.SourceScope, SourceIdentity: run.SourceIdentity,
		PlanDigest: run.PlanDigest, BatchID: run.BatchID,
	}, time.Now().UTC())
	if err != nil {
		_ = stockmigration.SetRunStatus(ctx, db, run.RunID, stockmigration.RunBlocked, reasonAuthorization, time.Now().UTC())
		return returnReason(out, reasonAuthorization, exitAuthorization, run.RunID, err)
	}
	if run.Status == stockmigration.RunCompleted {
		return writeJSON(out, runOutput(run, nil, ""))
	}
	if err = stockmigration.BindAuthorization(ctx, db, run, *payload, envelope.Signature, time.Now().UTC()); err != nil {
		_ = stockmigration.SetRunStatus(ctx, db, run.RunID, stockmigration.RunBlocked, reasonAuthorization, time.Now().UTC())
		return returnReason(out, reasonAuthorization, exitAuthorization, run.RunID, err)
	}
	resolver, err := resolverFromFlag(*artifactDir)
	if err != nil {
		return err
	}
	d, err := stockmigration.ReadWithResolver(ctx, db, *limit, resolver)
	if err != nil {
		return err
	}
	if rootMap, _ := stockmigration.FindNode(ctx, db, run.RunID, stockmigration.RootNodeKey); rootMap == nil {
		// No root has been staged yet: the source is still the original input and
		// must still match the frozen plan.
		_, digest, freezeErr := stockmigration.Freeze(d, run.Plan)
		if freezeErr != nil || digest != run.PlanDigest {
			_ = stockmigration.SetRunStatus(ctx, db, run.RunID, stockmigration.RunBlocked, reasonPlanStale, time.Now().UTC())
			return returnReason(out, reasonPlanStale, exitPlanStale, run.RunID, fmt.Errorf("source or plan changed; re-run preview"))
		}
	} else {
		// Root content is our own staged/committed output. The persisted plan is
		// authoritative; execution still re-derives labels and validates every
		// node before touching it.
		d.Labels = stockmigration.PlanLabels(run.Plan)
	}
	return executeAndReport(ctx, db, run, d, trust, payload, envelope.Signature, out, true)
}

func executeAndReport(ctx context.Context, db *sql.DB, run *stockmigration.MigrationRun, d stockmigration.Dataset, trust *migrationauth.TrustConfig, payload *migrationauth.Payload, signature string, out io.Writer, _ bool) error {
	status, reason, err := stockmigration.ExecuteRun(ctx, db, run, d, stockmigration.ExecuteOptions{
		ActorID: payload.ActorID, ActorName: payload.ActorID, Now: time.Now().UTC(),
	})
	nodes, _ := stockmigration.ListNode(ctx, db, run.RunID)
	output := runOutput(run, nodes, reason)
	if run, loadErr := stockmigration.LoadRun(ctx, db, run.RunID); loadErr == nil {
		output = runOutput(run, nodes, reason)
	}
	switch status {
	case stockmigration.RunCompleted:
		return writeJSON(out, output)
	case stockmigration.RunWaitingReview:
		return writeJSON(out, output)
	default:
		return returnReasonWithRun(out, reason, exitBlocked, run.RunID, err, output)
	}
}

func runOutput(run *stockmigration.MigrationRun, nodes []stockmigration.MigrationRunNode, reason string) map[string]any {
	if reason == "" && run.BlockedReason != nil {
		reason = *run.BlockedReason
	}
	return map[string]any{
		"run_id":      run.RunID,
		"batch_id":    run.BatchID,
		"status":      string(run.Status),
		"plan_digest": run.PlanDigest,
		"source":      run.SourceIdentity,
		"reason":      reason,
		"mappings":    nodes,
	}
}

func returnReason(out io.Writer, reason string, code int, runID string, err error) error {
	return returnReasonWithRun(out, reason, code, runID, err, nil)
}

func returnReasonWithRun(out io.Writer, reason string, code int, runID string, err error, output map[string]any) error {
	if output == nil {
		output = map[string]any{"status": "blocked", "reason": reason}
		if runID != "" {
			output["run_id"] = runID
		}
	}
	_ = writeJSON(out, output)
	return &reasonError{Reason: reason, Code: code, RunID: runID, Err: err}
}

func readTrustAndAuthorization(path string) (*migrationauth.TrustConfig, migrationauth.Envelope, error) {
	trust, err := migrationauth.TrustFromEnv()
	if err != nil {
		return nil, migrationauth.Envelope{}, err
	}
	raw, err := readBoundedFile(path, maxPlanBytes)
	if err != nil {
		return nil, migrationauth.Envelope{}, err
	}
	envelope, err := migrationauth.ParseEnvelope(raw)
	if err != nil {
		return nil, migrationauth.Envelope{}, err
	}
	return trust, envelope, nil
}

func openDatabase() (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(os.Getenv("MARKETPLACE_STOCK_MIGRATION_DSN"))
	if err != nil || cfg.User == "" || cfg.DBName == "" || cfg.MultiStatements || cfg.AllowAllFiles || len(cfg.Params) > 0 {
		return nil, fmt.Errorf("invalid stock migration database configuration")
	}
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("database connection failed")
	}
	db.SetMaxOpenConns(4)
	return db, nil
}

func resolverFromFlag(dir string) (stockmigration.SkillResolver, error) {
	if dir == "" {
		return nil, nil
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("artifact root must be an existing absolute directory")
	}
	store := storage.NewLocal(dir, "")
	return func(ctx context.Context, p *model.Plugin) (map[string]string, error) {
		return pluginsvc.ResolveMigrationSkillFiles(ctx, store, p)
	}, nil
}

func readFixture(path string, resolver stockmigration.SkillResolver) (stockmigration.Dataset, error) {
	raw, err := readBoundedFile(path, maxFixtureBytes)
	if err != nil {
		return stockmigration.Dataset{}, err
	}
	var data stockmigration.Dataset
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&data) != nil {
		return stockmigration.Dataset{}, fmt.Errorf("fixture decode failed")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return stockmigration.Dataset{}, fmt.Errorf("fixture decode failed")
	}
	return stockmigration.Hydrate(context.Background(), data, resolver), nil
}

func readPlan(path string) (stockmigration.MigrationPlan, error) {
	raw, err := readBoundedFile(path, maxPlanBytes)
	if err != nil {
		return stockmigration.MigrationPlan{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var plan stockmigration.MigrationPlan
	if err := decoder.Decode(&plan); err != nil {
		return stockmigration.MigrationPlan{}, fmt.Errorf("plan decode failed")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return stockmigration.MigrationPlan{}, fmt.Errorf("plan decode failed")
	}
	return plan, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("file read failed")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, fmt.Errorf("file exceeds limit")
	}
	return raw, nil
}

func parseStrict(f *flag.FlagSet, args []string) error {
	if err := f.Parse(args); err != nil {
		return rejectLegacyFlags(args)
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			name := strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-")
			switch name {
			case "apply", "rollback", "all-spaces", "g4-stop-on-exceptions-approved", "batch-id", "root-id", "expected-digest", "actor":
				return fmt.Errorf("unsafe or retired flag --%s is not accepted", name)
			}
		}
	}
	return nil
}

func rejectLegacyFlags(args []string) error {
	for _, arg := range args {
		name := strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-")
		switch name {
		case "apply", "rollback", "all-spaces", "g4-stop-on-exceptions-approved", "batch-id", "root-id", "expected-digest", "actor":
			return fmt.Errorf("unsafe or retired flag --%s is not accepted", name)
		}
	}
	return fmt.Errorf("invalid stock migration arguments")
}

func isLowerHex64(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
