// Package stableidentity validates and hashes user-controlled identity
// components used by idempotent target upserts.
package stableidentity

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateComponent rejects values that are ambiguous in logs, JSON, or the
// legacy NUL-delimited stable-ID encoding. Empty values remain valid because
// several identity components are optional.
func ValidateComponent(field, value string, maxBytes int) error {
	if maxBytes <= 0 {
		return fmt.Errorf("stable identity component %s has an invalid size limit", field)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("stable identity component %s exceeds %d bytes", field, maxBytes)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("stable identity component %s is not valid UTF-8", field)
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp) {
			return fmt.Errorf("stable identity component %s contains control or formatting characters", field)
		}
	}
	return nil
}

// Sum256 preserves the historical SHA-256 result for safe components. Unsafe
// direct callers receive a length-framed SHA-512/256 digest so embedded NULs
// cannot create component-boundary collisions. Public migration entry points
// reject those characters before artifacts or target requests are made.
func Sum256(parts ...string) [32]byte {
	unsafe := false
	for _, part := range parts {
		if strings.ContainsRune(part, '\x00') {
			unsafe = true
			break
		}
	}
	if !unsafe {
		return sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	}
	hash := sha512.New512_256()
	_, _ = hash.Write([]byte("promcast/stable-identity/framed/v1"))
	var length [8]byte
	for _, part := range parts {
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(part))
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}
