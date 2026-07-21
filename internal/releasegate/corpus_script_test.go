package releasegate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type archiveFixtureEntry struct {
	name     string
	typeflag byte
	linkname string
	size     int64
	data     []byte
	reader   io.Reader
}

func TestPrepareReleaseCorpusScriptFailsClosedWithoutConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release workflow runs this shell boundary on Linux")
	}

	destination := filepath.Join(t.TempDir(), "corpus")
	output, err := runPrepareScript(t, destination, nil, "", false)
	require.Error(t, err)
	assert.Contains(t, output, "RELEASE_CORPUS_URL is required; release validation is fail-closed")
	assert.NoFileExists(t, destination)
	assert.NoDirExists(t, destination)
}

func TestPrepareReleaseCorpusScriptRejectsChecksumMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release workflow runs this shell boundary on Linux")
	}

	archive := buildArchive(t, validCorpusEntries()...)
	destination := filepath.Join(t.TempDir(), "corpus")
	output, err := runPrepareScript(t, destination, archive, strings.Repeat("0", 64), true)
	require.Error(t, err)
	assert.Contains(t, output, "release corpus SHA-256 mismatch")
	assert.NoDirExists(t, destination)
}

func TestPrepareReleaseCorpusScriptValidatesArchiveBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release workflow runs this shell boundary on Linux")
	}

	tests := []struct {
		name        string
		entries     func() []archiveFixtureEntry
		errorDetail string
	}{
		{
			name: "traversal",
			entries: func() []archiveFixtureEntry {
				return append(requiredDirectoryEntries(), regularEntry("corpus/../escape", "escape"))
			},
			errorDetail: "unsafe path",
		},
		{
			name: "symbolic link",
			entries: func() []archiveFixtureEntry {
				return append(requiredDirectoryEntries(), archiveFixtureEntry{
					name:     "corpus/top/link",
					typeflag: tar.TypeSymlink,
					linkname: "/tmp/outside",
				})
			},
			errorDetail: "unsupported type",
		},
		{
			name: "hard link",
			entries: func() []archiveFixtureEntry {
				return append(requiredDirectoryEntries(), archiveFixtureEntry{
					name:     "corpus/top/link",
					typeflag: tar.TypeLink,
					linkname: "corpus/top/dashboard.json",
				})
			},
			errorDetail: "unsupported type",
		},
		{
			name: "duplicate path",
			entries: func() []archiveFixtureEntry {
				return append(requiredDirectoryEntries(),
					regularEntry("corpus/top/duplicate.json", "first"),
					regularEntry("corpus/top/duplicate.json", "second"),
				)
			},
			errorDetail: "duplicate path",
		},
		{
			name: "too many entries",
			entries: func() []archiveFixtureEntry {
				entries := requiredDirectoryEntries()
				for index := len(entries); index <= maxArchiveEntries; index++ {
					entries = append(entries, regularEntry(fmt.Sprintf("corpus/top/entry-%04d.json", index), ""))
				}
				return entries
			},
			errorDetail: fmt.Sprintf("exceeds %d entries", maxArchiveEntries),
		},
		{
			name: "oversized expansion",
			entries: func() []archiveFixtureEntry {
				return append(requiredDirectoryEntries(), archiveFixtureEntry{
					name:     "corpus/top/oversized.json",
					typeflag: tar.TypeReg,
					size:     maxExpandedBytes + 1,
					reader:   zeroReader{},
				})
			},
			errorDetail: fmt.Sprintf("expands beyond %d bytes", maxExpandedBytes),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := buildArchive(t, test.entries()...)
			destination := filepath.Join(t.TempDir(), "corpus")
			output, err := runPrepareScript(t, destination, archive, sha256Hex(archive), true)
			require.Error(t, err)
			assert.Contains(t, output, test.errorDetail)
			assert.NoDirExists(t, destination)
		})
	}
}

