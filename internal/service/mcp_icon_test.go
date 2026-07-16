package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// ─── #2 length validation ────────────────────────────────────────────────

func TestCreateRejectsOverlongFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*model.CreateRequest)
		field  string
	}{
		{"name", func(r *model.CreateRequest) { r.Name = strings.Repeat("a", model.MaxNameLen+1) }, "name"},
		{"slogan", func(r *model.CreateRequest) { r.Slogan = strings.Repeat("a", model.MaxSloganLen+1) }, "slogan"},
		{"url", func(r *model.CreateRequest) { r.URL = "https://" + strings.Repeat("a", model.MaxURLLen) }, "url"},
		{"command", func(r *model.CreateRequest) {
			r.Transport = model.TransportStdio
			r.URL = ""
			r.Command = strings.Repeat("c", model.MaxCommandLen+1)
		}, "command"},
		{"arg", func(r *model.CreateRequest) {
			r.Transport = model.TransportStdio
			r.URL = ""
			r.Command = "run"
			r.Args = []string{strings.Repeat("x", model.MaxArgLen+1)}
		}, "args[0]"},
		{"tool name", func(r *model.CreateRequest) {
			r.Tools = []model.Tool{{Name: strings.Repeat("t", model.MaxToolNameLen+1)}}
		}, "tools.name[0]"},
		{"tool description", func(r *model.CreateRequest) {
			r.Tools = []model.Tool{{Name: "ok", Description: strings.Repeat("d", model.MaxTextLen+1)}}
		}, "tools.description[0]"},
		{"faq answer", func(r *model.CreateRequest) {
			r.FAQs = []model.FAQ{{Question: "q", Answer: strings.Repeat("a", model.MaxTextLen+1)}}
		}, "faqs.answer[0]"},
		{"note", func(r *model.CreateRequest) {
			r.Notes = []string{strings.Repeat("n", model.MaxTextLen+1)}
		}, "notes[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			svc := New(store)
			req := baseCreate()
			tc.mutate(&req)

			_, apiErr := svc.Create(context.Background(), caller, req)
			if apiErr == nil || apiErr.Code != apierr.CodeInvalidRequest {
				t.Fatalf("expected invalid_request, got %v", apiErr)
			}
			if len(apiErr.Details) == 0 || apiErr.Details[0].Field != tc.field {
				t.Fatalf("expected detail field %q, got %+v", tc.field, apiErr.Details)
			}
			if store.created != nil {
				t.Fatalf("record was created despite validation failure")
			}
		})
	}
}

func TestCreateRejectsOverlongHeader(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	req := baseCreate()
	req.Headers = map[string]string{"X-Big": strings.Repeat("v", model.MaxHeaderValueLen+1)}

	_, apiErr := svc.Create(context.Background(), caller, req)
	if apiErr == nil || apiErr.Code != apierr.CodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %v", apiErr)
	}
}

func TestCreateAcceptsFieldsAtLimit(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	req := baseCreate()
	req.Name = strings.Repeat("a", model.MaxNameLen)
	req.Slogan = strings.Repeat("b", model.MaxSloganLen)

	if _, apiErr := svc.Create(context.Background(), caller, req); apiErr != nil {
		t.Fatalf("fields exactly at limit should pass, got %v", apiErr)
	}
}

// ─── #3 transport required fields ─────────────────────────────────────────

func TestCreateRequiresCommandForStdio(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	req := baseCreate()
	req.Transport = model.TransportStdio
	req.URL = ""
	req.Command = "" // missing

	_, apiErr := svc.Create(context.Background(), caller, req)
	if apiErr == nil || apiErr.Code != apierr.CodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %v", apiErr)
	}
	if apiErr.Details[0].Field != "command" || apiErr.Details[0].Reason != "required" {
		t.Fatalf("expected command required detail, got %+v", apiErr.Details)
	}
}

