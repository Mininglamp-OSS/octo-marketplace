package plugin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInvalidDismissalRejectedBeforeDispatch(t *testing.T) {
	choice := `{"target_plugin_id":"target","target_revision":"revision"}`
	for _, body := range []string{`{}`, `{"choices":[]}`, `{"choices":null}`, `{"choices":[{"target_revision":"v"}]}`, `{"choices":[{"target_plugin_id":"id"}]}`, `{"choices":[` + strings.TrimSuffix(strings.Repeat(choice+",", 101), ",") + `]}`, `{"choices":[` + choice + "," + choice + `]}`} {
		rec := httptest.NewRecorder()
		testEngine(&fakeService{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/plugins/source/dependency_updates/dismiss", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid dismissal returned %d", rec.Code)
		}
	}
}
