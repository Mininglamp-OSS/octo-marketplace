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
	// (a) Combined category values keep only the non-sentinel value.
	c1, _ := gin.CreateTestContext(httptest.NewRecorder())
	c1.Request = httptest.NewRequest("GET", "/mcps?category=all,dev&tag=all&tag=official", nil)
	p1, _, _ := listParams(c1)
	if !reflect.DeepEqual(p1.Categories, []string{"dev"}) {
		t.Fatalf("category 'all' must drop when combined with real keys, got %v", p1.Categories)
	}
	if !reflect.DeepEqual(p1.Tags, []string{"all", "official"}) {
		t.Fatalf("tag 'all' must survive as a legal tag value, got %v", p1.Tags)
	}

	// (b) Sole `?category=all` yields empty Categories (no leftover sentinel).
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	c2.Request = httptest.NewRequest("GET", "/mcps?category=all", nil)
	p2, _, _ := listParams(c2)
	if len(p2.Categories) != 0 {
		t.Fatalf("sole category=all must yield empty slice, got %v", p2.Categories)
	}

	// (c) Non-category filters preserve the literal "all" value.
	c3, _ := gin.CreateTestContext(httptest.NewRecorder())
	c3.Request = httptest.NewRequest("GET", "/mcps?transport=all&source=all&created_by_type=all", nil)
	p3, _, _ := listParams(c3)
	if !reflect.DeepEqual(p3.Transports, []string{"all"}) {
		t.Fatalf("transport='all' must survive, got %v", p3.Transports)
	}
	if !reflect.DeepEqual(p3.Sources, []string{"all"}) {
		t.Fatalf("source='all' must survive, got %v", p3.Sources)
	}
	if !reflect.DeepEqual(p3.CreatedByTypes, []string{"all"}) {
		t.Fatalf("created_by_type='all' must survive, got %v", p3.CreatedByTypes)
	}
}
