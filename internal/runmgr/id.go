package runmgr

import (
	"crypto/rand"
	"errors"
)

// crockfordAlphabet is Douglas Crockford's base32 alphabet: 0-9, A-Z minus
// I, L, O, U. Chosen for the short IDs surfaced by `slackrun runs` and
// `slackrun kill` — case-insensitive on decode, no ambiguous glyphs.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const shortIDLen = 8 // 8 chars * 5 bits = 40 bits of entropy

// newShortID returns a fresh 8-char Crockford-Base32 ID. Panics only if the
// system's CSPRNG is broken — treated as fatal at manager construction time.
func newShortID() (string, error) {
	// 5 bytes = 40 bits → exactly 8 base32 chars.
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	// Pack 40 bits into 8 groups of 5 bits.
	var out [shortIDLen]byte
	// Bit layout: 5 bytes = 40 bits = 8 × 5-bit groups.
	// group i = bits (35 - 5*i) .. (35 - 5*i + 4) of the 40-bit value.
	v := uint64(b[0])<<32 | uint64(b[1])<<24 | uint64(b[2])<<16 | uint64(b[3])<<8 | uint64(b[4])
	for i := 0; i < shortIDLen; i++ {
		shift := uint((shortIDLen - 1 - i) * 5)
		out[i] = crockfordAlphabet[(v>>shift)&0x1f]
	}
	return string(out[:]), nil
}

// ErrIDGeneration is returned by Register when the CSPRNG fails repeatedly.
// Practically unreachable; kept as a first-class error so callers can log.
var ErrIDGeneration = errors.New("failed to generate unique short id")
