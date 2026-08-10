package service

import (
	"context"
	"net"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Tests for issue #48: a normal user must not (1) register an MCP whose remote
// URL targets a private / local / cloud-metadata address (SSRF), nor (2) shadow
// an official visibility=system MCP by reusing its name or slug.

// ── SSRF / URL guard on create ──────────────────────────────────────────────

func TestCreateRejectsUnsafeTargetURL(t *testing.T) {
	unsafe := []struct {
		name string
		url  string
	}{
		{"loopback", "http://127.0.0.1/mcp"},
		{"private_10", "http://10.0.0.5/mcp"},
		{"private_192", "https://192.168.1.10/mcp"},
		{"link_local_metadata", "http://169.254.169.254/latest/meta-data/"},
		{"unspecified", "http://0.0.0.0/mcp"},
	}
	transports := []model.Transport{model.TransportStreamableHTTP, model.TransportSSE}

	for _, tr := range transports {
		for _, tc := range unsafe {
			t.Run(string(tr)+"/"+tc.name, func(t *testing.T) {
				svc := New(newFakeStore())
				req := baseCreate()
				req.Transport = tr
				req.URL = tc.url

				_, apiErr := svc.Create(context.Background(), caller, req)
				if apiErr == nil {
					t.Fatalf("expected create to reject unsafe url %q", tc.url)
				}
				if apiErr.Code != apierr.CodeInvalidRequest {
					t.Fatalf("code = %q, want %q", apiErr.Code, apierr.CodeInvalidRequest)
				}
			})
		}
	}
}

func TestCreateAllowsPublicTargetURL(t *testing.T) {
	svc := New(newFakeStore())
	svc.resolver = &fakeResolver{ips: ips(t, "93.184.216.34")}
	req := baseCreate()
	req.URL = "https://mcp.example.com/github"

	if _, apiErr := svc.Create(context.Background(), caller, req); apiErr != nil {
		t.Fatalf("public url rejected: %v", apiErr)
	}
}

// ── Resolve-time SSRF guard (issue #48 enhancement, fail-open) ──────────────

// A domain that resolves to any unsafe address is rejected — even though the
// URL string itself carries no literal IP.
func TestCreateRejectsDomainResolvingToUnsafe(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	svc.resolver = &fakeResolver{ips: ips(t, "93.184.216.34", "10.0.0.7")} // mixed public+private

	_, apiErr := svc.Create(context.Background(), caller, baseCreate())
	if apiErr == nil || apiErr.Code != apierr.CodeInvalidRequest {
		t.Fatalf("expected private_address rejection, got %v", apiErr)
	}
	if store.created != nil {
		t.Fatal("record must not be created when the host resolves to a private IP")
	}
}

func TestCreateAllowsDomainResolvingToPublic(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	svc.resolver = &fakeResolver{ips: ips(t, "93.184.216.34")}

	if _, apiErr := svc.Create(context.Background(), caller, baseCreate()); apiErr != nil {
		t.Fatalf("public resolution wrongly blocked: %v", apiErr)
	}
}

// Fail-open: a transient DNS error must NOT block the write — the runtime that
// actually dials owns the authoritative resolve-time gate.
func TestCreateFailsOpenOnDNSError(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	svc.resolver = &fakeResolver{err: &net.DNSError{Err: "no such host", Name: "mcp.example.com"}}

	if _, apiErr := svc.Create(context.Background(), caller, baseCreate()); apiErr != nil {
		t.Fatalf("DNS error must fail open, got %v", apiErr)
	}
	if store.created == nil {
		t.Fatal("record should be created on fail-open")
	}
}

func TestPatchRejectsDomainResolvingToUnsafe(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	svc.resolver = &fakeResolver{ips: ips(t, "10.0.0.7")}
	seed(store, model.MCP{
		ID:         "own",
		Name:       "Mine",
		Slug:       "mine",
		Visibility: model.VisibilityPublic,
		OwnerUID:   "u1",
		SpaceID:    "space-a",
		Transport:  model.TransportStreamableHTTP,
		Connection: model.Connection{URL: "https://mcp.example.com/ok"},
	})

	newURL := "https://evil.example.com/x"
	_, apiErr := svc.Patch(context.Background(), caller, "own", model.PatchRequest{URL: &newURL})
	if apiErr == nil || apiErr.Code != apierr.CodeInvalidRequest {
		t.Fatalf("expected private_address rejection, got %v", apiErr)
	}
	if store.updated != nil {
		t.Fatal("record must not be updated when the host resolves to a private IP")
	}
}

// stdio has no URL, so the SSRF guard must not fire and reject an otherwise
// valid stdio create.
func TestCreateStdioSkipsURLGuard(t *testing.T) {
	svc := New(newFakeStore())
	req := baseCreate()
	req.Transport = model.TransportStdio
	req.URL = ""
	req.Command = "npx"
	req.Args = []string{"-y", "@modelcontextprotocol/server-github"}

	if _, apiErr := svc.Create(context.Background(), caller, req); apiErr != nil {
		t.Fatalf("stdio create rejected: %v", apiErr)
	}
}

// The trusted self-hosted escape hatch (WithProbeAllowPrivate) must apply to
// create just as it does to Probe, so the two stay consistent.
func TestCreateAllowsPrivateURLWhenProbeAllowPrivate(t *testing.T) {
	svc := New(newFakeStore()).WithProbeAllowPrivate(true)
	req := baseCreate()
	req.URL = "http://10.0.0.5/mcp"

	if _, apiErr := svc.Create(context.Background(), caller, req); apiErr != nil {
		t.Fatalf("private url rejected under allow-private: %v", apiErr)
	}
}

// ── SSRF / URL guard on patch ───────────────────────────────────────────────

func TestPatchRejectsChangingURLToUnsafeTarget(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{
		ID:         "own",
		Name:       "Mine",
		Slug:       "mine",
		Visibility: model.VisibilityPublic,
		OwnerUID:   "u1",
		SpaceID:    "space-a",
		Transport:  model.TransportStreamableHTTP,
		Connection: model.Connection{URL: "https://mcp.example.com/ok"},
	})

	bad := "http://169.254.169.254/latest/meta-data/"
	_, apiErr := svc.Patch(context.Background(), caller, "own", model.PatchRequest{URL: &bad})
	if apiErr == nil {
		t.Fatalf("expected patch to reject unsafe url")
	}
	if apiErr.Code != apierr.CodeInvalidRequest {
		t.Fatalf("code = %q, want %q", apiErr.Code, apierr.CodeInvalidRequest)
	}
	if store.updated != nil {
		t.Fatalf("record must not be updated when url is rejected")
	}
}

