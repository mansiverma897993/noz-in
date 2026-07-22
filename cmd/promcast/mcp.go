package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mansiverma897993/noz-in/internal/app"
	"github.com/mansiverma897993/noz-in/internal/mcpserver"
	"github.com/mansiverma897993/noz-in/internal/metricmap"
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
)

const (
	// An inline Grafana artifact may be 64 MiB before JSON-RPC string escaping.
	// Six bytes per input byte is the JSON worst case; the extra MiB covers the
	// MCP envelope while keeping the wire request strictly bounded.
	maxMCPHTTPRequestBytes int64 = 6*(64<<20) + (1 << 20)
	// Keep enough process-wide wire budget for one worst-case escaped 64 MiB
	// inline artifact, but do not let the per-request cap multiply by the HTTP
	// concurrency limit into several GiB of simultaneously decoded requests.
	maxMCPHTTPInFlightBodyBytes  = maxMCPHTTPRequestBytes
	maxConcurrentMCPHTTPRequests = 16
	maxMCPHTTPHeaderBytes        = 1 << 20
	mcpHTTPReadHeaderTimeout     = 5 * time.Second
	mcpHTTPReadTimeout           = 30 * time.Second
	mcpHTTPIdleTimeout           = time.Minute
	minimumMCPHTTPTokenLength    = 32
)

type mcpHTTPLimits struct {
	MaxBodyBytes  int64
	MaxConcurrent int
	BodyBudget    *weightedBodyBudget
}

type mcpHTTPGuard struct {
	next         http.Handler
	expectedPort int
	maxBodyBytes int64
	authToken    string
	semaphore    chan struct{}
	bodyBudget   *weightedBodyBudget
}

type weightedBodyBudget struct {
	mutex    sync.Mutex
	capacity int64
	inUse    int64
}

var processMCPHTTPBodyBudget = &weightedBodyBudget{capacity: maxMCPHTTPInFlightBodyBytes}

func newWeightedBodyBudget(capacity int64) (*weightedBodyBudget, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("MCP HTTP body budget capacity must be positive")
	}
	return &weightedBodyBudget{capacity: capacity}, nil
}

func (budget *weightedBodyBudget) tryAcquire(weight int64) (func(), bool) {
	if budget == nil || weight < 0 || weight > budget.capacity {
		return nil, false
	}
	if weight == 0 {
		return func() {}, true
	}
	budget.mutex.Lock()
	defer budget.mutex.Unlock()
	if weight > budget.capacity-budget.inUse {
		return nil, false
	}
	budget.inUse += weight
	var once sync.Once
	return func() {
		once.Do(func() {
			budget.mutex.Lock()
			defer budget.mutex.Unlock()
			budget.inUse -= weight
		})
	}, true
}

type loopbackAuthority struct {
	host string
	port int
}

