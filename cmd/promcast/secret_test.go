package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretFileResolversUseSharedBoundedReader(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("  "+testMCPHTTPToken+"\n"), 0o600))

	apiKey, err := resolveAPIKey("", path)
	require.NoError(t, err)
	assert.Equal(t, testMCPHTTPToken, apiKey)

	optional, err := resolveOptionalSecret(path, "IGNORED_SECRET_ENVIRONMENT")
	require.NoError(t, err)
	assert.Equal(t, testMCPHTTPToken, optional)

	httpToken, generated, err := resolveMCPHTTPToken(path)
	require.NoError(t, err)
	assert.Equal(t, testMCPHTTPToken, httpToken)
	assert.False(t, generated)
}

func TestReadBoundedSecretRejectsOverflow(t *testing.T) {
	t.Parallel()

	accepted, err := readBoundedSecret(bytes.NewReader(make([]byte, maxSecretFileBytes)))
	require.NoError(t, err)
	assert.Len(t, accepted, int(maxSecretFileBytes))

	_, err = readBoundedSecret(bytes.NewReader(make([]byte, maxSecretFileBytes+1)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
	assert.Contains(t, err.Error(), "65536-byte limit")
}

func TestSecretFileResolversRejectOversizedRegularFiles(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "oversized-token")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(maxSecretFileBytes+1))
	require.NoError(t, file.Close())

	_, err = resolveAPIKey("", path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestBoundedSecretFileRejectsDevicesWithoutReadingThem(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("/dev/zero is a Unix device")
	}
	if _, err := os.Stat("/dev/zero"); err != nil {
		t.Skipf("/dev/zero is unavailable: %v", err)
	}

	_, err := readBoundedSecretFile("/dev/zero")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "regular file")
}
