package httpdetail

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestTextSanitizesTerminalControlsAndFormatting(t *testing.T) {
	t.Parallel()

	detail := Text("  before\x1b[31m\r\n\t\u202eafter  ")
	assert.Equal(t, "before [31m after", detail)
	assert.NotContains(t, detail, "\x1b")
	assert.NotContains(t, detail, "\n")
	assert.NotContains(t, detail, "\u202e")
}

func TestTextBoundsUTF8WithExplicitTruncationMarker(t *testing.T) {
	t.Parallel()

	detail := Text(strings.Repeat("界", MaxBytes))
	assert.LessOrEqual(t, len(detail), MaxBytes)
	assert.True(t, utf8.ValidString(detail))
	assert.True(t, strings.HasSuffix(detail, TruncationMarker))
}

func TestBytesBoundsBeforeConvertingCompleteBody(t *testing.T) {
	t.Parallel()

	data := append([]byte("prefix\x00\xff"), []byte(strings.Repeat("x", MaxBytes*2))...)
	detail := Bytes(data)
	assert.LessOrEqual(t, len(detail), MaxBytes)
	assert.True(t, utf8.ValidString(detail))
	assert.Contains(t, detail, "prefix ?")
	assert.True(t, strings.HasSuffix(detail, TruncationMarker))
}