func TestCreateRequiresURLForHTTP(t *testing.T) {
	for _, tr := range []model.Transport{model.TransportStreamableHTTP, model.TransportSSE} {
		store := newFakeStore()
		svc := New(store)
		req := baseCreate()
		req.Transport = tr
		req.URL = "" // missing

		_, apiErr := svc.Create(context.Background(), caller, req)
		if apiErr == nil || apiErr.Details[0].Field != "url" {
			t.Fatalf("%s: expected url required, got %v", tr, apiErr)
		}
	}
}

func TestCreateStdioArgsOptional(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	req := baseCreate()
	req.Transport = model.TransportStdio
	req.URL = ""
	req.Command = "run"
	req.Args = nil // args stay optional

	if _, apiErr := svc.Create(context.Background(), caller, req); apiErr != nil {
		t.Fatalf("stdio with command and no args should pass, got %v", apiErr)
	}
}

func TestPatchTransportRequiredOnlyWhenTouched(t *testing.T) {
	// A record whose stored transport is stdio but has no command (legacy /
	// incomplete). A rename-only PATCH must NOT be blocked by the required
	// -field rule; a PATCH that switches transport must be validated.
	store := newFakeStore()
	svc := New(store)
	fixedClock(svc)
	seed(store, model.MCP{
		ID: "own", Name: "Mine", Visibility: model.VisibilityPrivate,
		OwnerUID: "u1", SpaceID: "space-a", Transport: model.TransportStdio,
	})

	newName := "Renamed"
	if _, apiErr := svc.Patch(context.Background(), caller, "own", model.PatchRequest{Name: &newName}); apiErr != nil {
		t.Fatalf("rename-only patch should not enforce transport fields, got %v", apiErr)
	}

	// Switching to http without a url must fail.
	tr := model.TransportStreamableHTTP
	_, apiErr := svc.Patch(context.Background(), caller, "own", model.PatchRequest{Transport: &tr})
	if apiErr == nil || apiErr.Details[0].Field != "url" {
		t.Fatalf("expected url required on transport switch, got %v", apiErr)
	}
}

func TestPatchRejectsOverlongSlogan(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	fixedClock(svc)
	seed(store, model.MCP{
		ID: "own", Name: "Mine", Visibility: model.VisibilityPrivate,
		OwnerUID: "u1", SpaceID: "space-a", Transport: model.TransportStreamableHTTP,
		Connection: model.Connection{URL: "https://x"},
	})

	long := strings.Repeat("s", model.MaxSloganLen+1)
	_, apiErr := svc.Patch(context.Background(), caller, "own", model.PatchRequest{Slogan: &long})
	if apiErr == nil || apiErr.Code != apierr.CodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %v", apiErr)
	}
}

// ─── #1 icon upload ───────────────────────────────────────────────────────

type fakeUploader struct {
	putKey string
	putURL string
	err    error
}

func (f *fakeUploader) Put(_ context.Context, key, _ string, _ []byte) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.putKey = key
	if f.putURL != "" {
		return f.putURL, nil
	}
	return "https://cdn.example.com/" + key, nil
}

func newIconSvc(store *fakeStore, up IconStore) *Service {
	svc := New(store)
	fixedClock(svc)
	return svc.WithIconStore(up, IconConfig{Partition: "mcp"})
}

func TestUploadIconStoresAndBumpsVersion(t *testing.T) {
	store := newFakeStore()
	up := &fakeUploader{}
	svc := newIconSvc(store, up)
	seed(store, model.MCP{
		ID: "own", Name: "Mine", Visibility: model.VisibilityPrivate,
		OwnerUID: "u1", SpaceID: "space-a", Transport: model.TransportStreamableHTTP,
		Connection: model.Connection{URL: "https://x"}, IconVersion: 2,
	})

	res, apiErr := svc.UploadIcon(context.Background(), caller, "own", []byte("PNGDATA"), "image/png")
	if apiErr != nil {
		t.Fatalf("upload failed: %v", apiErr)
	}
	if res.Version != 3 {
		t.Fatalf("version = %d, want 3", res.Version)
	}
	if up.putKey != "mcp_icon/mcp/own/3.png" {
		t.Fatalf("key = %q, want mcp_icon/mcp/own/3.png", up.putKey)
	}
	if store.updated == nil || store.updated.Icon != res.URL || store.updated.IconVersion != 3 {
		t.Fatalf("record not updated with new icon: %+v", store.updated)
	}
}

