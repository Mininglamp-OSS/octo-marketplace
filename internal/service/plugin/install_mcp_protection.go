package plugin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
)

// MCP configuration may contain credentials anywhere in headers, env, args or
// URLs. Protect the complete configuration rather than guessing secret fields.
// Derive a separate encryption key from the deployment key ring; never use a
// request/eligibility MAC as an encryption key. Old key IDs remain required.
func (s *Service) frozenMCPAEAD(keyID string) (cipher.AEAD, error) {
	derived, err := s.installationHMAC(keyID, "mcp-protection-key-v1", nil)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(derived)
	if err != nil {
		return nil, ErrIntegrity
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrIntegrity
	}
	return cipher.NewGCM(block)
}

func (s *Service) protectFrozenMCP(keyID string, plan *pinnedInstallationPlan) error {
	if plan == nil || plan.EligibilityHMAC == "" || len(plan.ProtectedMCPConfigs) != 0 {
		return ErrIntegrity
	}
	configs := make([]json.RawMessage, len(plan.Definition.Experts))
	for i, e := range plan.Definition.Experts {
		configs[i] = e.MCPConfig
	}
	raw, err := json.Marshal(configs)
	if err != nil {
		return ErrIntegrity
	}
	aead, err := s.frozenMCPAEAD(keyID)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return ErrIntegrity
	}
	// The authenticated eligibility MAC binds caller/Space/workspace/key, root,
	// request fingerprint and selected graph. A ciphertext cannot move operations.
	aad := []byte("marketplace-install-mcp-v1\x00" + plan.EligibilityHMAC)
	plan.ProtectedMCPConfigs = append([]byte{1}, nonce...)
	plan.ProtectedMCPConfigs = aead.Seal(plan.ProtectedMCPConfigs, nonce, raw, aad)
	for i := range plan.Definition.Experts {
		plan.Definition.Experts[i].MCPConfig = nil
	}
	return nil
}

// Call only after verifying eligibility integrity and current permissions.
// Unprotected legacy plans fail closed without rewriting or resetting evidence.
func (s *Service) restoreFrozenMCP(keyID string, plan *pinnedInstallationPlan) error {
	if plan == nil || plan.EligibilityHMAC == "" || len(plan.ProtectedMCPConfigs) > maxCapabilityBytes+64 {
		return ErrIntegrity
	}
	for _, e := range plan.Definition.Experts {
		if len(e.MCPConfig) != 0 {
			return ErrIntegrity
		}
	}
	aead, err := s.frozenMCPAEAD(keyID)
	if err != nil {
		return err
	}
	blob := plan.ProtectedMCPConfigs
	if len(blob) < 1+aead.NonceSize()+aead.Overhead() || blob[0] != 1 {
		return ErrIntegrity
	}
	nonce := blob[1 : 1+aead.NonceSize()]
	aad := []byte("marketplace-install-mcp-v1\x00" + plan.EligibilityHMAC)
	raw, err := aead.Open(nil, nonce, blob[1+aead.NonceSize():], aad)
	if err != nil {
		return ErrIntegrity
	}
	var configs []json.RawMessage
	if json.Unmarshal(raw, &configs) != nil || len(configs) != len(plan.Definition.Experts) {
		return ErrIntegrity
	}
	for i, config := range configs {
		if string(config) != "null" {
			plan.Definition.Experts[i].MCPConfig = config
		}
	}
	return nil
}