func newMCPCommand() *cobra.Command {
	var transport string
	var port int
	var targetURL string
	var apiKey string
	var apiKeyFile string
	var outputRoot string
	var maxOutputEntries int
	var maxOutputBytes int64
	var root string
	var workers int
	var metricNameMapPath string
	var httpTokenFile string
	var allowInsecureHTTP bool
	maxOutputEntriesDefault, maxOutputEntriesEnvironmentError := positiveEnvironmentIntDefault(
		"SIGNOZ_MCP_MAX_OUTPUT_ENTRIES",
		mcpserver.DefaultMaxOutputEntries,
	)
	maxOutputBytesDefault, maxOutputBytesEnvironmentError := positiveEnvironmentInt64Default(
		"SIGNOZ_MCP_MAX_OUTPUT_BYTES",
		mcpserver.DefaultMaxOutputBytes,
	)
	command := &cobra.Command{
		Use:   "mcp",
		Short: "Serve migration tools over the Model Context Protocol",
		RunE: func(command *cobra.Command, _ []string) error {
			if maxOutputEntriesEnvironmentError != nil {
				return cliInputError(maxOutputEntriesEnvironmentError)
			}
			if maxOutputBytesEnvironmentError != nil {
				return cliInputError(maxOutputBytesEnvironmentError)
			}
			transport = strings.ToLower(strings.TrimSpace(transport))
			if transport != "stdio" && transport != "http" {
				return cliInputError(fmt.Errorf("--transport must be stdio or http"))
			}
			if port < 1 || port > 65535 {
				return cliInputError(fmt.Errorf("--port must be between 1 and 65535"))
			}
			if maxOutputEntries <= 0 {
				return cliInputError(fmt.Errorf("--max-output-entries must be greater than zero"))
			}
			if maxOutputBytes <= 0 {
				return cliInputError(fmt.Errorf("--max-output-bytes must be greater than zero"))
			}
			if workers <= 0 {
				return cliInputError(fmt.Errorf("--workers must be greater than zero"))
			}
			if workers > mcpserver.MaxValidationWorkers {
				return cliInputError(fmt.Errorf("--workers must not exceed %d", mcpserver.MaxValidationWorkers))
			}
			resolvedAPIKey, err := resolveAPIKey(apiKey, apiKeyFile)
			if err != nil {
				return err
			}
			if (targetURL == "") != (resolvedAPIKey == "") {
				return cliInputError(fmt.Errorf("SIGNOZ_URL and SIGNOZ_API_KEY must be configured together"))
			}
			metricNames := map[string]string(nil)
			if metricNameMapPath != "" {
				metricNames, err = metricmap.Load(metricNameMapPath)
				if err != nil {
					return &app.Error{Kind: app.ErrorInput, Err: err}
				}
			}
			service, err := mcpserver.New(mcpserver.Config{
				TargetURL: targetURL, APIKey: resolvedAPIKey, OutputRoot: outputRoot, Root: root, Workers: workers, MetricNameMap: metricNames,
				AllowInsecureHTTP: allowInsecureHTTP,
				MaxOutputEntries:  maxOutputEntries, MaxOutputBytes: maxOutputBytes,
			})
			if err != nil {
				if mcpserver.IsConfigError(err) {
					return cliInputError(err)
				}
				return err
			}
			if transport == "stdio" {
				stdio := server.NewStdioServer(service.Server())
				return stdio.Listen(command.Context(), os.Stdin, os.Stdout)
			}
			httpToken, generated, err := resolveMCPHTTPToken(httpTokenFile)
			if err != nil {
				return err
			}
			if generated {
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "Generated MCP HTTP bearer token (shown once): %s\n", httpToken)
			}
			return serveMCPHTTP(command.Context(), service.Server(), port, httpToken)
		},
	}
	command.Flags().StringVar(&transport, "transport", environmentDefault("TRANSPORT_MODE", "stdio"), "transport: stdio or http")
	command.Flags().IntVar(&port, "port", environmentIntDefault("MCP_SERVER_PORT", 8000), "loopback HTTP port")
	command.Flags().StringVar(&targetURL, "target", environmentDefault("SIGNOZ_URL", ""), "SigNoz base URL used by live tools")
	command.Flags().StringVar(&apiKey, "api-key", "", "SigNoz API key (prefer SIGNOZ_API_KEY)")
	command.Flags().StringVar(&apiKeyFile, "api-key-file", "", "file containing the SigNoz API key")
	command.Flags().StringVar(&outputRoot, "out", environmentDefault("PROMCAST_OUT", "out"), "migration state and artifact root")
	command.Flags().IntVar(&maxOutputEntries, "max-output-entries", maxOutputEntriesDefault, "maximum retained files and directories in the MCP output root")
	command.Flags().Int64Var(&maxOutputBytes, "max-output-bytes", maxOutputBytesDefault, "maximum retained logical bytes in the MCP output root")
	command.Flags().StringVar(&root, "root", ".", "filesystem root available to MCP tools")
	command.Flags().IntVar(&workers, "workers", 4, "maximum concurrent target validation requests")
	command.Flags().StringVar(&metricNameMapPath, "metric-name-map", "", "YAML mapping of source metric names to target metric names")
	command.Flags().StringVar(&httpTokenFile, "http-token-file", "", "file containing the bearer token required by the HTTP transport")
	command.Flags().BoolVar(&allowInsecureHTTP, "allow-insecure-http", false, "explicitly allow SigNoz credentials over non-loopback plaintext HTTP")
	return command
}

