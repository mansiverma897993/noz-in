package stableidentity

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSum256PreservesSafeLegacyIDs(t *testing.T) {
	parts := []string{"grafana", "production", "dashboard-uid"}
	legacy := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	assert.Equal(t, legacy, Sum256(parts...))
}

func TestSum256FramesUnsafeComponentBoundaries(t *testing.T) {
	left := Sum256("source\x00id", "dashboard")
	right := Sum256("source", "id\x00dashboard")
	assert.NotEqual(t, left, right)
}

func TestValidateComponentRejectsControlFormattingAndOversize(t *testing.T) {
	require.NoError(t, ValidateComponent("namespace", "grafana:production east", 64))
	for _, value := range []string{"grafana\x00production", "grafana\nproduction", "grafana\u202eproduction", "grafana\u2028production"} {
		assert.Error(t, ValidateComponent("namespace", value, 64), value)
	}
	assert.Error(t, ValidateComponent("namespace", strings.Repeat("x", 65), 64))
}
