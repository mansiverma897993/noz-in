package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPOutputQuotaFlagsHonorEnvironment(t *testing.T) {
	t.Setenv("SIGNOZ_MCP_MAX_OUTPUT_ENTRIES", "321")
	t.Setenv("SIGNOZ_MCP_MAX_OUTPUT_BYTES", "654321")
	command := newMCPCommand()

	entries, err := command.Flags().GetInt("max-output-entries")
	require.NoError(t, err)
	bytes, err := command.Flags().GetInt64("max-output-bytes")
	require.NoError(t, err)
	assert.Equal(t, 321, entries)
	assert.Equal(t, int64(654321), bytes)
}

func TestMCPOutputQuotaEnvironmentFailsClosed(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		value    string
	}{
		{name: "entries malformed", variable: "SIGNOZ_MCP_MAX_OUTPUT_ENTRIES", value: "invalid"},
		{name: "entries zero", variable: "SIGNOZ_MCP_MAX_OUTPUT_ENTRIES", value: "0"},
		{name: "entries negative", variable: "SIGNOZ_MCP_MAX_OUTPUT_ENTRIES", value: "-1"},
		{name: "entries overflow", variable: "SIGNOZ_MCP_MAX_OUTPUT_ENTRIES", value: "999999999999999999999999999999999999"},
		{name: "bytes malformed", variable: "SIGNOZ_MCP_MAX_OUTPUT_BYTES", value: "invalid"},
		{name: "bytes zero", variable: "SIGNOZ_MCP_MAX_OUTPUT_BYTES", value: "0"},
		{name: "bytes negative", variable: "SIGNOZ_MCP_MAX_OUTPUT_BYTES", value: "-1"},
		{name: "bytes overflow", variable: "SIGNOZ_MCP_MAX_OUTPUT_BYTES", value: "999999999999999999999999999999999999"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SIGNOZ_MCP_MAX_OUTPUT_ENTRIES", "")
			t.Setenv("SIGNOZ_MCP_MAX_OUTPUT_BYTES", "")
			t.Setenv(test.variable, test.value)

			_, err := runCommand(t, "mcp")
			assert.Equal(t, 3, commandExitCode(err), err)
			assert.ErrorContains(t, err, test.variable)
		})
	}
}

func TestMCPOutputQuotaAndWorkerFlagsRejectInvalidValues(t *testing.T) {
	tests := []struct {
		flag  string
		value string
	}{
		{flag: "--max-output-entries", value: "0"},
		{flag: "--max-output-entries", value: "-1"},
		{flag: "--max-output-entries", value: "999999999999999999999999999999999999"},
		{flag: "--max-output-bytes", value: "0"},
		{flag: "--max-output-bytes", value: "-1"},
		{flag: "--max-output-bytes", value: "999999999999999999999999999999999999"},
		{flag: "--workers", value: "0"},
		{flag: "--workers", value: "-1"},
		{flag: "--workers", value: "5"},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%s=%s", test.flag, test.value), func(t *testing.T) {
			t.Setenv("SIGNOZ_MCP_MAX_OUTPUT_ENTRIES", "")
			t.Setenv("SIGNOZ_MCP_MAX_OUTPUT_BYTES", "")

			_, err := runCommand(t, "mcp", test.flag, test.value)
			assert.Equal(t, 3, commandExitCode(err), err)
			assert.ErrorContains(t, err, test.flag)
		})
	}
}

func TestPositiveQuotaEnvironmentParsersAcceptUnsetAndPositiveValues(t *testing.T) {
	t.Setenv("SIGNOZ_MCP_TEST_INT", "")
	value, err := positiveEnvironmentIntDefault("SIGNOZ_MCP_TEST_INT", 42)
	require.NoError(t, err)
	assert.Equal(t, 42, value)
	t.Setenv("SIGNOZ_MCP_TEST_INT", "123")
	value, err = positiveEnvironmentIntDefault("SIGNOZ_MCP_TEST_INT", 42)
	require.NoError(t, err)
	assert.Equal(t, 123, value)

	t.Setenv("SIGNOZ_MCP_TEST_INT64", "")
	value64, err := positiveEnvironmentInt64Default("SIGNOZ_MCP_TEST_INT64", 42)
	require.NoError(t, err)
	assert.Equal(t, int64(42), value64)
	t.Setenv("SIGNOZ_MCP_TEST_INT64", "456")
	value64, err = positiveEnvironmentInt64Default("SIGNOZ_MCP_TEST_INT64", 42)
	require.NoError(t, err)
	assert.Equal(t, int64(456), value64)
}
