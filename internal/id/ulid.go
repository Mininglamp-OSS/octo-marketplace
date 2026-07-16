// Package id generates ULID-style opaque identifiers for marketplace records.
//
// A ULID is a 128-bit value rendered as 26 Crockford base32 characters: the
// leading 48 bits encode the millisecond timestamp (so IDs sort by creation
// time), the trailing 80 bits are random. The wire contract (docs/api/mcp-v1.md
// §3.1) treats the value as opaque; this package only guarantees the shape and
// the monotonic-by-time ordering the schema comment relies on.
//
// Implemented against the standard library only (crypto/rand), matching the
// repository convention of not adding a dependency until one is required.
package id

import (
	"crypto/rand"
	"encoding/binary"
	"time"
)

// crockford is the Crockford base32 alphabet used by the ULID spec: it omits
// I, L, O, and U to avoid visual ambiguity.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// New returns a fresh 26-character ULID-style identifier.
func New() string {
	return newAt(time.Now())
}

func newAt(t time.Time) string {
	var raw [16]byte

	ms := uint64(t.UnixMilli())
	// Timestamp occupies the high 48 bits (6 bytes).
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)

	// Remaining 80 bits (10 bytes) are randomness.
	if _, err := rand.Read(raw[6:]); err != nil {
		// crypto/rand.Read never returns an error on supported platforms; fall
		// back to a time-derived filler so the process still makes progress
		// rather than panicking on an impossible branch.
		filler := binary.BigEndian.Uint64([]byte{raw[0], raw[1], raw[2], raw[3], raw[4], raw[5], 0, 0})
		for i := 6; i < 16; i++ {
			raw[i] = byte(filler >> (uint(i) % 8 * 8))
		}
	}

	return encode(raw)
}

// encode renders the 16 raw bytes as 26 Crockford base32 characters. 26 * 5 =
// 130 bits covers the 128-bit value; the top two bits of the first character
// are always zero.
func encode(raw [16]byte) string {
	out := make([]byte, 26)

	out[0] = crockford[(raw[0]&224)>>5]
	out[1] = crockford[raw[0]&31]
	out[2] = crockford[(raw[1]&248)>>3]
	out[3] = crockford[((raw[1]&7)<<2)|((raw[2]&192)>>6)]
	out[4] = crockford[(raw[2]&62)>>1]
	out[5] = crockford[((raw[2]&1)<<4)|((raw[3]&240)>>4)]
	out[6] = crockford[((raw[3]&15)<<1)|((raw[4]&128)>>7)]
	out[7] = crockford[(raw[4]&124)>>2]
	out[8] = crockford[((raw[4]&3)<<3)|((raw[5]&224)>>5)]
	out[9] = crockford[raw[5]&31]

	out[10] = crockford[(raw[6]&248)>>3]
	out[11] = crockford[((raw[6]&7)<<2)|((raw[7]&192)>>6)]
	out[12] = crockford[(raw[7]&62)>>1]
	out[13] = crockford[((raw[7]&1)<<4)|((raw[8]&240)>>4)]
	out[14] = crockford[((raw[8]&15)<<1)|((raw[9]&128)>>7)]
	out[15] = crockford[(raw[9]&124)>>2]
	out[16] = crockford[((raw[9]&3)<<3)|((raw[10]&224)>>5)]
	out[17] = crockford[raw[10]&31]
	out[18] = crockford[(raw[11]&248)>>3]
	out[19] = crockford[((raw[11]&7)<<2)|((raw[12]&192)>>6)]
	out[20] = crockford[(raw[12]&62)>>1]
	out[21] = crockford[((raw[12]&1)<<4)|((raw[13]&240)>>4)]
	out[22] = crockford[((raw[13]&15)<<1)|((raw[14]&128)>>7)]
	out[23] = crockford[(raw[14]&124)>>2]
	out[24] = crockford[((raw[14]&3)<<3)|((raw[15]&224)>>5)]
	out[25] = crockford[raw[15]&31]

	return string(out)
}
