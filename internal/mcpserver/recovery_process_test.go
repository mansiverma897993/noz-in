package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const recoveryChildExitCode = 86

func TestMCPProcessRecovery(t *testing.T) {
	if os.Getenv("PROMCAST_RECOVERY_CHILD") == "1" {
		runMCPRecoveryChild(t)
		return
	}

	for _, test := range []struct {
		name          string
		barrier       string
		wantAttempt   bool
		wantResult    bool
		wantPOSTCount int32
	}{
		{name: "pre-attempt workspace", barrier: "migration-work-created"},
		{name: "initial inventory before payload", barrier: "migration-initial-inventory"},
		{name: "attempt published before HTTP", barrier: "migration-attempt-published", wantAttempt: true},
		{name: "target completed before final staging", barrier: "migration-target-completed", wantAttempt: true, wantPOSTCount: 1},
		{name: "result inventory before payload", barrier: "migration-result-inventory", wantAttempt: true, wantPOSTCount: 1},
		{name: "post-success result staged", barrier: "migration-result-prepared", wantAttempt: true, wantPOSTCount: 1},
		{name: "result installed before pointer", barrier: "migration-result-installed", wantResult: true, wantPOSTCount: 1},
		{name: "result pointer installed before cleanup", barrier: "migration-result-pointer-published", wantResult: true, wantPOSTCount: 1},
		{name: "cleanup after result inventory", barrier: "recovery-cleaning-result-removed", wantResult: true, wantPOSTCount: 1},
		{name: "cleanup after all inventories", barrier: "recovery-cleaning-initial-removed", wantResult: true, wantPOSTCount: 1},
		{name: "cleanup after plan", barrier: "recovery-cleaning-plan-removed", wantResult: true, wantPOSTCount: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "out")
			var posts atomic.Int32
			target := newRecoveryTarget(t, func() { posts.Add(1) })
			defer target.Close()

			command := recoveryChildCommand(t, root, output, target.URL, "migration", test.barrier, "")
			data, err := command.CombinedOutput()
			requireRecoveryChildExit(t, err, data)
			assert.Equal(t, test.wantPOSTCount, posts.Load())

			_, err = New(Config{Root: root, OutputRoot: output})
			require.NoError(t, err)
			assert.NoDirExists(t, filepath.Join(output, mcpWorkRootName))
			switch {
			case test.wantResult:
				assertRecoveredResult(t, output)
			case test.wantAttempt:
				require.NoError(t, inspectDurableAttempt(output))
			default:
				entries, readErr := os.ReadDir(output)
				require.NoError(t, readErr)
				assert.Empty(t, entries)
			}
		})
	}
}

func TestMCPInitialResultProcessRecovery(t *testing.T) {
	if os.Getenv("PROMCAST_RECOVERY_CHILD") == "1" {
		runMCPRecoveryChild(t)
		return
	}

	for _, test := range []struct {
		name            string
		mode            string
		withTarget      bool
		expectedAction  string
		importRequested bool
	}{
		{name: "requested import skipped before attempt", mode: "migration-no-executable", withTarget: true, expectedAction: "skipped", importRequested: true},
		{name: "offline non-import", mode: "migration-offline", expectedAction: "offline"},
		{name: "live dry run", mode: "migration-dry-run", withTarget: true, expectedAction: "dry_run"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "out")
			var posts atomic.Int32
			target := newRecoveryTarget(t, func() { posts.Add(1) })
			defer target.Close()
			targetURL := ""
			if test.withTarget {
				targetURL = target.URL
			}

			command := recoveryChildCommand(t, root, output, targetURL, test.mode, "migration-initial-installed", "")
			data, err := command.CombinedOutput()
			requireRecoveryChildExit(t, err, data)
			assert.Zero(t, posts.Load())

			_, err = New(Config{Root: root, OutputRoot: output})
			require.NoError(t, err)
			assert.NoDirExists(t, filepath.Join(output, mcpWorkRootName))
			assertRecoveredInitialResult(t, output, test.expectedAction, test.importRequested)
		})
	}
}

