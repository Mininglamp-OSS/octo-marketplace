package expert

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

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
