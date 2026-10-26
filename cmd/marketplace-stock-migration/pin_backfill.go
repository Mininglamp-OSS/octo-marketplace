package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/backfill/pinbackfill"
)

// pinBackfillPreviewFile is a legacy/compatibility wrapper accepted by apply.
// New previews write a bare Plan document whose own plan_digest field records
// the digest; both shapes are verified by readPinBackfillPlan.
type pinBackfillPreviewFile struct {
	PlanDigest string           `json:"plan_digest"`
	Plan       pinbackfill.Plan `json:"plan"`
}

func runPinBackfill(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: marketplace-stock-migration pin-backfill preview|apply [flags]")
	}
	switch args[0] {
	case "preview":
		return runPinBackfillPreview(args[1:], out)
	case "apply":
		return runPinBackfillApply(args[1:], out)
	default:
		return fmt.Errorf("pin-backfill requires preview or apply")
	}
}

func runPinBackfillPreview(args []string, out io.Writer) error {
	f := flag.NewFlagSet("pin-backfill preview", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	output := f.String("output", "", "operator-readable plan JSON destination")
	limit := f.Int("limit", 10000, "maximum released source versions scanned")
	if err := parseStrict(f, args); err != nil {
		return err
	}
	if strings.TrimSpace(*output) == "" {
		return fmt.Errorf("pin-backfill preview requires --output")
	}
	db, err := openDatabase()
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	plan, err := pinbackfill.Read(ctx, db, *limit)
	if err != nil {
		return err
	}
	digest, err := pinbackfill.Digest(plan)
	if err != nil {
		return fmt.Errorf("pin backfill plan digest failed")
	}
	plan.PlanDigest = digest
	if err = writePinBackfillJSONFile(*output, plan); err != nil {
		return err
	}
	return writeJSON(out, map[string]any{
		"phase":         "preview",
		"plan_digest":   digest,
		"output":        *output,
		"source_count":  len(plan.Sources),
		"blocked_count": len(plan.Blocked),
		"pin_count":     pinBackfillPinCount(plan),
	})
}

func runPinBackfillApply(args []string, out io.Writer) error {
	f := flag.NewFlagSet("pin-backfill apply", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	planPath := f.String("plan", "", "frozen pin-backfill plan JSON")
	planDigest := f.String("plan-digest", "", "SHA-256(JCS(plan)) lowercase hex")
	authorizationPath := f.String("authorization", "", "offline authorization artifact")
	if err := parseStrict(f, args); err != nil {
		return err
	}
	if strings.TrimSpace(*planPath) == "" || strings.TrimSpace(*planDigest) == "" || strings.TrimSpace(*authorizationPath) == "" {
		return fmt.Errorf("pin-backfill apply requires --plan, --plan-digest and --authorization")
	}
	if !isLowerHex64(*planDigest) {
		return returnReason(out, reasonPlanStale, exitPlanStale, "", fmt.Errorf("--plan-digest must be lowercase SHA-256 hex"))
	}
	plan, embeddedDigest, err := readPinBackfillPlan(*planPath)
	if err != nil {
		if errors.Is(err, errPinBackfillPlanDigest) {
			return returnReason(out, reasonPlanStale, exitPlanStale, "", err)
		}
		return err
	}
	computed, err := pinbackfill.Digest(plan)
	if err != nil || computed != *planDigest || (embeddedDigest != "" && embeddedDigest != computed) {
		return returnReason(out, reasonPlanStale, exitPlanStale, "", fmt.Errorf("plan digest does not match the frozen plan"))
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	result, err := pinbackfill.Apply(ctx, db, pinbackfill.ApplyOptions{
		Plan: plan, PlanDigest: *planDigest, Trust: trust, Authorization: envelope, Now: time.Now().UTC(),
	})
	if err != nil {
		switch {
		case errors.Is(err, pinbackfill.ErrPlanStale), errors.Is(err, pinbackfill.ErrInvalidPlan):
			return returnReasonWithRun(out, reasonPlanStale, exitPlanStale, "", err, pinBackfillApplyOutput(result, reasonPlanStale))
		case errors.Is(err, pinbackfill.ErrAuthorization):
			return returnReasonWithRun(out, reasonAuthorization, exitAuthorization, "", err, pinBackfillApplyOutput(result, reasonAuthorization))
		default:
			return err
		}
	}
	return writeJSON(out, result)
}

func pinBackfillApplyOutput(result pinbackfill.Result, reason string) map[string]any {
	return map[string]any{
		"status":      "blocked",
		"reason":      reason,
		"plan_digest": result.PlanDigest,
		"batch_id":    result.BatchID,
		"sources":     result.Sources,
		"blocked":     result.Blocked,
	}
}

func pinBackfillPinCount(plan pinbackfill.Plan) int {
	total := 0
	for _, source := range plan.Sources {
		total += len(source.Relations)
	}
	return total
}

var errPinBackfillPlanDigest = errors.New("pin backfill plan digest mismatch")

func readPinBackfillPlan(path string) (pinbackfill.Plan, string, error) {
	raw, err := readBoundedFile(path, maxPlanBytes)
	if err != nil {
		return pinbackfill.Plan{}, "", err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return pinbackfill.Plan{}, "", fmt.Errorf("plan decode failed")
	}
	if _, hasPlan := fields["plan"]; hasPlan {
		if len(fields) != 2 {
			return pinbackfill.Plan{}, "", fmt.Errorf("plan decode failed")
		}
		if _, ok := fields["plan_digest"]; !ok {
			return pinbackfill.Plan{}, "", fmt.Errorf("plan decode failed")
		}
		var preview pinBackfillPreviewFile
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&preview) != nil {
			return pinbackfill.Plan{}, "", fmt.Errorf("plan decode failed")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return pinbackfill.Plan{}, "", fmt.Errorf("plan decode failed")
		}
		if !isLowerHex64(preview.PlanDigest) {
			return pinbackfill.Plan{}, "", fmt.Errorf("plan decode failed")
		}
		computed, err := pinbackfill.Digest(preview.Plan)
		if err != nil || computed != preview.PlanDigest ||
			(preview.Plan.PlanDigest != "" && preview.Plan.PlanDigest != computed) {
			return pinbackfill.Plan{}, "", errPinBackfillPlanDigest
		}
		return preview.Plan, preview.PlanDigest, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var plan pinbackfill.Plan
	if decoder.Decode(&plan) != nil {
		return pinbackfill.Plan{}, "", fmt.Errorf("plan decode failed")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return pinbackfill.Plan{}, "", fmt.Errorf("plan decode failed")
	}
	embedded := plan.PlanDigest
	if embedded != "" && !isLowerHex64(embedded) {
		return pinbackfill.Plan{}, "", fmt.Errorf("plan decode failed")
	}
	computed, err := pinbackfill.Digest(plan)
	if err != nil || (embedded != "" && embedded != computed) {
		return pinbackfill.Plan{}, "", errPinBackfillPlanDigest
	}
	return plan, embedded, nil
}

func writePinBackfillJSONFile(path string, value any) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".pin-backfill-plan-*")
	if err != nil {
		return fmt.Errorf("plan output cannot be created")
	}
	tmp := file.Name()
	cleanup := func() {
		file.Close()
		os.Remove(tmp)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(value); err != nil {
		cleanup()
		return fmt.Errorf("plan output write failed")
	}
	if err = file.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("plan output write failed")
	}
	if err = file.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("plan output write failed")
	}
	if err = os.Chmod(tmp, 0600); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("plan output write failed")
	}
	if err = os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("plan output write failed")
	}
	return nil
}
