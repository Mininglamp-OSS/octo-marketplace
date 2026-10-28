package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pluginhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/notify"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

const cardReceiptTestSecret = "synthetic-card-receipt-test-secret"

type receiptRoles struct {
	role  atomic.Int64
	fail  atomic.Bool
	mixed atomic.Bool
	calls atomic.Int64
}

func (*receiptRoles) NotifyEnabled() bool { return false }
func (*receiptRoles) RoleEnabled() bool   { return true }
func (*receiptRoles) NotifySpaceAdmins(context.Context, notify.NotifyRequest) (*notify.NotifyResponse, error) {
	return nil, errors.New("notification disabled in fixture")
}
func (r *receiptRoles) MemberRole(context.Context, string, string) (*int, error) {
	n := r.calls.Add(1)
	if r.fail.Load() {
		return nil, errors.New("role lookup temporarily unavailable")
	}
	role := int(r.role.Load())
	if r.mixed.Load() {
		role = int(n % 2)
	}
	return &role, nil
}

type cardHTTPFixture struct {
	db     *sql.DB
	svc    *pluginsvc.Service
	engine *gin.Engine
	roles  *receiptRoles
}

func newCardHTTPFixture(t *testing.T, managed bool) *cardHTTPFixture {
	t.Helper()
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	roles := &receiptRoles{}
	roles.role.Store(int64(pluginsvc.SpaceRoleAdmin))
	svc := pluginsvc.New(repo).WithVersionManagement(managed).WithNotify(roles, nil)
	if _, err := repo.UpsertReviewPolicy(context.Background(), tenantScope(), false, "reviewer", "Reviewer"); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	pluginhandler.NewCardAction(svc, cardReceiptTestSecret, time.Minute*5).Register(engine)
	return &cardHTTPFixture{database, svc, engine, roles}
}
func (f *cardHTTPFixture) pending(t *testing.T, name string) (string, string) {
	t.Helper()
	created, err := f.svc.Create(context.Background(), managedOwner(), managedSkillWrite("1.0.0", "Card "+name, model.PluginVisibilitySpace))
	if err != nil {
		t.Fatal(err)
	}
	publication, err := f.svc.Publish(context.Background(), managedOwner(), pluginsvc.PublishParams{PluginID: created.Plugin.ID})
	if err != nil {
		t.Fatal(err)
	}
	if publication.Review == nil {
		t.Fatal("expected manual review")
	}
	return publication.Review.ID, created.Plugin.ID
}
func signedReceiptRequest(event, operator, decision, review string) *http.Request {
	raw, _ := json.Marshal(map[string]any{"event_id": event, "operator_uid": operator, "decision": decision, "data": map[string]string{"review_id": review}})
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, pluginhandler.CardActionPath, strings.NewReader(string(raw)))
	req.Header.Set(notify.HeaderTimestamp, stamp)
	req.Header.Set(notify.HeaderEventID, event)
	req.Header.Set(notify.HeaderSignature, notify.Sign(cardReceiptTestSecret, http.MethodPost, pluginhandler.CardActionPath, stamp, event, raw))
	return req
}
func (f *cardHTTPFixture) send(event, operator, decision, review string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, signedReceiptRequest(event, operator, decision, review))
	return rec
}
func checkCardResult(t *testing.T, rec *httptest.ResponseRecorder, disposition, state string) {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || out["disposition"] != disposition || out["state"] != state {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	for key := range out {
		if key != "disposition" && key != "state" && key != "requester_uid" && key != "display" {
			t.Fatalf("private receipt metadata leaked: %s", rec.Body.String())
		}
	}
}
func receiptCount(t *testing.T, db *sql.DB, event string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM plugin_card_action_receipts WHERE event_id=?`, event).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func checkReviewState(t *testing.T, db *sql.DB, review, state string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT status FROM plugin_review_requests WHERE review_id=?`, review).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != state {
		t.Fatalf("review state=%s want=%s", got, state)
	}
}

func TestCardHTTPHandledRefusalsReplay(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprintf("managed=%t", managed), func(t *testing.T) {
			f := newCardHTTPFixture(t, managed)
			review, _ := f.pending(t, "refusal")
			f.roles.role.Store(int64(pluginsvc.SpaceRoleMember))
			first := f.send("6100", "operator-1", "deny", review)
			checkCardResult(t, first, "forbidden", "pending")
			if receiptCount(t, f.db, "6100") != 1 {
				t.Fatal("handled refusal not durable")
			}
			calls := f.roles.calls.Load()
			f.roles.role.Store(int64(pluginsvc.SpaceRoleAdmin))
			replay := f.send("6100", "operator-1", "deny", review)
			if replay.Code != 200 || replay.Body.String() != first.Body.String() || f.roles.calls.Load() != calls {
				t.Fatal("refusal replay re-ran authorization or changed response")
			}
			checkReviewState(t, f.db, review, "pending")
			for _, input := range [][3]string{{"other-operator", "deny", review}, {"operator-1", "approve", review}, {"operator-1", "deny", "other-review"}} {
				bad := f.send("6100", input[0], input[1], input[2])
				if bad.Code != 400 {
					t.Fatalf("event reused for different signed inputs: %d %s", bad.Code, bad.Body.String())
				}
			}
			fresh := f.send("6101", "operator-1", "deny", review)
			checkCardResult(t, fresh, "applied", "denied")
			checkReviewState(t, f.db, review, "rejected")
			if again := f.send("6100", "operator-1", "deny", review); again.Body.String() != first.Body.String() {
				t.Fatal("state changes rewrote old refusal")
			}
			// A physically absent parent must still have a terminal receipt; restoring
			// an applicant's record later cannot turn an old click into a new decision.
			missing := f.send("6102", "operator-1", "deny", "restored-review")
			checkCardResult(t, missing, "not_found", "cancelled")
			restored, _ := f.pending(t, "restored")
			if _, err := f.db.Exec(`UPDATE plugin_review_requests SET review_id='restored-review' WHERE review_id=?`, restored); err != nil {
				t.Fatal(err)
			}
			again := f.send("6102", "operator-1", "deny", "restored-review")
			if again.Code != 200 || again.Body.String() != missing.Body.String() {
				t.Fatal("missing-review refusal did not replay")
			}
			checkReviewState(t, f.db, "restored-review", "pending")
			invalid := signedReceiptRequest("6103", "operator-1", "deny", "restored-review")
			invalid.Header.Set(notify.HeaderSignature, "invalid")
			response := httptest.NewRecorder()
			f.engine.ServeHTTP(response, invalid)
			if response.Code != 401 || receiptCount(t, f.db, "6103") != 0 {
				t.Fatal("invalid signature consumed an event")
			}
		})
	}
}

