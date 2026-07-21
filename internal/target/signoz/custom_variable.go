package signoz

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

var (
	customDecimalNumber = regexp.MustCompile(`^[+-]?(?:(?:[0-9]+(?:\.[0-9]*)?)|(?:\.[0-9]+))(?:[eE][+-]?[0-9]+)?$`)
	customRadixNumber   = regexp.MustCompile(`^[+-]?0(?:[xX][0-9a-fA-F]+|[bB][01]+|[oO][0-7]+)$`)
)

// EncodeStableCustomSelection serializes a selected string list through the
// exact safe subset of SigNoz v0.133's customCommaValuesParser. SigNoz rebuilds
// CUSTOM defaults from customValue on every dashboard reload and ignores the
// persisted selectedValue, so only a round-tripping encoding is safe to emit.
func EncodeStableCustomSelection(values []string) (string, bool) {
	if len(values) == 0 {
		return "", false
	}
	parts := make([]string, len(values))
	for index, value := range values {
		if !stableCustomString(value) {
			return "", false
		}
		parts[index] = strings.ReplaceAll(value, ",", `\,`)
	}
	encoded := strings.Join(parts, ",")
	decoded, ok := DecodeStableCustomSelection(encoded)
	if !ok || !slices.Equal(decoded, values) {
		return "", false
	}
	return encoded, true
}

// DecodeStableCustomSelection parses the string-only, round-trippable subset
// accepted by SigNoz v0.133's customCommaValuesParser. Numeric coercion,
// display-label syntax, empty values, and lossy whitespace are rejected.
func DecodeStableCustomSelection(value string) ([]string, bool) {
	segments := splitCustomCommaValues(value)
	if len(segments) == 0 {
		return nil, false
	}
	decoded := make([]string, 0, len(segments))
	for _, segment := range segments {
		item := strings.ReplaceAll(segment, `\,`, ",")
		if !stableCustomString(item) {
			return nil, false
		}
		decoded = append(decoded, item)
	}
	return decoded, true
}

// StableCustomRuntimeValue returns the exact value selected by the pinned v5
// dashboard reload path for a strict customValue emitted by this project.
func StableCustomRuntimeValue(customValue string, multi bool) (any, error) {
	values, ok := DecodeStableCustomSelection(customValue)
	if !ok {
		return nil, fmt.Errorf("custom variable value is outside the stable v0.133 reload subset")
	}
	if multi {
		return values, nil
	}
	return values[0], nil
}

func splitCustomCommaValues(value string) []string {
	var result []string
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		result = append(result, current.String())
		current.Reset()
	}
	for index := 0; index < len(value); index++ {
		if value[index] == '\\' && index+1 < len(value) && value[index+1] == ',' {
			current.WriteByte('\\')
			current.WriteByte(',')
			index++
			continue
		}
		if value[index] == ',' {
			flush()
			continue
		}
		current.WriteByte(value[index])
	}
	flush()
	return result
}

func stableCustomString(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		// ECMAScript trim and the custom parser's whitespace matcher treat the
		// byte-order mark as whitespace even though Go's unicode.IsSpace and
		// strings.TrimSpace do not. Reject it anywhere so the target cannot trim
		// a value, coerce a number, or expose display-label syntax after reload.
		if character == '\uFEFF' || unicode.IsControl(character) {
			return false
		}
	}
	if hasCustomDisplayLabelSyntax(value) || customNumberCoercible(value) {
		return false
	}
	return true
}

func hasCustomDisplayLabelSyntax(value string) bool {
	runes := []rune(value)
	for index, character := range runes {
		if character == ':' && index > 1 && index+2 < len(runes) &&
			unicode.IsSpace(runes[index-1]) && unicode.IsSpace(runes[index+1]) {
			return true
		}
	}
	return false
}

func customNumberCoercible(value string) bool {
	if value == "Infinity" || value == "+Infinity" || value == "-Infinity" {
		return true
	}
	return customDecimalNumber.MatchString(value) || customRadixNumber.MatchString(value)
}
