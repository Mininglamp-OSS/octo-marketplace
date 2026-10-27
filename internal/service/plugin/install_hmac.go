package plugin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// WithInstallationHMACKeys copies the deployment key ring. Rotation changes the
// active ID for new attempts while old IDs remain available for frozen retries.
// No key or environment value may appear in logs, errors, or stored plans.
func (s *Service) WithInstallationHMACKeys(active string, keys map[string]string) *Service {
	s.installHMACKeyID = active
	s.installHMACKeys = make(map[string][]byte, len(keys))
	for id, key := range keys {
		s.installHMACKeys[id] = []byte(key)
	}
	return s
}

func (s *Service) installationHMAC(keyID, domain string, value any) (string, error) {
	key := s.installHMACKeys[keyID]
	if keyID == "" || len(key) < 32 {
		return "", ErrVersionBaselineUnavailable
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", ErrInvalidRequest
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("marketplace-install-v2\x00" + domain + "\x00"))
	mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)), nil
}
func equalInstallationHMAC(a, b string) bool {
	aa, aerr := hex.DecodeString(a)
	bb, berr := hex.DecodeString(b)
	if aerr != nil || berr != nil || len(aa) != sha256.Size || len(bb) != sha256.Size {
		return false
	}
	return hmac.Equal(aa, bb)
}

// installationEligibilityHMAC authenticates the frozen governance graph and its
// operation context. RequestFingerprint binds the existing content/runtime MAC,
// so neither a plan nor its node/edge lists can be transplanted to another key,
// caller, Space or workspace. Only immutable fields participate; leases, outcome
// and deadlines may change without re-signing. No secret runtime values persist.
// Typed strings/arrays keep this MAC stable across MySQL JSON object formatting.
func (s *Service) installationEligibilityHMAC(caller Caller, a pluginrepo.InstallAttempt, plan *pinnedInstallationPlan) (string, error) {
	return s.installationHMAC(a.HMACKeyID, "eligibility-v1", struct {
		Space, Caller         string
		Workspace, Key        string
		Plugin, RootVersion   string
		Adapter, Intent       string
		Input, Request, KeyID string
		NodeIDs               []string
		Edges                 []planEligibilityEdge
	}{
		Space: caller.SpaceID, Caller: caller.UID, Workspace: a.WorkspaceID, Key: a.KeyDigest,
		Plugin: a.PluginID, RootVersion: plan.RootVersionID, Adapter: a.AdapterVersion,
		Intent: a.IntentDigest, Input: a.InputFingerprint, Request: a.RequestFingerprint, KeyID: a.HMACKeyID,
		NodeIDs: plan.NodeIDs, Edges: plan.Edges,
	})
}
