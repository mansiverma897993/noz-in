package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mansiverma897993/signoz/internal/safeoutput"
	"github.com/mansiverma897993/signoz/internal/version"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const instructions = "promcast translates Grafana dashboards and Prometheus rules into SigNoz artifacts. Migrate a dashboard, inspect every needs-review verdict, then re-run live validation after fixing inputs or ingesting missing telemetry. This server never hides unsupported semantics."

// MaxValidationWorkers bounds concurrent requests made by MCP live-validation
// tools so caller-provided configuration cannot bypass the process limit.
const MaxValidationWorkers = 4

type configError struct {
	err error
}

func (err *configError) Error() string {
	return err.err.Error()
}

func (err *configError) Unwrap() error {
	return err.err
}

func configErrorf(format string, arguments ...any) error {
	return &configError{err: fmt.Errorf(format, arguments...)}
}

// IsConfigError reports whether err represents invalid process-level MCP
// configuration. It lets a CLI classify configuration errors as invalid input
// without mistaking operational filesystem or network failures for user input.
func IsConfigError(err error) bool {
	var target *configError
	return errors.As(err, &target)
}

// Config supplies process-level MCP settings. Credentials never enter tool arguments.
type Config struct {
	TargetURL         string
	APIKey            string
	OutputRoot        string
	MaxOutputEntries  int
	MaxOutputBytes    int64
	Root              string
	HTTPClient        *http.Client
	AllowInsecureHTTP bool
	Workers           int
	MetricNameMap     map[string]string
	Now               func() time.Time
}

// Service owns the deterministic MCP tool handlers.
type Service struct {
	config           Config
	server           *server.MCPServer
	inputRootInfo    os.FileInfo
	outputRootInfo   os.FileInfo
	outputQuota      *outputQuota
	toolSlot         chan struct{}
	crashBarrier     func(string)
	publicationFault func(string) error
}

// New constructs the MCP service and registers its tools and resources.
func New(config Config) (*Service, error) {
	if config.Workers < 0 {
		return nil, configErrorf("MCP workers must be zero (default) or between 1 and %d", MaxValidationWorkers)
	}
	if config.Workers == 0 {
		config.Workers = MaxValidationWorkers
	}
	if config.Workers > MaxValidationWorkers {
		return nil, configErrorf("MCP workers must not exceed %d", MaxValidationWorkers)
	}
	outputQuota, err := newOutputQuota(config.MaxOutputEntries, config.MaxOutputBytes)
	if err != nil {
		return nil, err
	}
	config.MaxOutputEntries = int(outputQuota.maxEntries)
	config.MaxOutputBytes = outputQuota.maxBytes

	if config.Root == "" {
		config.Root = "."
	}
	root, err := filepath.Abs(config.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve MCP root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve MCP root symlinks: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect MCP root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("MCP root must be a directory")
	}
	config.Root = filepath.Clean(root)
	if config.OutputRoot == "" {
		config.OutputRoot = "out"
	}
	if !filepath.IsAbs(config.OutputRoot) {
		config.OutputRoot = filepath.Join(config.Root, config.OutputRoot)
	}
	config.OutputRoot = filepath.Clean(config.OutputRoot)
	pinnedOutput, err := safeoutput.OpenOrCreateDirectory(config.OutputRoot, 0o700)
	if err != nil {
		return nil, fmt.Errorf("create MCP output root: %w", err)
	}
	config.OutputRoot = pinnedOutput.Path()
	outputInfo, err := pinnedOutput.Root().Stat(".")
	if err != nil {
		_ = pinnedOutput.Close()
		return nil, fmt.Errorf("inspect MCP output root: %w", err)
	}
	if !outputInfo.IsDir() {
		_ = pinnedOutput.Close()
		return nil, fmt.Errorf("MCP output root must be a directory")
	}
	if err := pinnedOutput.Close(); err != nil {
		return nil, fmt.Errorf("close pinned MCP output root: %w", err)
	}
	mcpServer := server.NewMCPServer(
		"promcast",
		version.Version(),
		server.WithLogging(),
		server.WithToolCapabilities(false),
		server.WithResourceCapabilities(false, false),
		server.WithInstructions(instructions),
		server.WithRecovery(),
	)
	service := &Service{
		config: config, server: mcpServer,
		inputRootInfo: info, outputRootInfo: outputInfo,
		outputQuota: outputQuota,
		toolSlot:    make(chan struct{}, 1),
	}
	if err := service.recoverOutputWork(); err != nil {
		return nil, fmt.Errorf("recover interrupted MCP output work: %w", err)
	}
	service.registerTools()
	service.registerResources()
	return service, nil
}