func TestMCPPrivateStagingRecoveryUsesPersistedParent(t *testing.T) {
	if os.Getenv("PROMCAST_RECOVERY_CHILD") == "1" {
		runMCPRecoveryChild(t)
		return
	}

	root := t.TempDir()
	output := filepath.Join(root, "out")
	tempA := filepath.Join(root, "temp-a")
	tempB := filepath.Join(root, "temp-b")
	require.NoError(t, os.Mkdir(tempA, 0o700))
	require.NoError(t, os.Mkdir(tempB, 0o700))
	t.Setenv("TMPDIR", tempA)
	target := newRecoveryTarget(t, func() {})
	defer target.Close()
	command := recoveryChildCommand(t, root, output, target.URL, "migration", "migration-private-staging-ready", "")
	data, err := command.CombinedOutput()
	requireRecoveryChildExit(t, err, data)
	assertPrivateStagingCount(t, tempA, 1)

	t.Setenv("TMPDIR", tempB)
	_, err = New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)
	assertPrivateStagingCount(t, tempA, 0)
	assertPrivateStagingCount(t, tempB, 0)
	assert.NoDirExists(t, filepath.Join(output, mcpWorkRootName))
}

func TestMCPPrivateStagingRecoveryAllowsMissingPersistedParent(t *testing.T) {
	if os.Getenv("PROMCAST_RECOVERY_CHILD") == "1" {
		runMCPRecoveryChild(t)
		return
	}

	root := t.TempDir()
	output := filepath.Join(root, "out")
	tempA := filepath.Join(root, "ephemeral-temp")
	tempB := filepath.Join(root, "restart-temp")
	require.NoError(t, os.Mkdir(tempA, 0o700))
	require.NoError(t, os.Mkdir(tempB, 0o700))
	t.Setenv("TMPDIR", tempA)
	target := newRecoveryTarget(t, func() {})
	defer target.Close()
	command := recoveryChildCommand(t, root, output, target.URL, "migration", "migration-private-staging-ready", "")
	data, err := command.CombinedOutput()
	requireRecoveryChildExit(t, err, data)
	assertPrivateStagingCount(t, tempA, 1)
	require.NoError(t, os.RemoveAll(tempA))

	t.Setenv("TMPDIR", tempB)
	_, err = New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)
	assert.NoDirExists(t, filepath.Join(output, mcpWorkRootName))
}

func TestMCPProcessRecoveryWhileTargetRequestIsInFlight(t *testing.T) {
	if os.Getenv("PROMCAST_RECOVERY_CHILD") == "1" {
		runMCPRecoveryChild(t)
		return
	}

	root := t.TempDir()
	output := filepath.Join(root, "out")
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	target := newRecoveryTargetWithPOST(t, func(writer http.ResponseWriter) {
		started <- struct{}{}
		<-release
		writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"id": "too-late"}})
	})
	defer target.Close()

	command := recoveryChildCommand(t, root, output, target.URL, "migration", "", "")
	require.NoError(t, command.Start())
	select {
	case <-started:
	case <-time.After(20 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("child did not reach the target dashboard request")
	}
	require.NoError(t, command.Process.Kill())
	_ = command.Wait()
	close(release)

	_, err := New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)
	require.NoError(t, inspectDurableAttempt(output))
	assert.NoDirExists(t, filepath.Join(output, mcpWorkRootName))
}