func TestUploadIconOwnerOnly(t *testing.T) {
	store := newFakeStore()
	svc := newIconSvc(store, &fakeUploader{})
	seed(store, model.MCP{
		ID: "other", Name: "Theirs", Visibility: model.VisibilityPublic,
		OwnerUID: "u2", SpaceID: "space-a", Transport: model.TransportStreamableHTTP,
		Connection: model.Connection{URL: "https://x"},
	})

	_, apiErr := svc.UploadIcon(context.Background(), caller, "other", []byte("x"), "image/png")
	if apiErr == nil || apiErr.Code != apierr.CodeForbidden {
		t.Fatalf("expected forbidden, got %v", apiErr)
	}
}

func TestUploadIconCrossSpaceNotFound(t *testing.T) {
	store := newFakeStore()
	svc := newIconSvc(store, &fakeUploader{})
	seed(store, model.MCP{
		ID: "elsewhere", Name: "X", Visibility: model.VisibilityPrivate,
		OwnerUID: "u1", SpaceID: "space-b", Transport: model.TransportStreamableHTTP,
		Connection: model.Connection{URL: "https://x"},
	})

	_, apiErr := svc.UploadIcon(context.Background(), caller, "elsewhere", []byte("x"), "image/png")
	if apiErr == nil || apiErr.Code != apierr.CodeNotFound {
		t.Fatalf("expected not_found, got %v", apiErr)
	}
}

func TestUploadIconDisabledWhenNoStore(t *testing.T) {
	store := newFakeStore()
	svc := New(store) // no WithIconStore
	seed(store, model.MCP{
		ID: "own", OwnerUID: "u1", SpaceID: "space-a",
		Visibility: model.VisibilityPrivate, Transport: model.TransportStdio,
	})

	_, apiErr := svc.UploadIcon(context.Background(), caller, "own", []byte("x"), "image/png")
	if apiErr == nil || apiErr.Code != apierr.CodeInvalidRequest {
		t.Fatalf("expected invalid_request when storage disabled, got %v", apiErr)
	}
}

func TestUploadIconRejectsEmptyAndOversized(t *testing.T) {
	store := newFakeStore()
	svc := New(store).WithIconStore(&fakeUploader{}, IconConfig{Partition: "mcp", MaxBytes: 4})
	svc.now = func() time.Time { return time.Now() }
	seed(store, model.MCP{
		ID: "own", OwnerUID: "u1", SpaceID: "space-a",
		Visibility: model.VisibilityPrivate, Transport: model.TransportStdio,
		Connection: model.Connection{Command: "run"},
	})

	if _, apiErr := svc.UploadIcon(context.Background(), caller, "own", nil, "image/png"); apiErr == nil {
		t.Fatalf("expected error for empty upload")
	}
	if _, apiErr := svc.UploadIcon(context.Background(), caller, "own", []byte("toolong"), "image/png"); apiErr == nil {
		t.Fatalf("expected error for oversized upload")
	}
}

func TestUploadIconStorageErrorIsInternal(t *testing.T) {
	store := newFakeStore()
	up := &fakeUploader{err: errors.New("bucket unreachable")}
	svc := newIconSvc(store, up)
	seed(store, model.MCP{
		ID: "own", OwnerUID: "u1", SpaceID: "space-a",
		Visibility: model.VisibilityPrivate, Transport: model.TransportStdio,
		Connection: model.Connection{Command: "run"},
	})

	_, apiErr := svc.UploadIcon(context.Background(), caller, "own", []byte("x"), "image/png")
	if apiErr == nil || apiErr.Code != apierr.CodeInternal {
		t.Fatalf("expected internal error (leak-free), got %v", apiErr)
	}
	// The bucket error string must not surface to the client.
	if apiErr != nil && strings.Contains(apiErr.Message, "bucket") {
		t.Fatalf("bucket error leaked to client message: %q", apiErr.Message)
	}
}
