package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testMCPHTTPToken = "0123456789abcdef0123456789abcdef"

func TestMCPHTTPRequiresLoopbackHostAndSameOrigin(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	handler := newMCPHTTPHandler(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}), 8000, testMCPHTTPToken)

	tests := []struct {
		name   string
		host   string
		origin string
		want   int
	}{
		{name: "IPv4 loopback without browser origin", host: "127.0.0.1:8000", want: http.StatusNoContent},
		{name: "localhost same origin", host: "localhost:8000", origin: "http://localhost:8000", want: http.StatusNoContent},
		{name: "IPv6 loopback same origin", host: "[::1]:8000", origin: "http://[::1]:8000", want: http.StatusNoContent},
		{name: "external host", host: "attacker.example:8000", want: http.StatusForbidden},
		{name: "loopback prefix attack", host: "127.0.0.1.attacker.example:8000", want: http.StatusForbidden},
		{name: "wrong loopback port", host: "127.0.0.1:8001", want: http.StatusForbidden},
		{name: "external origin", host: "127.0.0.1:8000", origin: "https://attacker.example", want: http.StatusForbidden},
		{name: "cross port origin", host: "localhost:8000", origin: "http://localhost:3000", want: http.StatusForbidden},
		{name: "cross scheme origin", host: "localhost:8000", origin: "https://localhost:8000", want: http.StatusForbidden},
		{name: "opaque origin", host: "localhost:8000", origin: "null", want: http.StatusForbidden},
	}

	accepted := int32(0)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8000/mcp", strings.NewReader(`{}`))
			request.Host = test.host
			request.Header.Set("Authorization", "Bearer "+testMCPHTTPToken)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assert.Equal(t, test.want, response.Code)
			if test.want == http.StatusNoContent {
				accepted++
			}
		})
	}
	assert.Equal(t, accepted, calls.Load())
}

func TestMCPHTTPBoundsKnownAndStreamingRequestBodies(t *testing.T) {
	t.Parallel()

	limits := mcpHTTPLimits{MaxBodyBytes: 4, MaxConcurrent: 1}
	var calls atomic.Int32
	var readError error
	handler := newMCPHTTPHandlerWithLimits(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		_, err := io.ReadAll(request.Body)
		readError = err
		writer.WriteHeader(http.StatusNoContent)
	}), 8000, testMCPHTTPToken, limits)

	known := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8000/mcp", strings.NewReader("12345"))
	known.Host = "127.0.0.1:8000"
	known.Header.Set("Authorization", "Bearer "+testMCPHTTPToken)
	known.ContentLength = 5
	knownResponse := httptest.NewRecorder()
	handler.ServeHTTP(knownResponse, known)
	assert.Equal(t, http.StatusRequestEntityTooLarge, knownResponse.Code)
	assert.Zero(t, calls.Load(), "known oversized bodies must be rejected before the MCP transport")

	streaming := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8000/mcp", strings.NewReader("12345"))
	streaming.Host = "127.0.0.1:8000"
	streaming.Header.Set("Authorization", "Bearer "+testMCPHTTPToken)
	streaming.ContentLength = -1
	streamingResponse := httptest.NewRecorder()
	handler.ServeHTTP(streamingResponse, streaming)
	assert.Equal(t, http.StatusNoContent, streamingResponse.Code)
	assert.Equal(t, int32(1), calls.Load())
	var tooLarge *http.MaxBytesError
	require.Error(t, readError)
	assert.True(t, errors.As(readError, &tooLarge), "chunked bodies must remain bounded while read by the transport")
}

func TestMCPHTTPRejectsRequestsAboveConcurrencyLimit(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan struct{})
	var calls atomic.Int32
	handler := newMCPHTTPHandlerWithLimits(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		writer.WriteHeader(http.StatusNoContent)
	}), 8000, testMCPHTTPToken, mcpHTTPLimits{MaxBodyBytes: 1024, MaxConcurrent: 1})

	go func() {
		defer close(firstDone)
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8000/mcp", strings.NewReader(`{}`))
		request.Host = "127.0.0.1:8000"
		request.Header.Set("Authorization", "Bearer "+testMCPHTTPToken)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first MCP request did not enter the transport")
	}
	second := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8000/mcp", strings.NewReader(`{}`))
	second.Host = "127.0.0.1:8000"
	second.Header.Set("Authorization", "Bearer "+testMCPHTTPToken)
	secondResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondResponse, second)
	assert.Equal(t, http.StatusServiceUnavailable, secondResponse.Code)
	assert.Equal(t, "1", secondResponse.Header().Get("Retry-After"))
	assert.Equal(t, int32(1), calls.Load(), "overload must not queue unbounded work in the MCP transport")

	close(release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first MCP request did not complete")
	}
}

