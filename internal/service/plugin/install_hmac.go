package plugin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
func equalInstallationHMAC(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }
