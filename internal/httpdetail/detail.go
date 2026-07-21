// Package httpdetail bounds and neutralizes untrusted response text before it
// is copied into errors, terminal output, and migration evidence.
package httpdetail

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxBytes is the maximum retained UTF-8 error detail from an upstream HTTP
	// response. Successful response bodies have separate, endpoint-level caps.
	MaxBytes = 64 << 10
	// TruncationMarker makes a bounded diagnostic distinguishable from the
	// complete upstream response.
	TruncationMarker = "... [truncated]"
)

// Text sanitizes and bounds already-decoded upstream response text.
func Text(value string) string {
	return sanitize(value, false)
}

// Bytes sanitizes and bounds an unstructured upstream response body without
// first copying the complete body into a string.
func Bytes(value []byte) string {
	truncated := len(value) > MaxBytes
	if truncated {
		value = value[:MaxBytes]
	}
	return sanitize(string(value), truncated)
}

func sanitize(value string, truncated bool) string {
	var builder strings.Builder
	builder.Grow(min(len(value), MaxBytes))
	lastWasSpace := false
	for offset := 0; offset < len(value); {
		character, size := utf8.DecodeRuneInString(value[offset:])
		offset += size
		if character == utf8.RuneError && size == 1 {
			character = '?'
		}
		if unicode.IsSpace(character) || unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			character = ' '
		}
		if character == ' ' {
			if builder.Len() == 0 || lastWasSpace {
				continue
			}
			lastWasSpace = true
		} else {
			lastWasSpace = false
		}
		encodedSize := utf8.RuneLen(character)
		if encodedSize < 0 {
			character = '?'
			encodedSize = 1
		}
		if builder.Len()+encodedSize > MaxBytes {
			truncated = true
			break
		}
		builder.WriteRune(character)
	}

	detail := strings.TrimSpace(builder.String())
	if !truncated {
		return detail
	}
	suffix := " " + TruncationMarker
	contentLimit := MaxBytes - len(suffix)
	if len(detail) > contentLimit {
		detail = strings.TrimSpace(validUTF8Prefix(detail, contentLimit))
	}
	if detail == "" {
		return TruncationMarker
	}
	return detail + suffix
}

func validUTF8Prefix(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	prefix := value[:limit]
	for !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix
}