func serveMCPHTTP(ctx context.Context, mcpServer *server.MCPServer, port int, authToken string) error {
	address := "127.0.0.1:" + strconv.Itoa(port)
	httpServer := newMCPHTTPServer(address)
	transport := newMCPStreamableHTTPServer(mcpServer, httpServer)
	httpServer.Handler = newMCPHTTPHandler(transport, port, authToken)

	errors := make(chan error, 1)
	go func() {
		errors <- transport.Start(address)
	}()
	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return transport.Shutdown(shutdownContext)
	case err := <-errors:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func newMCPStreamableHTTPServer(mcpServer *server.MCPServer, httpServer *http.Server) *server.StreamableHTTPServer {
	return server.NewStreamableHTTPServer(
		mcpServer,
		server.WithEndpointPath("/mcp"),
		server.WithStateLess(true),
		server.WithDisableStreaming(true),
		server.WithStreamableHTTPServer(httpServer),
	)
}

func newMCPHTTPServer(address string) *http.Server {
	return &http.Server{
		Addr:              address,
		ReadHeaderTimeout: mcpHTTPReadHeaderTimeout,
		ReadTimeout:       mcpHTTPReadTimeout,
		IdleTimeout:       mcpHTTPIdleTimeout,
		MaxHeaderBytes:    maxMCPHTTPHeaderBytes,
	}
}

func newMCPHTTPHandler(transport http.Handler, port int, authToken string) http.Handler {
	return newMCPHTTPHandlerWithLimits(transport, port, authToken, mcpHTTPLimits{
		MaxBodyBytes:  maxMCPHTTPRequestBytes,
		MaxConcurrent: maxConcurrentMCPHTTPRequests,
		BodyBudget:    processMCPHTTPBodyBudget,
	})
}

func newMCPHTTPHandlerWithLimits(transport http.Handler, port int, authToken string, limits mcpHTTPLimits) http.Handler {
	if limits.MaxBodyBytes <= 0 {
		limits.MaxBodyBytes = maxMCPHTTPRequestBytes
	}
	if limits.MaxConcurrent <= 0 {
		limits.MaxConcurrent = maxConcurrentMCPHTTPRequests
	}
	if limits.BodyBudget == nil {
		limits.BodyBudget = processMCPHTTPBodyBudget
	}
	guard := &mcpHTTPGuard{
		next: transport, expectedPort: port, maxBodyBytes: limits.MaxBodyBytes,
		authToken:  authToken,
		semaphore:  make(chan struct{}, limits.MaxConcurrent),
		bodyBudget: limits.BodyBudget,
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", guard)
	mux.HandleFunc("/livez", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "ok\n")
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "ready\n")
	})
	return requireLoopbackHost(mux, port)
}