// ── Official (system) namespace protection on create ────────────────────────

func TestCreateRejectsSystemNameCollision(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{
		ID:         "sys",
		Name:       "GitHub Official",
		Slug:       "github-official",
		Visibility: model.VisibilitySystem,
	})

	req := baseCreate()
	req.Name = "GitHub Official"
	req.Slug = "user-picked-different-slug"

	_, apiErr := svc.Create(context.Background(), caller, req)
	if apiErr == nil || apiErr.Code != apierr.CodeNameTaken {
		t.Fatalf("apiErr = %v, want DUPLICATE name_taken", apiErr)
	}
	if store.created != nil {
		t.Fatalf("record must not be created on system name collision")
	}
}

func TestCreateRejectsSystemSlugCollision(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{
		ID:         "sys",
		Name:       "GitHub Official",
		Slug:       "github",
		Visibility: model.VisibilitySystem,
	})

	req := baseCreate()
	req.Name = "My Totally Different Name"
	req.Slug = "github"

	_, apiErr := svc.Create(context.Background(), caller, req)
	if apiErr == nil || apiErr.Code != apierr.CodeSlugTaken {
		t.Fatalf("apiErr = %v, want DUPLICATE slug_taken", apiErr)
	}
}

// The guard is scoped to system rows only: colliding with a non-system row in
// the fake store must NOT be blocked here (Space-scoped uniqueness is the DB's
// job, not this service-level official-namespace check).
func TestCreateAllowsNameMatchingNonSystemRow(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{
		ID:         "peer",
		Name:       "GitHub Official",
		Slug:       "peer-slug",
		Visibility: model.VisibilityPublic,
		OwnerUID:   "someone-else",
		SpaceID:    "space-b",
	})

	req := baseCreate()
	req.Name = "GitHub Official"
	req.Slug = "my-own-slug"

	if _, apiErr := svc.Create(context.Background(), caller, req); apiErr != nil {
		t.Fatalf("collision with non-system row wrongly blocked: %v", apiErr)
	}
}