func TestMCPValidationProcessRecovery(t *testing.T) {
	if os.Getenv("PROMCAST_RECOVERY_CHILD") == "1" {
		runMCPRecoveryChild(t)
		return
	}

	for _, test := range []struct {
		name     string
		barrier  string
		wantRuns int
	}{
		{name: "validation work before publication", barrier: "validation-work-created"},
		{name: "validation inventory before payload", barrier: "validation-inventory"},
		{name: "validation installed before cleanup", barrier: "validation-installed", wantRuns: 1},
		{name: "validation cleanup after inventory", barrier: "recovery-cleaning-validation-removed", wantRuns: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "out")
			migrationID := createRecoveryMigration(t, root, output)
			target := newRecoveryTarget(t, func() {})
			defer target.Close()

			command := recoveryChildCommand(t, root, output, target.URL, "validation", test.barrier, migrationID)
			data, err := command.CombinedOutput()
			requireRecoveryChildExit(t, err, data)

			service, err := New(Config{Root: root, OutputRoot: output})
			require.NoError(t, err)
			assert.NoDirExists(t, filepath.Join(output, mcpWorkRootName))
			validationRoot := filepath.Join(output, migrationID, "validations")
			entries, readErr := os.ReadDir(validationRoot)
			if test.wantRuns == 0 {
				if errors.Is(readErr, os.ErrNotExist) {
					return
				}
				require.NoError(t, readErr)
				assert.Empty(t, entries)
				return
			}
			require.NoError(t, readErr)
			require.Len(t, entries, test.wantRuns)
			require.True(t, entries[0].IsDir())
			require.NoError(t, service.verifyValidationRun(filepath.Join(migrationID, "validations", entries[0].Name())))
		})
	}
}

func runMCPRecoveryChild(t *testing.T) {
	root := os.Getenv("PROMCAST_RECOVERY_ROOT")
	output := os.Getenv("PROMCAST_RECOVERY_OUTPUT")
	target := os.Getenv("PROMCAST_RECOVERY_TARGET")
	barrier := os.Getenv("PROMCAST_RECOVERY_BARRIER")
	mode := os.Getenv("PROMCAST_RECOVERY_MODE")
	service, err := New(Config{
		Root: root, OutputRoot: output, TargetURL: target, APIKey: "recovery-test-key", Workers: 1,
	})
	require.NoError(t, err)
	service.crashBarrier = func(name string) {
		if name == barrier {
			os.Exit(recoveryChildExitCode)
		}
	}
	switch mode {
	case "migration", "migration-no-executable", "migration-offline", "migration-dry-run", "migration-with-rule":
		grafanaJSON := `{"uid":"process-recovery","title":"Process recovery","panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]}`
		importRequested := true
		if mode == "migration-no-executable" {
			grafanaJSON = `{"uid":"process-recovery-empty","title":"Process recovery empty","panels":[]}`
		}
		if mode == "migration-offline" || mode == "migration-dry-run" {
			importRequested = false
		}
		arguments := map[string]any{
			"grafana_json": grafanaJSON, "source_namespace": "grafana:recovery", "import": importRequested,
		}
		if mode == "migration-with-rule" {
			arguments["rules"] = []string{"recovery-rule.yaml"}
		}
		_, err = service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
			Params: mcp.CallToolParams{Arguments: arguments},
		})
	case "validation":
		_, err = service.handleValidateQueries(context.Background(), mcp.CallToolRequest{
			Params: mcp.CallToolParams{Arguments: map[string]any{
				"migration_id": os.Getenv("PROMCAST_RECOVERY_MIGRATION_ID"),
			}},
		})
	default:
		t.Fatalf("unsupported recovery child mode %q", mode)
	}
	require.NoError(t, err)
	t.Fatalf("recovery child completed without reaching barrier %q", barrier)
}

func recoveryChildCommand(
	t *testing.T,
	root, output, target, mode, barrier, migrationID string,
) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPProcessRecovery$")
	command.Env = append(os.Environ(),
		"PROMCAST_RECOVERY_CHILD=1",
		"PROMCAST_RECOVERY_ROOT="+root,
		"PROMCAST_RECOVERY_OUTPUT="+output,
		"PROMCAST_RECOVERY_TARGET="+target,
		"PROMCAST_RECOVERY_MODE="+mode,
		"PROMCAST_RECOVERY_BARRIER="+barrier,
		"PROMCAST_RECOVERY_MIGRATION_ID="+migrationID,
	)
	return command
}