func TestPrepareReleaseCorpusScriptExtractsVerifiedArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release workflow runs this shell boundary on Linux")
	}

	archive := buildArchive(t, validCorpusEntries()...)
	destination := filepath.Join(t.TempDir(), "corpus")
	output, err := runPrepareScript(t, destination, archive, sha256Hex(archive), true)
	require.NoError(t, err, output)
	assert.Contains(t, output, "prepared hash-verified release corpus")
	assert.FileExists(t, filepath.Join(destination, "corpus", "top", "dashboard.json"))
	assert.FileExists(t, filepath.Join(destination, "corpus", "mixin", "mixin.json"))
	assert.FileExists(t, filepath.Join(destination, "corpus-complex", "rules.yaml"))
	data, readErr := os.ReadFile(filepath.Join(destination, "corpus", "top", "dashboard.json"))
	require.NoError(t, readErr)
	assert.JSONEq(t, `{"title":"test"}`, string(data))
}

func runPrepareScript(
	t *testing.T,
	destination string,
	archive []byte,
	expectedSHA256 string,
	serveArchive bool,
) (string, error) {
	t.Helper()

	repositoryRoot := filepath.Clean(filepath.Join(packageDirectory(t), "..", ".."))
	command := exec.CommandContext(t.Context(), filepath.Join(repositoryRoot, "scripts", "prepare-release-corpus.sh"), destination)
	command.Dir = repositoryRoot
	command.Env = environmentWithout(
		os.Environ(),
		"RELEASE_CORPUS_URL",
		"RELEASE_CORPUS_SHA256",
		"CURL_CA_BUNDLE",
		"SSL_CERT_FILE",
		"NO_PROXY",
	)
	if !serveArchive {
		output, err := command.CombinedOutput()
		return string(output), err
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodGet, request.Method)
		writer.Header().Set("Content-Type", "application/gzip")
		_, _ = writer.Write(archive)
	}))
	t.Cleanup(server.Close)
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	certificatePath := filepath.Join(t.TempDir(), "release-corpus-ca.pem")
	require.NoError(t, os.WriteFile(certificatePath, certificate, 0o600))
	command.Env = append(command.Env,
		"RELEASE_CORPUS_URL="+server.URL,
		"RELEASE_CORPUS_SHA256="+expectedSHA256,
		"CURL_CA_BUNDLE="+certificatePath,
		"SSL_CERT_FILE="+certificatePath,
		"NO_PROXY=127.0.0.1,localhost",
	)
	output, err := command.CombinedOutput()
	return string(output), err
}

func packageDirectory(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Dir(filename)
}

func environmentWithout(environment []string, names ...string) []string {
	filtered := make([]string, 0, len(environment))
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		if !slices.Contains(names, name) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func validCorpusEntries() []archiveFixtureEntry {
	return append(requiredDirectoryEntries(),
		regularEntry("corpus/top/dashboard.json", `{"title":"test"}`),
		regularEntry("corpus/mixin/mixin.json", `{}`),
		regularEntry("corpus-complex/rules.yaml", "groups: []\n"),
	)
}

func requiredDirectoryEntries() []archiveFixtureEntry {
	return []archiveFixtureEntry{
		{name: "corpus/", typeflag: tar.TypeDir},
		{name: "corpus/top/", typeflag: tar.TypeDir},
		{name: "corpus/mixin/", typeflag: tar.TypeDir},
		{name: "corpus-complex/", typeflag: tar.TypeDir},
	}
}

func regularEntry(name, data string) archiveFixtureEntry {
	return archiveFixtureEntry{
		name:     name,
		typeflag: tar.TypeReg,
		size:     int64(len(data)),
		data:     []byte(data),
	}
}

func buildArchive(t *testing.T, entries ...archiveFixtureEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter, err := gzip.NewWriterLevel(&output, gzip.BestSpeed)
	require.NoError(t, err)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{
			Name:     entry.name,
			Mode:     0o600,
			Size:     entry.size,
			Typeflag: entry.typeflag,
			Linkname: entry.linkname,
		}
		if entry.typeflag == tar.TypeDir {
			header.Mode = 0o700
		}
		require.NoError(t, tarWriter.WriteHeader(header))
		reader := entry.reader
		if reader == nil {
			reader = bytes.NewReader(entry.data)
		}
		if entry.size > 0 {
			written, copyErr := io.CopyN(tarWriter, reader, entry.size)
			require.NoError(t, copyErr)
			require.Equal(t, entry.size, written)
		}
	}
	require.NoError(t, tarWriter.Close())
	require.NoError(t, gzipWriter.Close())
	return output.Bytes()
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}
