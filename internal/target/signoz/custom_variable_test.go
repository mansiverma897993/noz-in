package signoz

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStableCustomSelectionRoundTripsPinnedParserSubset(t *testing.T) {
	t.Parallel()

	for _, values := range [][]string{
		{"prod"},
		{"prod", "stage"},
		{"us,west", "eu-central"},
		{`path\,with-comma`, `path\\,with-two-backslashes`},
		{"true", "NaN", "service:api"},
	} {
		encoded, ok := EncodeStableCustomSelection(values)
		require.True(t, ok, values)
		decoded, ok := DecodeStableCustomSelection(encoded)
		require.True(t, ok, encoded)
		assert.Equal(t, values, decoded)
	}
}

func TestStableCustomSelectionRejectsLossyPinnedParserValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"", " prod", "prod ", "display : prod", "1", "001", "1e3", ".5",
		"0x10", "0b10", "0o10", "Infinity", "+Infinity", "-Infinity", "line\nbreak",
		"\uFEFFprod", "prod\uFEFF", "\uFEFF1\uFEFF", "a\uFEFF:\uFEFFb",
	} {
		_, ok := EncodeStableCustomSelection([]string{value})
		assert.False(t, ok, value)
	}
	_, ok := EncodeStableCustomSelection(nil)
	assert.False(t, ok)
}

func TestStableCustomRuntimeValueNormalizesSingleAndMulti(t *testing.T) {
	t.Parallel()

	encoded, ok := EncodeStableCustomSelection([]string{"prod", "stage"})
	require.True(t, ok)
	multi, err := StableCustomRuntimeValue(encoded, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"prod", "stage"}, multi)
	single, err := StableCustomRuntimeValue(encoded, false)
	require.NoError(t, err)
	assert.Equal(t, "prod", single)

	_, err = StableCustomRuntimeValue("display : 1", true)
	require.Error(t, err)
}
