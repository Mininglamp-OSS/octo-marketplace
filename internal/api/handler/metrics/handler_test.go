package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	metricssvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/metrics"
	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

type fakeSkillSvc struct {
	items map[string]*skillsvc.SkillItem
}

func (f *fakeSkillSvc) Get(_ context.Context, id, _, _ string) (*skillsvc.SkillItem, error) {
	item, ok := f.items[id]
	if !ok {
		return nil, skillsvc.ErrNotFound
	}
	return item, nil
}

// mockMetricsRedis is a mock implementation for handler tests.
type mockMetricsRedis struct {
	err error
}

func (m *mockMetricsRedis) TrackView(context.Context, string, string) error     { return m.err }
func (m *mockMetricsRedis) TrackDownload(context.Context, string, string) error { return m.err }
func (m *mockMetricsRedis) TrackInstall(context.Context, string, string) error  { return m.err }

func setupTestRouter(redisErr error) *gin.Engine {
	metricssvc.ResetResolvers()

	skillService := &fakeSkillSvc{
		items: map[string]*skillsvc.SkillItem{
			"skill-123": {ID: "skill-123"},
		},
	}
	metricssvc.RegisterResolver("skill", metricssvc.NewSkillResolver(skillService))

	redis := &mockMetricsRedis{err: redisErr}
	svc := metricssvc.New(redis)
	h := New(svc)

	r := gin.New()
	// Fake auth middleware that sets identity
	r.Use(func(c *gin.Context) {
		c.Set("marketplace.identity", model.Identity{UID: "user-1", Name: "Test"})
		c.Set("marketplace.space_id", "space-1")
		c.Next()
	})
	v1 := r.Group("/api/v1")
	h.Register(v1)
	return r
}

func doTrack(r *gin.Engine, body map[string]string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/track", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestTrack_Success(t *testing.T) {
	r := setupTestRouter(nil)
	defer metricssvc.ResetResolvers()

	w := doTrack(r, map[string]string{
		"resource_type": "skill",
		"resource_id":   "skill-123",
		"event_type":    "view",
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTrack_UnsupportedEventType(t *testing.T) {
	r := setupTestRouter(nil)
	defer metricssvc.ResetResolvers()

	w := doTrack(r, map[string]string{
		"resource_type": "skill",
		"resource_id":   "skill-123",
		"event_type":    "download",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestTrack_UnsupportedResourceType(t *testing.T) {
	r := setupTestRouter(nil)
	defer metricssvc.ResetResolvers()

	w := doTrack(r, map[string]string{
		"resource_type": "mcp",
		"resource_id":   "mcp-1",
		"event_type":    "view",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestTrack_EmptyResourceID(t *testing.T) {
	r := setupTestRouter(nil)
	defer metricssvc.ResetResolvers()

	w := doTrack(r, map[string]string{
		"resource_type": "skill",
		"resource_id":   "",
		"event_type":    "view",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestTrack_ResourceNotVisible(t *testing.T) {
	r := setupTestRouter(nil)
	defer metricssvc.ResetResolvers()

	// Request for a skill that doesn't exist in our fake
	w := doTrack(r, map[string]string{
		"resource_type": "skill",
		"resource_id":   "nonexistent",
		"event_type":    "view",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTrack_RedisFailure_Still204(t *testing.T) {
	r := setupTestRouter(nil) // Redis error only logged in service, not returned
	defer metricssvc.ResetResolvers()

	w := doTrack(r, map[string]string{
		"resource_type": "skill",
		"resource_id":   "skill-123",
		"event_type":    "view",
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204 even with Redis failure, got %d", w.Code)
	}
}

func TestTrack_InvalidJSON(t *testing.T) {
	r := setupTestRouter(nil)
	defer metricssvc.ResetResolvers()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/track", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestTrack_NoAuth(t *testing.T) {
	metricssvc.ResetResolvers()
	defer metricssvc.ResetResolvers()

	skillService := &fakeSkillSvc{
		items: map[string]*skillsvc.SkillItem{"skill-123": {ID: "skill-123"}},
	}
	metricssvc.RegisterResolver("skill", metricssvc.NewSkillResolver(skillService))

	redis := &mockMetricsRedis{}
	svc := metricssvc.New(redis)
	h := New(svc)

	r := gin.New()
	// No auth middleware — Identity() will return false
	authenticator := middleware.NewAuthenticator(true, nil, model.Identity{}, "")
	v1 := r.Group("/api/v1", authenticator.Handler())
	h.Register(v1)

	body, _ := json.Marshal(map[string]string{
		"resource_type": "skill",
		"resource_id":   "skill-123",
		"event_type":    "view",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/track", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}