func TestMCPHTTPBodyBudgetIsSharedAndWeightedAcrossHandlers(t *testing.T) {
	t.Parallel()

	budget, err := newWeightedBodyBudget(6)
	require.NoError(t, err)
	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan struct{})
	firstHandler := newMCPHTTPHandlerWithLimits(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		writer.WriteHeader(http.StatusNoContent)
	}), 8000, testMCPHTTPToken, mcpHTTPLimits{
		MaxBodyBytes: 6, MaxConcurrent: 2, BodyBudget: budget,
	})
	var secondCalls atomic.Int32
	secondHandler := newMCPHTTPHandlerWithLimits(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}), 8000, testMCPHTTPToken, mcpHTTPLimits{
		MaxBodyBytes: 6, MaxConcurrent: 2, BodyBudget: budget,
	})

	go func() {
		defer close(firstDone)
		request := authorizedMCPRequest(strings.NewReader("1234"))
		firstHandler.ServeHTTP(httptest.NewRecorder(), request)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not acquire the shared body budget")
	}

	overBudget := authorizedMCPRequest(strings.NewReader("123"))
	overBudgetResponse := httptest.NewRecorder()
	secondHandler.ServeHTTP(overBudgetResponse, overBudget)
	assert.Equal(t, http.StatusServiceUnavailable, overBudgetResponse.Code)
	assert.Equal(t, "1", overBudgetResponse.Header().Get("Retry-After"))
	assert.Equal(t, "close", overBudgetResponse.Header().Get("Connection"))
	assert.Zero(t, secondCalls.Load(), "aggregate request weight must be rejected before the transport")

	fitsBudget := authorizedMCPRequest(strings.NewReader("12"))
	fitsResponse := httptest.NewRecorder()
	secondHandler.ServeHTTP(fitsResponse, fitsBudget)
	assert.Equal(t, http.StatusNoContent, fitsResponse.Code)
	assert.Equal(t, int32(1), secondCalls.Load())

	close(release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first request did not release the shared body budget")
	}

	afterRelease := authorizedMCPRequest(strings.NewReader("123"))
	afterReleaseResponse := httptest.NewRecorder()
	secondHandler.ServeHTTP(afterReleaseResponse, afterRelease)
	assert.Equal(t, http.StatusNoContent, afterReleaseResponse.Code)
	assert.Equal(t, int32(2), secondCalls.Load())
}

func TestMCPHTTPUnknownLengthReservesFullPerRequestAllowance(t *testing.T) {
	t.Parallel()

	budget, err := newWeightedBodyBudget(6)
	require.NoError(t, err)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	handler := newMCPHTTPHandlerWithLimits(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		writer.WriteHeader(http.StatusNoContent)
	}), 8000, testMCPHTTPToken, mcpHTTPLimits{
		MaxBodyBytes: 6, MaxConcurrent: 2, BodyBudget: budget,
	})

	go func() {
		defer close(done)
		request := authorizedMCPRequest(strings.NewReader("1"))
		request.ContentLength = -1
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("unknown-length request did not enter the transport")
	}

	known := authorizedMCPRequest(strings.NewReader("1"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, known)
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unknown-length request did not finish")
	}
}

func authorizedMCPRequest(body io.Reader) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8000/mcp", body)
	request.Host = "127.0.0.1:8000"
	request.Header.Set("Authorization", "Bearer "+testMCPHTTPToken)
	return request
}

