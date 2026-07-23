package handler

import (
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestListParamsNormalizesRepeatedAndCommaValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, raw := range []string{"/mcps?category=dev&category=search&transport=stdio", "/mcps?category=dev,search&transport=stdio"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", raw, nil)
		p, _, _ := listParams(c)
		if !reflect.DeepEqual(p.Categories, []string{"dev", "search"}) {
			t.Fatalf("%s categories=%v", raw, p.Categories)
		}
		if !reflect.DeepEqual(p.Transports, []string{"stdio"}) {
			t.Fatalf("%s transports=%v", raw, p.Transports)
		}
	}
}

// TestCategoryAllSentinelDroppedFromCategoryOnly locks in the split behaviour:
// the "all" sentinel disables the CATEGORY filter (mcp-v1.md §0) but must NOT
// silently swallow a tag / transport / source literally named "all". This
// guards a cross-repo bug where the frontend allows a tag named "all"
// (mcpTagValidation only limits length + charset) and the backend used to
// strip it in the shared splitQuery helper.
func TestCategoryAllSentinelDroppedFromCategoryOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/mcps?category=all,dev&tag=all&tag=official", nil)
	p, _, _ := listParams(c)
	if !reflect.DeepEqual(p.Categories, []string{"dev"}) {
		t.Fatalf("category 'all' must drop when combined with real keys, got %v", p.Categories)
	}
	if !reflect.DeepEqual(p.Tags, []string{"all", "official"}) {
		t.Fatalf("tag 'all' must survive as a legal tag value, got %v", p.Tags)
	}
}
