package secrets

import (
	"crypto/rand"
	"errors"
)

// Kept character-for-character in step with the browser generator in
// frontend/src/components/secretGenerator.js, so a secret generated here and
// one generated there are drawn from the same alphabet.
const (
	passwordAlphanumeric = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	passwordSymbols      = "!@#$%^&*()-_=+[]{}"
)

// Bounds for a generated password. The floor is well above the browser
// generator's, which allows 1: a value chosen here can never be read back by
// the caller that asked for it, so there is no way to discover after the fact
// that it was too short to be worth anything.
const (
	MinPasswordLength     = 16
	MaxPasswordLength     = 4096
	DefaultPasswordLength = 32
)

var ErrPasswordLength = errors.New("password length out of range")

// GeneratePassword returns a uniformly random password of the given length.
// The result is a byte slice rather than a string so the caller can zero it
// once it has been sealed; a string would leave a copy on the heap until the
// collector got round to it.
func GeneratePassword(length int, includeSymbols bool) ([]byte, error) {
	if length == 0 {
		length = DefaultPasswordLength
	}
	if length < MinPasswordLength || length > MaxPasswordLength {
		return nil, ErrPasswordLength
	}
	alphabet := passwordAlphanumeric
	if includeSymbols {
		alphabet += passwordSymbols
	}
	out := make([]byte, length)
	if err := fillFromAlphabet(out, alphabet); err != nil {
		return nil, err
	}
	return out, nil
}

// fillFromAlphabet draws each byte by rejection sampling. Taking the raw byte
// modulo the alphabet size would bias the first 256%n characters upwards,
// which for a 62-character alphabet means eight of them turn up ~1.6% more
// often than the rest.
func fillFromAlphabet(out []byte, alphabet string) error {
	n := len(alphabet)
	// An int, not a byte: an alphabet whose size divides 256 evenly gives a
	// limit of exactly 256, which would wrap to 0 as a byte and reject every
	// draw forever.
	limit := 256 - (256 % n)
	// Over-read, because roughly (256-limit)/256 of the draws are discarded and
	// a per-byte read would mean one syscall-backed call per character.
	buf := make([]byte, len(out)+len(out)/4+16)
	filled, pos := 0, len(buf)
	for filled < len(out) {
		if pos == len(buf) {
			if _, err := rand.Read(buf); err != nil {
				return err
			}
			pos = 0
		}
		b := int(buf[pos])
		pos++
		if b >= limit {
			continue
		}
		out[filled] = alphabet[b%n]
		filled++
	}
	return nil
}

func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
