package expert

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
	"github.com/gin-gonic/gin"
)

// A name collision must surface as 409 with error.code "DUPLICATE" — the code
// the contract (docs/api/expert-v1.md §2) and the Swagger annotations advertise,
// and the one the rest of the platform emits for this case.
func TestWriteServiceErrorNameTakenIsDuplicate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	writeServiceError(c, expertsvc.ErrNameTaken, "expert.create")

	if w.Code != 409 {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, w.Body.String())
	}
	if body.Error.Code != "DUPLICATE" {
		t.Fatalf("error.code = %q, want DUPLICATE", body.Error.Code)
	}
}

func decodeErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, w.Body.String())
	}
	return body.Error.Code
}

// An unconfigured fleet client (OCTO_FLEET_URL unset) must surface as a 503
// UPSTREAM_UNAVAILABLE, not a 500 — install depends on a downstream service.
func TestWriteInstallErrorFleetNotConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	writeInstallError(c, expertsvc.ErrFleetNotConfigured)

	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if code := decodeErrCode(t, w); code != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("error.code = %q, want UPSTREAM_UNAVAILABLE", code)
	}
}

// A fleet 4xx (e.g. duplicate agent name) is surfaced verbatim so the client
// sees the real fault instead of a masking 503.
func TestWriteInstallErrorFleet4xxPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	writeInstallError(c, &fleet.APIError{Status: 409, Message: "an agent named \"X\" already exists"})

	if w.Code != 409 {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if code := decodeErrCode(t, w); code != "CONFLICT" {
		t.Fatalf("error.code = %q, want CONFLICT", code)
	}
}

// A fleet 5xx / transport failure collapses to 503 UPSTREAM_UNAVAILABLE.
func TestWriteInstallErrorFleet5xxIsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	writeInstallError(c, &fleet.APIError{Status: 500, Message: "boom"})

	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if code := decodeErrCode(t, w); code != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("error.code = %q, want UPSTREAM_UNAVAILABLE", code)
	}
}

// ErrNotFound (hidden/absent expert) still maps through the shared catalog
// mapping to a 404.
func TestWriteInstallErrorNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	writeInstallError(c, expertsvc.ErrNotFound)

	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}