func TestCardHTTPReceiptFailuresRollbackAndRetry(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprintf("managed=%t", managed), func(t *testing.T) {
			f := newCardHTTPFixture(t, managed)
			// Failure before the receipt can be made durable must never be HTTP200.
			refused, _ := f.pending(t, "write-failure")
			f.roles.role.Store(int64(pluginsvc.SpaceRoleMember))
			if _, err := f.db.Exec(`CREATE TRIGGER block_receipt BEFORE INSERT ON plugin_card_action_receipts FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='synthetic receipt fault'`); err != nil {
				t.Fatal(err)
			}
			rec := f.send("6200", "operator-1", "deny", refused)
			if rec.Code != 503 || receiptCount(t, f.db, "6200") != 0 {
				t.Fatal("receipt failure acknowledged a refusal")
			}
			f.roles.role.Store(int64(pluginsvc.SpaceRoleAdmin))
			applied, plugin := f.pending(t, "atomic-approval")
			rec = f.send("6201", "operator-1", "approve", applied)
			if rec.Code != 503 || receiptCount(t, f.db, "6201") != 0 {
				t.Fatal("receipt failure acknowledged approval")
			}
			checkReviewState(t, f.db, applied, "pending")
			var listing string
			if err := f.db.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id=?`, plugin).Scan(&listing); err != nil || listing != "draft" {
				t.Fatalf("approval escaped receipt transaction: %s %v", listing, err)
			}
			if _, err := f.db.Exec(`DROP TRIGGER block_receipt`); err != nil {
				t.Fatal(err)
			}
			checkCardResult(t, f.send("6201", "operator-1", "approve", applied), "applied", "approved")
			checkReviewState(t, f.db, applied, "approved")
			// A failure after the receipt insert rolls back BOTH receipt and decision.
			rejected, _ := f.pending(t, "atomic-rejection")
			if _, err := f.db.Exec(`CREATE TRIGGER block_decision BEFORE UPDATE ON plugin_review_requests FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='synthetic decision fault'`); err != nil {
				t.Fatal(err)
			}
			rec = f.send("6202", "operator-1", "deny", rejected)
			if rec.Code != 503 || receiptCount(t, f.db, "6202") != 0 {
				t.Fatal("failed decision left an applied receipt")
			}
			checkReviewState(t, f.db, rejected, "pending")
			if _, err := f.db.Exec(`DROP TRIGGER block_decision`); err != nil {
				t.Fatal(err)
			}
			checkCardResult(t, f.send("6202", "operator-1", "deny", rejected), "applied", "denied")
			transient, _ := f.pending(t, "role-fault")
			f.roles.fail.Store(true)
			rec = f.send("6203", "operator-1", "deny", transient)
			if rec.Code != 503 || receiptCount(t, f.db, "6203") != 0 {
				t.Fatal("temporary role failure became terminal")
			}
			f.roles.fail.Store(false)
			checkCardResult(t, f.send("6203", "operator-1", "deny", transient), "applied", "denied")
		})
	}
}

func TestCardHTTPConcurrentEventHasOneDurableWinner(t *testing.T) {
	f := newCardHTTPFixture(t, true)
	review, _ := f.pending(t, "contenders")
	f.roles.mixed.Store(true)
	const n = 8
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; responses <- f.send("6300", "operator-1", "deny", review) }()
	}
	close(start)
	wg.Wait()
	close(responses)
	first := ""
	for rec := range responses {
		if rec.Code != 200 {
			t.Fatalf("contender failed: %d %s", rec.Code, rec.Body.String())
		}
		if first == "" {
			first = rec.Body.String()
		} else if first != rec.Body.String() {
			t.Fatal("same event returned different handled outcomes")
		}
	}
	if receiptCount(t, f.db, "6300") != 1 {
		t.Fatal("multiple event receipts")
	}
	var out pluginsvc.CardActionResult
	if err := json.Unmarshal([]byte(first), &out); err != nil {
		t.Fatal(err)
	}
	if out.Disposition == "forbidden" {
		checkReviewState(t, f.db, review, "pending")
	} else if out.Disposition == "applied" && out.State == "denied" {
		checkReviewState(t, f.db, review, "rejected")
	} else {
		t.Fatalf("unexpected winner: %s", first)
	}
}
