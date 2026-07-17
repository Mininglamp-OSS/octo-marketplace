package metrics

import (
	"context"
	"errors"
	"testing"
)

// mockRedis implements MetricsRedis for testing.
type mockRedis struct {
	viewCalls     []string
	downloadCalls []string
	installCalls  []string
	err           error
}

func (m *mockRedis) TrackView(_ context.Context, resourceType, resourceID string) error {
	m.viewCalls = append(m.viewCalls, resourceType+"/"+resourceID)
	return m.err
}

func (m *mockRedis) TrackDownload(_ context.Context, resourceType, resourceID string) error {
	m.downloadCalls = append(m.downloadCalls, resourceType+"/"+resourceID)
	return m.err
}

func (m *mockRedis) TrackInstall(_ context.Context, resourceType, resourceID string) error {
	m.installCalls = append(m.installCalls, resourceType+"/"+resourceID)
	return m.err
}

func setupService(redis MetricsRedis) *Service {
	ResetResolvers()
	RegisterResolver("skill", &testResolver{canView: true})
	return New(redis)
}

func TestTrackView_Success(t *testing.T) {
	redis := &mockRedis{}
	svc := setupService(redis)
	defer ResetResolvers()

	err := svc.TrackView(context.Background(), "skill", "sk-1", Caller{UID: "u1", SpaceID: "s1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(redis.viewCalls) != 1 || redis.viewCalls[0] != "skill/sk-1" {
		t.Fatalf("expected one view call for skill/sk-1, got %v", redis.viewCalls)
	}
}

func TestTrackView_EmptyResourceType(t *testing.T) {
	redis := &mockRedis{}
	svc := setupService(redis)
	defer ResetResolvers()

	err := svc.TrackView(context.Background(), "", "sk-1", Caller{UID: "u1", SpaceID: "s1"})
	if !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("expected ErrInvalidParam, got %v", err)
	}
}

func TestTrackView_EmptyResourceID(t *testing.T) {
	redis := &mockRedis{}
	svc := setupService(redis)
	defer ResetResolvers()

	err := svc.TrackView(context.Background(), "skill", "", Caller{UID: "u1", SpaceID: "s1"})
	if !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("expected ErrInvalidParam, got %v", err)
	}
}

func TestTrackView_ResourceTypeTooLong(t *testing.T) {
	redis := &mockRedis{}
	svc := setupService(redis)
	defer ResetResolvers()

	longType := "aaaaaaaaaabbbbbbbbbbccccccccccdddd" // 34 chars > 32
	err := svc.TrackView(context.Background(), longType, "sk-1", Caller{UID: "u1", SpaceID: "s1"})
	if !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("expected ErrInvalidParam, got %v", err)
	}
}

func TestTrackView_UnsupportedType(t *testing.T) {
	redis := &mockRedis{}
	ResetResolvers()
	RegisterResolver("skill", &testResolver{canView: true})
	svc := New(redis)
	defer ResetResolvers()

	err := svc.TrackView(context.Background(), "mcp", "m-1", Caller{UID: "u1", SpaceID: "s1"})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("expected ErrUnsupportedType, got %v", err)
	}
}

func TestTrackView_ResourceNotVisible(t *testing.T) {
	redis := &mockRedis{}
	ResetResolvers()
	RegisterResolver("skill", &testResolver{canView: false})
	svc := New(redis)
	defer ResetResolvers()

	err := svc.TrackView(context.Background(), "skill", "sk-1", Caller{UID: "u1", SpaceID: "s1"})
	if !errors.Is(err, ErrResourceNotVisible) {
		t.Fatalf("expected ErrResourceNotVisible, got %v", err)
	}
}

func TestTrackView_RedisFailure_StillReturnsNil(t *testing.T) {
	redis := &mockRedis{err: errors.New("connection refused")}
	ResetResolvers()
	RegisterResolver("skill", &testResolver{canView: true})
	svc := New(redis)
	defer ResetResolvers()

	err := svc.TrackView(context.Background(), "skill", "sk-1", Caller{UID: "u1", SpaceID: "s1"})
	if err != nil {
		t.Fatalf("expected nil error even on Redis failure, got %v", err)
	}
}

func TestTrackDownload_Success(t *testing.T) {
	redis := &mockRedis{}
	svc := setupService(redis)
	defer ResetResolvers()

	err := svc.TrackDownload(context.Background(), "skill", "sk-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(redis.downloadCalls) != 1 || redis.downloadCalls[0] != "skill/sk-1" {
		t.Fatalf("expected one download call, got %v", redis.downloadCalls)
	}
}

func TestTrackDownload_RedisFailure_StillReturnsNil(t *testing.T) {
	redis := &mockRedis{err: errors.New("timeout")}
	svc := setupService(redis)
	defer ResetResolvers()

	err := svc.TrackDownload(context.Background(), "skill", "sk-1")
	if err != nil {
		t.Fatalf("expected nil on redis failure, got %v", err)
	}
}

func TestTrackInstall_Success(t *testing.T) {
	redis := &mockRedis{}
	svc := setupService(redis)
	defer ResetResolvers()

	err := svc.TrackInstall(context.Background(), "skill", "sk-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(redis.installCalls) != 1 || redis.installCalls[0] != "skill/sk-1" {
		t.Fatalf("expected one install call, got %v", redis.installCalls)
	}
}

func TestTrackInstall_UnsupportedType(t *testing.T) {
	redis := &mockRedis{}
	ResetResolvers()
	svc := New(redis)
	defer ResetResolvers()

	err := svc.TrackInstall(context.Background(), "unknown", "r-1")
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("expected ErrUnsupportedType, got %v", err)
	}
}

func TestNew_NilRedis(t *testing.T) {
	ResetResolvers()
	RegisterResolver("skill", &testResolver{canView: true})
	svc := New(nil)
	defer ResetResolvers()

	// Should work with no-op redis
	err := svc.TrackView(context.Background(), "skill", "sk-1", Caller{UID: "u1", SpaceID: "s1"})
	if err != nil {
		t.Fatalf("expected nil error with nil redis, got %v", err)
	}
}