func (service *Service) acquireTool(ctx context.Context) (func(), error) {
	select {
	case service.toolSlot <- struct{}{}:
		return func() { <-service.toolSlot }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for MCP tool working-set slot: %w", ctx.Err())
	}
}

// Server returns the configured protocol server.
func (service *Service) Server() *server.MCPServer {
	return service.server
}

func (service *Service) registerTools() {
	service.server.AddTool(mcp.NewTool(
		"migrate_dashboard",
		mcp.WithDescription("Migrate one Grafana dashboard to SigNoz. Supply exactly one of grafana_json, grafana_path, or grafana_id. Every panel and query receives a verdict; set import=true only when the target should be created or updated."),
		mcp.WithString("grafana_json", mcp.Description("Full Grafana dashboard JSON.")),
		mcp.WithString("grafana_path", mcp.Description("Dashboard JSON path inside the configured MCP root.")),
		mcp.WithString("grafana_id", mcp.Description("Numeric grafana.com dashboard ID, for example 1860.")),
		mcp.WithString("source_namespace", mcp.Description("Stable source estate or Grafana organization identifier. Required whenever the MCP server has a SigNoz target configured, including validation-only runs, so generated target IDs match later imports.")),
		mcp.WithString("source_identity", mcp.Description("Stable logical dashboard identity. Required for importing inline dashboards that do not carry a Grafana UID; paths and grafana.com IDs provide a default.")),
		mcp.WithBoolean("import", mcp.DefaultBool(false), mcp.Description("Import into the configured SigNoz target after validation.")),
		mcp.WithString("rate_interval", mcp.DefaultString("5m"), mcp.Description("Literal duration substituted for $__rate_interval.")),
		mcp.WithArray("rules", mcp.Items(map[string]any{"type": "string"}), mcp.Description("Prometheus rule paths used to inline recording rules.")),
		mcp.WithArray("variables", mcp.Items(map[string]any{"type": "string"}), mcp.Description("Dashboard values in name=value form.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	), service.handleMigrateDashboard)

	service.server.AddTool(mcp.NewTool(
		"explain_verdict",
		mcp.WithDescription("Explain a dashboard, variable, panel, or query verdict from a completed migration using its stable source path, deterministic reason codes, emitted query, and live validation evidence."),
		mcp.WithString("migration_id", mcp.Required(), mcp.Description("ID returned by migrate_dashboard.")),
		mcp.WithString("kind", mcp.Description("Stable selector kind: dashboard, variable, panel, or query. Provide together with source_path.")),
		mcp.WithString("source_path", mcp.Description("Exact source_path returned by migrate_dashboard. Provide together with kind.")),
		mcp.WithString("panel", mcp.Description("Legacy selector: exact title, unique title substring, or zero-based panel index.")),
		mcp.WithString("query", mcp.Description("Optional query reference such as A, B, or C when using the legacy panel selector.")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	), service.handleExplainVerdict)

	service.server.AddTool(mcp.NewTool(
		"validate_queries",
		mcp.WithDescription("Re-run live SigNoz metadata, preview, and data checks for a completed migration without importing anything."),
		mcp.WithString("migration_id", mcp.Required(), mcp.Description("ID returned by migrate_dashboard.")),
		mcp.WithString("window", mcp.DefaultString("30m"), mcp.Description("Lookback window such as 30m or 6h.")),
		mcp.WithString("panel", mcp.Description("Optional exact title, unique title substring, or zero-based panel index to validate.")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	), service.handleValidateQueries)
}
