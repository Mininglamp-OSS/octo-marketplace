package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"go.uber.org/zap"
)

type Resolver interface {
	Resolve(ctx context.Context, token string) (model.Identity, error)
}

type HTTPResolver struct {
	baseURL string
	client  *http.Client

	// driftLogOnce suppresses the flood of warnings when octo-server sends an
	// out-of-range SpaceRoles value (e.g. it drifts onto the inverted octo-web
	// encoding). A bad value arrives on EVERY verify response so a per-request
	// log line would be a DoS on our own log; one log per distinct bad value
	// keeps the signal visible once per process boot without amplification.
	driftLogOnce map[int]*sync.Once
	driftMu      sync.Mutex
}

func NewHTTPResolver(baseURL string) *HTTPResolver {
	return &HTTPResolver{
		baseURL:      baseURL,
		client:       &http.Client{Timeout: 5 * time.Second},
		driftLogOnce: make(map[int]*sync.Once),
	}
}

func (r *HTTPResolver) Resolve(ctx context.Context, token string) (model.Identity, error) {
	if token == "" {
		return model.Identity{}, nil
	}
	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return model.Identity{}, fmt.Errorf("encode verify request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/v1/auth/verify?include=context", bytes.NewReader(body))
	if err != nil {
		return model.Identity{}, fmt.Errorf("create verify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return model.Identity{}, fmt.Errorf("verify token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return model.Identity{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return model.Identity{}, fmt.Errorf("verify token returned status %d", resp.StatusCode)
	}
	var identity model.Identity
	if err := json.NewDecoder(resp.Body).Decode(&identity); err != nil {
		return model.Identity{}, fmt.Errorf("decode verify response: %w", err)
	}
	r.clampSpaceRoles(identity.SpaceRoles)
	return identity, nil
}

// clampSpaceRoles walks the decoded SpaceRoles map and replaces every out-of-
// range entry with model.SpaceRoleMember.
//
// Why clamp HERE (decoder output) rather than only in Identity.SpaceRole? The
// decode is the true ingress chokepoint: every production request flows
// through HTTPResolver.Resolve (directly, or via CachedResolver), and clamping
// at decode keeps the stored Identity safe even if a future caller reads
// SpaceRoles directly instead of going through Identity.SpaceRole. The two
// other identity producers already carry safe values:
//
//   - Bot auth (middleware.authenticateBot) builds an Identity with an empty
//     SpaceRoles map, which yields SpaceRoleMember everywhere.
//   - Dev identities are range-validated at boot by Config.validateAPI (which
//     enforces 0..2 on DEV_AUTH_SPACE_ROLE exactly like this clamp).
//
// A drifted octo-server emits the bad value on every response, so a naive Warn
// per request turns into log-flood self-DoS. We log at most once per distinct
// bad integer value per process life, which is loud enough to be noticed in a
// log-tail but bounded.
func (r *HTTPResolver) clampSpaceRoles(roles map[string]int) {
	if len(roles) == 0 {
		return
	}
	for spaceID, v := range roles {
		clamped, bad := model.ClampSpaceRole(v)
		if !bad {
			continue
		}
		roles[spaceID] = clamped
		r.onceForBadValue(v).Do(func() {
			logging.Warn("auth_space_role_drift",
				zap.String("operation", "auth.resolve"),
				zap.Int("received_role", v),
				zap.Int("clamped_to", clamped),
				zap.String("expected_range", "0..2 (0=member, 1=admin, 2=owner)"),
				zap.String("hint", "octo-server is sending an out-of-range space_roles value — likely the inverted octo-web encoding (1=owner,2=admin,3=member). All such values are being treated as plain member; review-authority grants fail closed until the upstream is fixed."),
			)
		})
	}
}

func (r *HTTPResolver) onceForBadValue(v int) *sync.Once {
	r.driftMu.Lock()
	defer r.driftMu.Unlock()
	o, ok := r.driftLogOnce[v]
	if !ok {
		o = &sync.Once{}
		r.driftLogOnce[v] = o
	}
	return o
}
