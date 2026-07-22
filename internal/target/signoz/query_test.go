package signoz

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientQueryRangeSummarizesScalarAndSeriesResults(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/api/v5/query_range", request.URL.Path)
		assert.Equal(t, http.MethodPost, request.Method)
		writeTestJSON(t, writer, http.StatusOK, map[string]any{
			"status": "success",
			"data": map[string]any{"data": map[string]any{"results": []any{
				map[string]any{"queryName": "A", "data": []any{[]any{42}}},
				map[string]any{"queryName": "B", "aggregations": []any{map[string]any{"series": []any{
					map[string]any{
						"labels": []any{map[string]any{"key": map[string]any{"name": "service.name"}, "value": "node"}},
						"values": []any{map[string]any{"timestamp": 1, "value": 2}, map[string]any{"timestamp": 2, "value": 3}},
					},
				}}}},
			}}},
		})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	results, err := client.QueryRange(context.Background(), QueryRangeRequest{})
	require.NoError(t, err)
	assert.Equal(t, QueryExecutionResult{Rows: 1}, results["A"])
	assert.True(t, results["A"].HasData())
	assert.Equal(t, QueryExecutionResult{Series: 1, Points: 2, Sample: []MetricSeries{{
		Labels: map[string]string{"service.name": "node"},
		Values: []MetricPoint{{Timestamp: 1, Value: 2}, {Timestamp: 2, Value: 3}},
	}}}, results["B"])
}

func TestClientQueryRangeReturnsSeries(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, writer, http.StatusOK, map[string]any{
			"status": "success",
			"data": map[string]any{"data": map[string]any{"results": []any{
				map[string]any{"queryName": "A", "aggregations": []any{map[string]any{"series": []any{
					map[string]any{
						"labels": []any{map[string]any{"key": map[string]any{"name": "service.name"}, "value": "node-exporter"}},
						"values": []any{map[string]any{"timestamp": 1000, "value": 1.5}},
					},
				}}}},
			}}},
		})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	results, err := client.QueryRangeSeries(context.Background(), QueryRangeRequest{})
	require.NoError(t, err)
	require.Len(t, results["A"], 1)
	assert.Equal(t, map[string]string{"service.name": "node-exporter"}, results["A"][0].Labels)
	assert.Equal(t, []MetricPoint{{Timestamp: 1000, Value: 1.5}}, results["A"][0].Values)
}

func TestClientQueryRangeDecodesStringNonFiniteSamples(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, err := writer.Write([]byte(`{"status":"success","data":{"data":{"results":[{"queryName":"A","aggregations":[{"series":[{"values":[{"timestamp":1000,"value":"NaN","partial":true},{"timestamp":2000,"value":"+Inf"},{"timestamp":3000,"value":"-Inf"}]}]}]}]}}}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	results, err := client.QueryRangeSeries(context.Background(), QueryRangeRequest{})
	require.NoError(t, err)
	require.Len(t, results["A"], 1)
	values := results["A"][0].Values
	require.Len(t, values, 3)
	assert.True(t, math.IsNaN(values[0].Value))
	assert.True(t, values[0].Partial)
	assert.False(t, values[1].Partial)
	assert.True(t, math.IsInf(values[1].Value, 1))
	assert.True(t, math.IsInf(values[2].Value, -1))
}

func TestClientQueryRangeRejectsErrorEnvelope(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, writer, http.StatusOK, map[string]any{"status": "error", "error": "bad query"})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	_, err = client.QueryRange(context.Background(), QueryRangeRequest{})
	require.ErrorContains(t, err, "bad query")
}

func TestClientQueryRangeRejectsMalformedSuccessEnvelope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "missing status", body: `{}`},
		{name: "missing outer data", body: `{"status":"success"}`},
		{name: "missing inner data", body: `{"status":"success","data":{}}`},
		{name: "missing results", body: `{"status":"success","data":{"data":{}}}`},
		{name: "null results", body: `{"status":"success","data":{"data":{"results":null}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, err := writer.Write([]byte(test.body))
				require.NoError(t, err)
			}))
			t.Cleanup(server.Close)

			client, err := NewClient(server.URL, "test-key", server.Client())
			require.NoError(t, err)
			_, err = client.QueryRangeSeries(context.Background(), QueryRangeRequest{})
			require.Error(t, err)
		})
	}
}

func TestClientQueryRangeAcceptsPresentEmptyResults(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, writer, http.StatusOK, map[string]any{
			"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{}}},
		})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	results, err := client.QueryRangeSeries(context.Background(), QueryRangeRequest{})
	require.NoError(t, err)
	assert.Empty(t, results)
}