// ── Official (system) namespace protection on patch ─────────────────────────

func TestPatchRejectsRenameToSystemName(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{
		ID:         "sys",
		Name:       "GitHub Official",
		Slug:       "github-official",
		Visibility: model.VisibilitySystem,
	})
	seed(store, model.MCP{
		ID:         "own",
		Name:       "My MCP",
		Slug:       "my-mcp",
		Visibility: model.VisibilityPublic,
		OwnerUID:   "u1",
		SpaceID:    "space-a",
		Transport:  model.TransportStreamableHTTP,
		Connection: model.Connection{URL: "https://mcp.example.com/ok"},
	})

	newName := "GitHub Official"
	_, apiErr := svc.Patch(context.Background(), caller, "own", model.PatchRequest{Name: &newName})
	if apiErr == nil || apiErr.Code != apierr.CodeNameTaken {
		t.Fatalf("apiErr = %v, want DUPLICATE name_taken", apiErr)
	}
	if store.updated != nil {
		t.Fatalf("record must not be updated on system name collision")
	}
}

func TestPatchRejectsRenameToSystemSlug(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{
		ID:         "sys",
		Name:       "GitHub Official",
		Slug:       "github",
		Visibility: model.VisibilitySystem,
	})
	seed(store, model.MCP{
		ID:         "own",
		Name:       "My MCP",
		Slug:       "my-mcp",
		Visibility: model.VisibilityPublic,
		OwnerUID:   "u1",
		SpaceID:    "space-a",
		Transport:  model.TransportStreamableHTTP,
		Connection: model.Connection{URL: "https://mcp.example.com/ok"},
	})

	newSlug := "github"
	_, apiErr := svc.Patch(context.Background(), caller, "own", model.PatchRequest{Slug: &newSlug})
	if apiErr == nil || apiErr.Code != apierr.CodeSlugTaken {
		t.Fatalf("apiErr = %v, want DUPLICATE slug_taken", apiErr)
	}
}

// Regression: when the caller owns a system row (public Patch's ownership gate
// passes because m.OwnerUID == caller.UID), a no-op edit must NOT self-collide
// against the official-namespace check. exceptID=m.ID guards this.
func TestPatchOwnedSystemRowDoesNotSelfCollide(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	seed(store, model.MCP{
		ID:         "sys",
		Name:       "eewe",
		Slug:       "eewe",
		Visibility: model.VisibilitySystem,
		OwnerUID:   "u1", // same identity as `caller`
		Transport:  model.TransportStreamableHTTP,
		Connection: model.Connection{URL: "https://official.example.com/"},
	})

	newSlogan := "updated tagline"
	if _, apiErr := svc.Patch(context.Background(), caller, "sys", model.PatchRequest{Slogan: &newSlogan}); apiErr != nil {
		t.Fatalf("no-op edit of owned system row wrongly rejected: %v", apiErr)
	}
	if store.updated == nil || store.updated.Slogan != "updated tagline" {
		t.Fatalf("system row was not updated: %+v", store.updated)
	}
}
