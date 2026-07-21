package signoz

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mansiverma897993/signoz/internal/httpdetail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientMetricMetadata(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "test-key", request.Header.Get("SIGNOZ-API-KEY"))
		assert.Equal(t, "node_cpu_seconds_total", request.URL.Query().Get("metricName"))
		writeTestJSON(t, writer, http.StatusOK, map[string]any{
			"data": map[string]any{"type": " SUM ", "temporality": " CUMULATIVE ", "isMonotonic": true},
		})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	metadata, err := client.MetricMetadata(context.Background(), "node_cpu_seconds_total")
	require.NoError(t, err)
	assert.Equal(t, "sum", metadata.Type)
	assert.Equal(t, "cumulative", metadata.Temporality)
	assert.True(t, metadata.IsMonotonic)
}

func TestClientPreservesSigNozBasePath(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/signoz/api/v2/metrics/metadata", request.URL.Path)
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"data":{"type":"gauge","temporality":"unspecified"}}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL+"/signoz/", "test-key", server.Client())
	require.NoError(t, err)
	_, err = client.MetricMetadata(context.Background(), "up")
	require.NoError(t, err)
}

func TestClientDoesNotEchoMalformedURLSecrets(t *testing.T) {
	t.Parallel()

	_, err := NewClient("https://example.com/\x00?token=do-not-print", "test-key", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "do-not-print")
}

func TestClientRejectsAPIKeyControlCharactersWithoutEchoingIt(t *testing.T) {
	t.Parallel()

	_, err := NewClient("https://example.com", "prefix\r\ndo-not-print", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "control characters")
	assert.NotContains(t, err.Error(), "do-not-print")
}

func TestClientRequiresExplicitOptInForCredentialedRemoteHTTP(t *testing.T) {
	t.Parallel()

	_, err := NewClient("http://10.0.0.2:3301", "secret", nil)
	require.ErrorContains(t, err, "plaintext HTTP")
	client, err := NewClientWithOptions(
		"http://10.0.0.2:3301",
		"secret",
		nil,
		ClientOptions{AllowInsecureHTTP: true},
	)
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestClientDoesNotFollowRedirectWithAPIKey(t *testing.T) {
	t.Parallel()

	var redirected atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Store(true)
	}))
	t.Cleanup(destination.Close)
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", destination.URL)
		writer.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(source.Close)

	client, err := NewClient(source.URL, "target-secret", source.Client())
	require.NoError(t, err)
	_, err = client.MetricMetadata(context.Background(), "up")
	require.Error(t, err)
	assert.False(t, redirected.Load())
}

func TestClientMetricMetadataRejectsMalformedSuccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response map[string]any
		expected string
	}{
		{name: "missing data", response: map[string]any{}, expected: "missing data"},
		{name: "null data", response: map[string]any{"data": nil}, expected: "missing data"},
		{name: "missing type", response: map[string]any{"data": map[string]any{}}, expected: "missing data.type"},
		{name: "blank type", response: map[string]any{"data": map[string]any{"type": "  "}}, expected: "missing data.type"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writeTestJSON(t, writer, http.StatusOK, test.response)
			}))
			t.Cleanup(server.Close)

			client, err := NewClient(server.URL, "test-key", server.Client())
			require.NoError(t, err)
			_, err = client.MetricMetadata(context.Background(), "up")
			require.ErrorContains(t, err, test.expected)
			var apiError *APIError
			assert.False(t, errors.As(err, &apiError))
		})
	}
}

func TestClientMetricAttributes(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "node_cpu_seconds_total", request.URL.Query().Get("metricName"))
		assert.NotEmpty(t, request.URL.Query().Get("start"))
		assert.NotEmpty(t, request.URL.Query().Get("end"))
		writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": map[string]any{
			"attributes": []any{map[string]any{"key": "cpu", "values": []string{"0", "1"}, "valueCount": 2}},
		}})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	attributes, err := client.MetricAttributes(context.Background(), "node_cpu_seconds_total", time.Unix(1, 0), time.Unix(2, 0))
	require.NoError(t, err)
	require.Len(t, attributes, 1)
	assert.Equal(t, "cpu", attributes[0].Key)
}

func TestClientMetricAttributesRequiresExplicitArray(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response map[string]any
		expected string
		valid    bool
	}{
		{name: "missing data", response: map[string]any{}, expected: "missing data"},
		{name: "null data", response: map[string]any{"data": nil}, expected: "missing data"},
		{name: "missing attributes", response: map[string]any{"data": map[string]any{}}, expected: "missing data.attributes"},
		{name: "null attributes", response: map[string]any{"data": map[string]any{"attributes": nil}}, expected: "missing data.attributes"},
		{name: "explicit empty attributes", response: map[string]any{"data": map[string]any{"attributes": []any{}}}, valid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writeTestJSON(t, writer, http.StatusOK, test.response)
			}))
			t.Cleanup(server.Close)

			client, err := NewClient(server.URL, "test-key", server.Client())
			require.NoError(t, err)
			attributes, err := client.MetricAttributes(context.Background(), "up", time.Unix(1, 0), time.Unix(2, 0))
			if test.valid {
				require.NoError(t, err)
				assert.NotNil(t, attributes)
				assert.Empty(t, attributes)
				return
			}
			require.ErrorContains(t, err, test.expected)
			var apiError *APIError
			assert.False(t, errors.As(err, &apiError))
		})
	}
}

