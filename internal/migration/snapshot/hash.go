package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func cellHash(c []*[]byte) string {
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
