package plugin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

// TestImportFieldErrorNamesTheVersionInputInTheEnvelope is the wire half of the
// import version rule. resolveImportFields refuses a caller-SUBMITTED malformed
// version with a *ReviewFieldError rather than a bare ErrInvalidRequest
// precisely so the browser can mark the version input; that only pays off if
// writeServiceError still carries the field through on this route, which does
// not otherwise go through the review path. A collapse to
// {"field":"body","reason":"invalid"} would be a silent regression.
func TestImportFieldErrorNamesTheVersionInputInTheEnvelope(t *testing.T) {
	f := &fakeService{err: &pluginsvc.ReviewFieldError{Field: "version", Reason: "invalid"}}
	body := []byte(`{"parse_task_id":"task-1","version":"1.0"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	testEngine(f).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, rec.Body.String())
	}
	if envelope.Error.Code != "VALIDATION_ERROR" {
		t.Fatalf("code = %q, want VALIDATION_ERROR", envelope.Error.Code)
	}
	if envelope.Error.Details["field"] != "version" || envelope.Error.Details["reason"] != "invalid" {
		t.Fatalf("details = %#v, want field=version reason=invalid", envelope.Error.Details)
	}
}
