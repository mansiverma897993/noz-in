package prometheus

import (
	"context"
	"encoding/json"
	"math"
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

func TestNewClientRejectsSecretsAndAmbiguousURLComponents(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		baseURL string
		want    string
	}{
		{name: "userinfo", baseURL: "https://user:password@example.com", want: "must not contain user information"},
		{name: "query", baseURL: "https://example.com?token=secret", want: "must not contain a query or fragment"},
		{name: "fragment", baseURL: "https://example.com/#secret", want: "must not contain a query or fragment"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewClient(testCase.baseURL, "", nil)
			require.ErrorContains(t, err, testCase.want)
		})
	}
}

func TestNewClientDoesNotEchoMalformedURLSecrets(t *testing.T) {
	t.Parallel()

	_, err := NewClient("https://example.com/\x00?token=do-not-print", "", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "do-not-print")
}

func TestNewClientRejectsBearerTokenControlCharactersWithoutEchoingIt(t *testing.T) {
	t.Parallel()

	_, err := NewClient("https://example.com", "prefix\r\ndo-not-print", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "control characters")
	assert.NotContains(t, err.Error(), "do-not-print")
}

func TestClientRequiresExplicitOptInForCredentialedRemoteHTTP(t *testing.T) {
	t.Parallel()

	_, err := NewClient("http://10.0.0.2:9090", "secret", nil)
	require.ErrorContains(t, err, "plaintext HTTP")
	client, err := NewClientWithOptions(
		"http://10.0.0.2:9090",
		"secret",
		nil,
		ClientOptions{AllowInsecureHTTP: true},
	)
	require.NoError(t, err)
	require.NotNil(t, client)

	_, err = NewClient("http://10.0.0.2:9090", "", nil)
	require.NoError(t, err)
}

func TestClientQueryRangePreservesPrometheusBasePath(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/prometheus/api/v1/query_range", request.URL.Path)
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL+"/prometheus/", "", server.Client())
	require.NoError(t, err)
	_, err = client.QueryRange(context.Background(), "up", time.Unix(0, 0), time.Unix(120, 0), time.Minute)
	require.NoError(t, err)
}

func TestClientDoesNotFollowRedirectWithBearerToken(t *testing.T) {
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

	client, err := NewClient(source.URL, "source-secret", source.Client())
	require.NoError(t, err)
	_, err = client.QueryRange(context.Background(), "up", time.Unix(0, 0), time.Unix(120, 0), time.Minute)
	require.ErrorContains(t, err, "HTTP 302")
	assert.False(t, redirected.Load())
}

func TestClientQueryRangeDecodesMatrix(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/api/v1/query_range", request.URL.Path)
		assert.Equal(t, "up", request.URL.Query().Get("query"))
		assert.Equal(t, "60", request.URL.Query().Get("step"))
		assert.Equal(t, "Bearer source-token", request.Header.Get("Authorization"))
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up","job":"node"},"values":[[10.25,"1"],[70.25,"NaN"]]}]}}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "source-token", server.Client())
	require.NoError(t, err)
	series, err := client.QueryRange(context.Background(), "up", time.Unix(0, 0), time.Unix(120, 0), time.Minute)
	require.NoError(t, err)
	require.Len(t, series, 1)
	assert.Equal(t, map[string]string{"__name__": "up", "job": "node"}, series[0].Labels)
	require.Len(t, series[0].Values, 2)
	assert.Equal(t, int64(10250), series[0].Values[0].Timestamp)
	assert.Equal(t, 1.0, series[0].Values[0].Value)
	assert.True(t, math.IsNaN(series[0].Values[1].Value))
}

func TestClientQueryRangeRejectsPrometheusError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"status":"error","errorType":"bad_data","error":"invalid expression"}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "", server.Client())
	require.NoError(t, err)
	_, err = client.QueryRange(context.Background(), "up", time.Unix(0, 0), time.Unix(120, 0), time.Minute)
	require.ErrorContains(t, err, "invalid expression")
}

func TestClientBoundsAndSanitizesPrometheusErrorDetail(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		writeError func(*testing.T, http.ResponseWriter)
	}{
		{
			name: "structured error",
			writeError: func(t *testing.T, writer http.ResponseWriter) {
				writer.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{
					"status":    "error",
					"errorType": "bad\x1b\ninput",
					"error":     "bad\x1b[31m\nquery " + strings.Repeat("x", httpdetail.MaxBytes*2),
				}))
			},
		},
		{
			name: "unstructured HTTP error",
			writeError: func(t *testing.T, writer http.ResponseWriter) {
				writer.WriteHeader(http.StatusBadRequest)
				_, err := writer.Write([]byte("bad\x1b[31m\nquery " + strings.Repeat("x", httpdetail.MaxBytes*2)))
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

			client, err := NewClient(server.URL, "", server.Client())
			require.NoError(t, err)
			_, err = client.QueryRange(context.Background(), "up", time.Unix(0, 0), time.Unix(120, 0), time.Minute)
			require.Error(t, err)
			detail := err.Error()
			assert.LessOrEqual(t, len(detail), httpdetail.MaxBytes+128)
			assert.True(t, strings.HasSuffix(detail, httpdetail.TruncationMarker))
			assert.NotContains(t, detail, "\x1b")
			assert.NotContains(t, detail, "\n")
			assert.Contains(t, detail, "bad [31m query")
		})
	}
}