func requireRecoveryChildExit(t *testing.T, err error, output []byte) {
	t.Helper()
	var exitError *exec.ExitError
	require.ErrorAs(t, err, &exitError, "child output:\n%s", output)
	assert.Equal(t, recoveryChildExitCode, exitError.ExitCode(), "child output:\n%s", output)
}

func newRecoveryTarget(t *testing.T, onPOST func()) *httptest.Server {
	t.Helper()
	return newRecoveryTargetWithPOST(t, func(writer http.ResponseWriter) {
		onPOST()
		writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"id": "recovered-id"}})
	})
}

func newRecoveryTargetWithPOST(t *testing.T, post func(http.ResponseWriter)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"compositeQuery": map[string]any{
				"A": map[string]any{"valid": true},
			}}})
		case "/api/v5/query_range":
			writeCheckpointJSONResponse(t, writer, map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A", "series": []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}},
				}}}},
			})
		case "/api/v1/dashboards":
			if request.Method == http.MethodGet {
				writeCheckpointJSONResponse(t, writer, map[string]any{"data": []any{}})
				return
			}
			post(writer)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
}

func assertRecoveredResult(t *testing.T, output string) {
	t.Helper()
	entries, err := os.ReadDir(output)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.True(t, entries[0].IsDir())
	directory := filepath.Join(output, entries[0].Name())
	data, err := os.ReadFile(filepath.Join(directory, "migration-result.json"))
	require.NoError(t, err)
	state, err := decodeManifest(data)
	require.NoError(t, err)
	assert.Equal(t, resultGeneration, state.Generation)
	assert.FileExists(t, filepath.Join(directory, resultGeneration, state.Report))
	assert.NoFileExists(t, filepath.Join(directory, resultGeneration, "migration-result.json"))
}

func assertRecoveredInitialResult(
	t *testing.T,
	output string,
	expectedAction string,
	importRequested bool,
) {
	t.Helper()
	entries, err := os.ReadDir(output)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	directory := filepath.Join(output, entries[0].Name())
	data, err := os.ReadFile(filepath.Join(directory, "migration.json"))
	require.NoError(t, err)
	state, err := decodeManifest(data)
	require.NoError(t, err)
	assert.Equal(t, resultGeneration, state.Generation)
	assert.NoFileExists(t, filepath.Join(directory, "migration-result.json"))
	reportData, err := os.ReadFile(filepath.Join(directory, resultGeneration, state.Report))
	require.NoError(t, err)
	evidence, err := decodeDashboardReport(reportData)
	require.NoError(t, err)
	assert.Equal(t, importRequested, evidence.Run.Flags["importRequested"])
	assert.Equal(t, false, evidence.Run.Flags["importAttempted"])
	assert.Equal(t, expectedAction, evidence.Run.Flags["targetAction"])
}

func assertPrivateStagingCount(t *testing.T, parent string, expected int) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), privateMCPStagingPrefix) {
			count++
		}
	}
	assert.Equal(t, expected, count)
}

func createRecoveryMigration(t *testing.T, root, output string) string {
	t.Helper()
	service, err := New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)
	result, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"grafana_json": `{"uid":"validation-recovery","title":"Validation recovery","panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]}`,
		}},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	data, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var response migrateResponse
	require.NoError(t, json.Unmarshal(data, &response))
	return response.MigrationID
}

func TestMCPRecoveryChildCommandIsBounded(t *testing.T) {
	// Keep the subprocess timeout construction covered without running a child.
	command := recoveryChildCommand(t, t.TempDir(), filepath.Join(t.TempDir(), "out"), "http://127.0.0.1", "migration", "barrier", "")
	assert.NotNil(t, command)
	assert.Contains(t, fmt.Sprint(command.Env), "PROMCAST_RECOVERY_CHILD=1")
}