func TestMCPHTTPRequiresBearerToken(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	var downstreamAuthorization string
	handler := newMCPHTTPHandler(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		downstreamAuthorization = request.Header.Get("Authorization")
		writer.WriteHeader(http.StatusNoContent)
	}), 8000, testMCPHTTPToken)

	tests := []struct {
		name          string
		authorization []string
		want          int
	}{
		{name: "missing token", want: http.StatusUnauthorized},
		{name: "wrong token", authorization: []string{"Bearer 0123456789abcdef0123456789abcdeg"}, want: http.StatusUnauthorized},
		{name: "wrong scheme", authorization: []string{"Basic " + testMCPHTTPToken}, want: http.StatusUnauthorized},
		{name: "multiple headers", authorization: []string{"Bearer " + testMCPHTTPToken, "Bearer " + testMCPHTTPToken}, want: http.StatusUnauthorized},
		{name: "valid token", authorization: []string{"Bearer " + testMCPHTTPToken}, want: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8000/mcp", strings.NewReader(`{}`))
			request.Host = "127.0.0.1:8000"
			for _, value := range test.authorization {
				request.Header.Add("Authorization", value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assert.Equal(t, test.want, response.Code)
			if test.want == http.StatusUnauthorized {
				assert.Equal(t, `Bearer realm="promcast-mcp"`, response.Header().Get("WWW-Authenticate"))
				assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			}
		})
	}
	assert.Equal(t, int32(1), calls.Load())
	assert.Empty(t, downstreamAuthorization, "the local bearer token must not be exposed to the MCP transport")
}

func TestMCPHTTPHealthEndpointsDoNotRequireBearerToken(t *testing.T) {
	t.Parallel()

	handler := newMCPHTTPHandler(http.NotFoundHandler(), 8000, testMCPHTTPToken)
	for _, path := range []string{"/livez", "/readyz"} {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8000"+path, nil)
		request.Host = "127.0.0.1:8000"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assert.Equal(t, http.StatusOK, response.Code)
	}
}

func TestMCPHTTPServerBoundsHeaderAndBodyReadTime(t *testing.T) {
	t.Parallel()

	httpServer := newMCPHTTPServer("127.0.0.1:8000")
	assert.Equal(t, mcpHTTPReadHeaderTimeout, httpServer.ReadHeaderTimeout)
	assert.Equal(t, mcpHTTPReadTimeout, httpServer.ReadTimeout)
	assert.Equal(t, mcpHTTPIdleTimeout, httpServer.IdleTimeout)
	assert.Equal(t, maxMCPHTTPHeaderBytes, httpServer.MaxHeaderBytes)
	assert.Greater(t, maxMCPHTTPRequestBytes, int64(6*(64<<20)), "the wire cap must include the worst-case JSON escaping of a valid inline artifact")
	assert.GreaterOrEqual(
		t,
		processMCPHTTPBodyBudget.capacity,
		maxMCPHTTPRequestBytes,
		"the process-wide budget must admit one maximum valid request",
	)
}

func TestMCPHTTPDisablesLongLivedGETStreams(t *testing.T) {
	t.Parallel()

	httpServer := newMCPHTTPServer("127.0.0.1:8000")
	transport := newMCPStreamableHTTPServer(mcpserver.NewMCPServer("test", "1.0.0"), httpServer)
	handler := newMCPHTTPHandler(transport, 8000, testMCPHTTPToken)

	for range maxConcurrentMCPHTTPRequests + 1 {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8000/mcp", nil)
		request.Host = "127.0.0.1:8000"
		request.Header.Set("Accept", "text/event-stream")
		request.Header.Set("Authorization", "Bearer "+testMCPHTTPToken)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assert.Equal(t, http.StatusMethodNotAllowed, response.Code)
		assert.NotEqual(t, http.StatusServiceUnavailable, response.Code, "GET streams must not retain concurrency slots")
	}
}

func TestResolveMCPHTTPTokenUsesConfiguredOrGeneratedSecret(t *testing.T) {
	t.Setenv("SIGNOZ_MCP_HTTP_TOKEN", testMCPHTTPToken)
	token, generated, err := resolveMCPHTTPToken("")
	require.NoError(t, err)
	assert.Equal(t, testMCPHTTPToken, token)
	assert.False(t, generated)

	t.Setenv("SIGNOZ_MCP_HTTP_TOKEN", "")
	token, generated, err = resolveMCPHTTPToken("")
	require.NoError(t, err)
	assert.True(t, generated)
	assert.GreaterOrEqual(t, len(token), minimumMCPHTTPTokenLength)
	assert.NoError(t, validateMCPHTTPToken(token))
}