func TestClientUpsertDashboardCreatesAndUpdates(t *testing.T) {
	t.Parallel()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		switch request.Method {
		case http.MethodGet:
			if requests == 1 {
				writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{}})
				return
			}
			writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{
				map[string]any{"id": "dashboard-id", "data": map[string]any{"uuid": "stable-uuid"}},
			}})
		case http.MethodPost:
			writeTestJSON(t, writer, http.StatusCreated, map[string]any{"data": map[string]any{"id": "dashboard-id"}})
		case http.MethodPut:
			assert.Equal(t, "/api/v1/dashboards/dashboard-id", request.URL.Path)
			writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": map[string]any{"id": "dashboard-id"}})
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	dashboard := DashboardV5{Title: "Host", UUID: "stable-uuid"}
	created, err := client.UpsertDashboard(context.Background(), dashboard)
	require.NoError(t, err)
	assert.Equal(t, DashboardWriteResult{ID: "dashboard-id", Action: "created"}, created)
	updated, err := client.UpsertDashboard(context.Background(), dashboard)
	require.NoError(t, err)
	assert.Equal(t, DashboardWriteResult{ID: "dashboard-id", Action: "updated"}, updated)
}

func TestClientReturnsStructuredAPIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, writer, http.StatusBadRequest, map[string]any{
			"status": "error",
			"error":  map[string]any{"code": "invalid_input", "message": "bad dashboard"},
		})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	_, err = client.ListDashboards(context.Background())
	var apiError *APIError
	require.ErrorAs(t, err, &apiError)
	assert.Equal(t, http.StatusBadRequest, apiError.StatusCode)
	assert.Equal(t, "invalid_input", apiError.Code)
}

func TestClientBoundsAndSanitizesAPIErrorDetail(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		writeError func(*testing.T, http.ResponseWriter)
	}{
		{
			name: "structured error",
			writeError: func(t *testing.T, writer http.ResponseWriter) {
				writeTestJSON(t, writer, http.StatusBadRequest, map[string]any{"error": map[string]any{
					"code":    "invalid\x1b\ninput",
					"message": "bad\x1b[31m\nrequest " + strings.Repeat("x", httpdetail.MaxBytes*2),
				}})
			},
		},
		{
			name: "unstructured error",
			writeError: func(t *testing.T, writer http.ResponseWriter) {
				writer.WriteHeader(http.StatusBadRequest)
				_, err := writer.Write([]byte("bad\x1b[31m\nrequest " + strings.Repeat("x", httpdetail.MaxBytes*2)))
				require.NoError(t, err)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				test.writeError(t, writer)
			}))
			t.Cleanup(server.Close)

			client, err := NewClient(server.URL, "test-key", server.Client())
			require.NoError(t, err)
			_, err = client.ListDashboards(context.Background())
			var apiError *APIError
			require.ErrorAs(t, err, &apiError)
			assert.LessOrEqual(t, len(apiError.Message), httpdetail.MaxBytes)
			assert.True(t, strings.HasSuffix(apiError.Message, httpdetail.TruncationMarker))
			assert.NotContains(t, apiError.Message, "\x1b")
			assert.NotContains(t, apiError.Message, "\n")
			assert.Contains(t, apiError.Message, "bad [31m request")
			assert.NotContains(t, apiError.Code, "\x1b")
			assert.NotContains(t, apiError.Code, "\n")
		})
	}
}

func TestNewClientRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	_, err := NewClient("localhost:8080", "key", nil)
	require.ErrorContains(t, err, "absolute HTTP")
	_, err = NewClient("http://localhost:8080", "", nil)
	require.ErrorContains(t, err, "API key is required")
	_, err = NewClient("http://user:secret@localhost:8080", "key", nil)
	require.ErrorContains(t, err, "user information")
	_, err = NewClient("http://localhost:8080?token=secret", "key", nil)
	require.ErrorContains(t, err, "query or fragment")
}

func TestClientRetriesSafeRequests(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			writer.Header().Set("Retry-After", "0")
			writeTestJSON(t, writer, http.StatusTooManyRequests, map[string]any{"error": map[string]any{"message": "slow down"}})
			return
		}
		writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{}})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	_, err = client.ListDashboards(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, attempts)
}

func TestClientDoesNotRetryDashboardCreate(t *testing.T) {
	t.Parallel()

	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{}})
			return
		}
		posts++
		writeTestJSON(t, writer, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": "unavailable"}})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	_, err = client.UpsertDashboard(context.Background(), DashboardV5{UUID: "stable", Title: "Test"})
	require.Error(t, err)
	assert.Equal(t, 1, posts)
}

func writeTestJSON(t *testing.T, writer http.ResponseWriter, status int, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	require.NoError(t, json.NewEncoder(writer).Encode(value))
}