func requireLoopbackHost(next http.Handler, expectedPort int) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if _, err := parseLoopbackAuthority(request.Host, expectedPort); err != nil {
			http.Error(writer, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (guard *mcpHTTPGuard) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if !safeMCPOrigin(request, guard.expectedPort) {
		http.Error(writer, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	if !validMCPHTTPAuthorization(request.Header.Values("Authorization"), guard.authToken) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("WWW-Authenticate", `Bearer realm="promcast-mcp"`)
		http.Error(writer, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	request.Header.Del("Authorization")
	if request.ContentLength > guard.maxBodyBytes {
		writer.Header().Set("Connection", "close")
		_ = request.Body.Close()
		http.Error(writer, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
		return
	}
	if request.Body != nil && request.Body != http.NoBody {
		request.Body = http.MaxBytesReader(writer, request.Body, guard.maxBodyBytes)
	}
	select {
	case guard.semaphore <- struct{}{}:
		defer func() { <-guard.semaphore }()
	case <-request.Context().Done():
		return
	default:
		rejectMCPHTTPOverload(writer, request)
		return
	}
	bodyWeight := guard.requestBodyWeight(request)
	releaseBodyBudget, acquired := guard.bodyBudget.tryAcquire(bodyWeight)
	if !acquired {
		rejectMCPHTTPOverload(writer, request)
		return
	}
	defer releaseBodyBudget()
	guard.next.ServeHTTP(writer, request)
}

func (guard *mcpHTTPGuard) requestBodyWeight(request *http.Request) int64 {
	if request.Body == nil || request.Body == http.NoBody {
		return 0
	}
	if request.ContentLength > 0 {
		return request.ContentLength
	}
	// A chunked body, or a non-NoBody request whose length is otherwise
	// unknown, can consume the complete per-request allowance.
	return guard.maxBodyBytes
}

func rejectMCPHTTPOverload(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Retry-After", "1")
	writer.Header().Set("Connection", "close")
	if request.Body != nil {
		_ = request.Body.Close()
	}
	http.Error(writer, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
}

func validMCPHTTPAuthorization(values []string, expectedToken string) bool {
	if expectedToken == "" || len(values) != 1 {
		return false
	}
	scheme, token, found := strings.Cut(strings.TrimSpace(values[0]), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.TrimSpace(token) != token {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(expectedToken)) == 1
}

func resolveMCPHTTPToken(path string) (string, bool, error) {
	if path != "" {
		token, err := readBoundedSecretFile(path)
		if err != nil {
			return "", false, cliInputError(fmt.Errorf("read MCP HTTP token file %q: %w", path, err))
		}
		if err := validateMCPHTTPToken(token); err != nil {
			return "", false, cliInputError(fmt.Errorf("invalid MCP HTTP token file: %w", err))
		}
		return token, false, nil
	}
	if token := strings.TrimSpace(os.Getenv("SIGNOZ_MCP_HTTP_TOKEN")); token != "" {
		if err := validateMCPHTTPToken(token); err != nil {
			return "", false, cliInputError(fmt.Errorf("invalid SIGNOZ_MCP_HTTP_TOKEN: %w", err))
		}
		return token, false, nil
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", false, fmt.Errorf("generate MCP HTTP token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(random), true, nil
}

func validateMCPHTTPToken(token string) error {
	if len(token) < minimumMCPHTTPTokenLength {
		return fmt.Errorf("token must be at least %d characters", minimumMCPHTTPTokenLength)
	}
	for _, character := range token {
		if character <= ' ' || character == 0x7f {
			return fmt.Errorf("token must not contain whitespace or control characters")
		}
	}
	return nil
}

func safeMCPOrigin(request *http.Request, expectedPort int) bool {
	values := request.Header.Values("Origin")
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 {
		return false
	}
	origin, err := url.Parse(strings.TrimSpace(values[0]))
	if err != nil || origin.Scheme != "http" || origin.Host == "" || origin.User != nil || origin.Opaque != "" ||
		origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	requestAuthority, err := parseLoopbackAuthority(request.Host, expectedPort)
	if err != nil {
		return false
	}
	originAuthority, err := parseLoopbackAuthority(origin.Host, expectedPort)
	return err == nil && originAuthority == requestAuthority
}

func parseLoopbackAuthority(value string, expectedPort int) (loopbackAuthority, error) {
	if expectedPort < 1 || expectedPort > 65535 || strings.TrimSpace(value) != value || value == "" {
		return loopbackAuthority{}, fmt.Errorf("invalid loopback authority")
	}
	host, portValue, err := net.SplitHostPort(value)
	if err != nil {
		if expectedPort != 80 || strings.Contains(value, ":") {
			return loopbackAuthority{}, fmt.Errorf("loopback authority must include the configured port")
		}
		host, portValue = value, "80"
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port != expectedPort {
		return loopbackAuthority{}, fmt.Errorf("loopback authority uses a different port")
	}
	host = strings.ToLower(host)
	if host == "localhost" {
		return loopbackAuthority{host: host, port: port}, nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return loopbackAuthority{}, fmt.Errorf("authority host is not a loopback literal")
	}
	return loopbackAuthority{host: ip.String(), port: port}, nil
}

func environmentDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func environmentIntDefault(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err == nil && value > 0 {
		return value
	}
	return fallback
}

func positiveEnvironmentIntDefault(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback, fmt.Errorf("%s must be a positive base-10 integer within the platform integer range", name)
	}
	if value <= 0 {
		return fallback, fmt.Errorf("%s must be greater than zero", name)
	}
	return value, nil
}

func positiveEnvironmentInt64Default(name string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback, fmt.Errorf("%s must be a positive base-10 integer within the signed 64-bit range", name)
	}
	if value <= 0 {
		return fallback, fmt.Errorf("%s must be greater than zero", name)
	}
	return value, nil
}
