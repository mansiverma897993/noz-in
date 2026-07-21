package signoz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
	"time"

	"github.com/mansiverma897993/signoz/internal/httpdetail"
	"github.com/mansiverma897993/signoz/internal/transportpolicy"
)

const maxResponseSize = 32 << 20

const (
	maxRequestAttempts = 3
	maxRetryDelay      = 5 * time.Second
)

// Client calls the SigNoz HTTP API with a service-account key.
type Client struct {
	baseURL    *url.URL
	apiKey     string
	httpClient *http.Client
}

// APIError describes a non-success response from SigNoz.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (err *APIError) Error() string {
	if err.Code == "" {
		return fmt.Sprintf("SigNoz API returned HTTP %d: %s", err.StatusCode, err.Message)
	}
	return fmt.Sprintf("SigNoz API returned HTTP %d (%s): %s", err.StatusCode, err.Code, err.Message)
}

// MetricMetadata is the target's interpretation of an ingested metric.
type MetricMetadata struct {
	Description string `json:"description"`
	Type        string `json:"type"`
	Unit        string `json:"unit"`
	Temporality string `json:"temporality"`
	IsMonotonic bool   `json:"isMonotonic"`
}

// MetricAttribute describes one dimension present on a target metric.
type MetricAttribute struct {
	Key        string   `json:"key"`
	Values     []string `json:"values"`
	ValueCount int      `json:"valueCount"`
}

// StoredDashboard is a dashboard record returned by the v1 API.
type StoredDashboard struct {
	ID     string      `json:"id"`
	Data   DashboardV5 `json:"data"`
	Locked bool        `json:"locked"`
}

// DashboardWriteResult describes an idempotent dashboard write.
type DashboardWriteResult struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

// NewClient constructs a SigNoz API client.
func NewClient(baseURL, apiKey string, httpClient *http.Client) (*Client, error) {
	return NewClientWithOptions(baseURL, apiKey, httpClient, ClientOptions{})
}

// ClientOptions contains explicit transport-risk acknowledgements.
type ClientOptions struct {
	AllowInsecureHTTP bool
}

// NewClientWithOptions constructs a SigNoz API client with explicit policy.
func NewClientWithOptions(baseURL, apiKey string, httpClient *http.Client, options ClientOptions) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, errors.New("parse SigNoz URL: invalid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("SigNoz URL must be an absolute HTTP or HTTPS URL")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("SigNoz URL must not contain user information")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("SigNoz URL must not contain a query or fragment")
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("SigNoz API key is required")
	}
	if containsHeaderControl(apiKey) {
		return nil, errors.New("SigNoz API key contains invalid control characters")
	}
	if err := transportpolicy.RequireProtectedCredentials(parsed, true, options.AllowInsecureHTTP, "SigNoz"); err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	} else {
		clone := *httpClient
		httpClient = &clone
	}
	// SigNoz API requests carry a custom credential header that net/http would
	// otherwise copy to redirects. Require callers to provide the final origin.
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: parsed, apiKey: apiKey, httpClient: httpClient}, nil
}

func containsHeaderControl(value string) bool {
	return strings.IndexFunc(value, func(character rune) bool {
		return character < ' ' || character == 0x7f
	}) >= 0
}

// MetricMetadata returns target metadata for one metric name.
func (client *Client) MetricMetadata(ctx context.Context, metricName string) (MetricMetadata, error) {
	query := make(url.Values)
	query.Set("metricName", metricName)
	var response struct {
		Data *MetricMetadata `json:"data"`
	}
	if err := client.do(ctx, http.MethodGet, "/api/v2/metrics/metadata", query, nil, &response); err != nil {
		return MetricMetadata{}, err
	}
	if response.Data == nil {
		return MetricMetadata{}, fmt.Errorf("decode SigNoz metric metadata response: missing data")
	}
	response.Data.Type = strings.ToLower(strings.TrimSpace(response.Data.Type))
	if response.Data.Type == "" {
		return MetricMetadata{}, fmt.Errorf("decode SigNoz metric metadata response: missing data.type")
	}
	response.Data.Temporality = strings.ToLower(strings.TrimSpace(response.Data.Temporality))
	return *response.Data, nil
}

// MetricAttributes returns dimensions observed for a metric in a time range.
func (client *Client) MetricAttributes(
	ctx context.Context,
	metricName string,
	start time.Time,
	end time.Time,
) ([]MetricAttribute, error) {
	query := make(url.Values)
	query.Set("metricName", metricName)
	query.Set("start", fmt.Sprintf("%d", start.UnixMilli()))
	query.Set("end", fmt.Sprintf("%d", end.UnixMilli()))
	var response struct {
		Data *struct {
			Attributes *[]MetricAttribute `json:"attributes"`
		} `json:"data"`
	}
	if err := client.do(ctx, http.MethodGet, "/api/v2/metrics/attributes", query, nil, &response); err != nil {
		return nil, err
	}
	if response.Data == nil {
		return nil, fmt.Errorf("decode SigNoz metric attributes response: missing data")
	}
	if response.Data.Attributes == nil {
		return nil, fmt.Errorf("decode SigNoz metric attributes response: missing data.attributes")
	}
	return *response.Data.Attributes, nil
}

// ListDashboards returns dashboards visible to the service account.
func (client *Client) ListDashboards(ctx context.Context) ([]StoredDashboard, error) {
	var response struct {
		Data *[]StoredDashboard `json:"data"`
	}
	if err := client.do(ctx, http.MethodGet, "/api/v1/dashboards", nil, nil, &response); err != nil {
		return nil, err
	}
	if response.Data == nil {
		return nil, fmt.Errorf("decode SigNoz dashboard inventory response: missing data array")
	}
	return *response.Data, nil
}

// UpsertDashboard creates or updates a dashboard using its deterministic UUID.
func (client *Client) UpsertDashboard(ctx context.Context, dashboard DashboardV5) (DashboardWriteResult, error) {
	if strings.TrimSpace(dashboard.UUID) == "" {
		return DashboardWriteResult{}, fmt.Errorf("dashboard UUID is required for idempotent import")
	}
	release, err := acquireUpsertLocks(ctx, client.upsertLockKey("dashboard-uuid", dashboard.UUID))
	if err != nil {
		return DashboardWriteResult{}, err
	}
	defer release()

	return client.upsertDashboardLocked(ctx, dashboard)
}

func (client *Client) upsertDashboardLocked(ctx context.Context, dashboard DashboardV5) (DashboardWriteResult, error) {
	dashboards, err := client.ListDashboards(ctx)
	if err != nil {
		return DashboardWriteResult{}, err
	}
	existing, found, err := dashboardWithUUID(dashboards, dashboard.UUID)
	if err != nil {
		return DashboardWriteResult{}, err
	}
	if found {
		if existing.Locked {
			return DashboardWriteResult{}, fmt.Errorf("dashboard %q is locked", existing.ID)
		}
		return client.updateDashboard(ctx, existing.ID, dashboard)
	}

	var response struct {
		Data StoredDashboard `json:"data"`
	}
	if err := client.do(ctx, http.MethodPost, "/api/v1/dashboards", nil, dashboard, &response); err != nil {
		return client.reconcileDashboardCreate(ctx, dashboard, err)
	}
	if response.Data.ID == "" {
		return client.reconcileDashboardCreate(
			ctx, dashboard, fmt.Errorf("SigNoz create dashboard response did not include an id"),
		)
	}
	return DashboardWriteResult{ID: response.Data.ID, Action: "created"}, nil
}

func (client *Client) updateDashboard(
	ctx context.Context,
	id string,
	dashboard DashboardV5,
) (DashboardWriteResult, error) {
	var response struct {
		Data StoredDashboard `json:"data"`
	}
	path := "/api/v1/dashboards/" + url.PathEscape(id)
	if err := client.do(ctx, http.MethodPut, path, nil, dashboard, &response); err != nil {
		return DashboardWriteResult{}, err
	}
	return DashboardWriteResult{ID: id, Action: "updated"}, nil
}

func (client *Client) reconcileDashboardCreate(
	ctx context.Context,
	dashboard DashboardV5,
	createErr error,
) (DashboardWriteResult, error) {
	dashboards, err := client.ListDashboards(ctx)
	if err != nil {
		return DashboardWriteResult{}, fmt.Errorf("create dashboard: %w; reconcile target inventory: %v", createErr, err)
	}
	existing, found, err := dashboardWithUUID(dashboards, dashboard.UUID)
	if err != nil {
		return DashboardWriteResult{}, errors.Join(createErr, err)
	}
	if !found {
		return DashboardWriteResult{}, createErr
	}
	if existing.Locked {
		return DashboardWriteResult{}, errors.Join(createErr, fmt.Errorf("dashboard %q is locked", existing.ID))
	}
	return client.updateDashboard(ctx, existing.ID, dashboard)
}

func dashboardWithUUID(dashboards []StoredDashboard, uuid string) (StoredDashboard, bool, error) {
	var match StoredDashboard
	found := false
	for _, dashboard := range dashboards {
		if dashboard.Data.UUID != uuid {
			continue
		}
		if strings.TrimSpace(dashboard.ID) == "" {
			return StoredDashboard{}, false, fmt.Errorf("SigNoz dashboard with deterministic UUID %q has no id", uuid)
		}
		if found {
			return StoredDashboard{}, false, fmt.Errorf("multiple SigNoz dashboards use deterministic UUID %q", uuid)
		}
		match = dashboard
		found = true
	}
	return match, found, nil
}

func (client *Client) do(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	body any,
	destination any,
) error {
	endpoint := *client.baseURL
	endpoint.Path = pathpkg.Join(endpoint.Path, path)
	endpoint.RawPath = ""
	if query != nil {
		endpoint.RawQuery = query.Encode()
	}

	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode SigNoz request: %w", err)
		}
	}

	for attempt := range maxRequestAttempts {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(encoded)
		}
		request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
		if err != nil {
			return fmt.Errorf("create SigNoz request: %w", err)
		}
		request.Header.Set("SIGNOZ-API-KEY", client.apiKey)
		request.Header.Set("Accept", "application/json")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}

		response, err := client.httpClient.Do(request)
		if err != nil {
			if attempt+1 < maxRequestAttempts && retrySafe(method, path) {
				if err := waitForRetry(ctx, backoff(attempt)); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("call SigNoz API: %w", err)
		}
		limited := io.LimitReader(response.Body, maxResponseSize+1)
		data, readErr := io.ReadAll(limited)
		closeErr := response.Body.Close()
		if readErr != nil {
			return errors.Join(fmt.Errorf("read SigNoz response: %w", readErr), closeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close SigNoz response: %w", closeErr)
		}
		if len(data) > maxResponseSize {
			return fmt.Errorf("SigNoz response exceeds %d bytes", maxResponseSize)
		}
		if retryableStatus(response.StatusCode) && attempt+1 < maxRequestAttempts && retrySafe(method, path) {
			if err := waitForRetry(ctx, retryDelay(response.Header.Get("Retry-After"), attempt)); err != nil {
				return err
			}
			continue
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return decodeAPIError(response.StatusCode, data)
		}
		if destination == nil || response.StatusCode == http.StatusNoContent || len(bytes.TrimSpace(data)) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, destination); err != nil {
			return fmt.Errorf("decode SigNoz response: %w", err)
		}
		return nil
	}
	return fmt.Errorf("SigNoz request exhausted retry attempts")
}

func retrySafe(method, path string) bool {
	if method == http.MethodGet || method == http.MethodPut || method == http.MethodDelete {
		return true
	}
	return method == http.MethodPost && (path == "/api/v5/query_range" || path == "/api/v5/query_range/preview")
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func retryDelay(header string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && seconds >= 0 {
		return min(time.Duration(seconds)*time.Second, maxRetryDelay)
	}
	if when, err := http.ParseTime(header); err == nil {
		return min(max(time.Until(when), 0), maxRetryDelay)
	}
	return backoff(attempt)
}

func backoff(attempt int) time.Duration {
	return min(100*time.Millisecond*time.Duration(1<<attempt), maxRetryDelay)
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait to retry SigNoz request: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

func decodeAPIError(statusCode int, data []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil && envelope.Error.Message != "" {
		code := httpdetail.Text(envelope.Error.Code)
		if len(code) > 256 {
			code = "invalid_error_code"
		}
		message := httpdetail.Text(envelope.Error.Message)
		if message == "" {
			message = http.StatusText(statusCode)
		}
		return &APIError{StatusCode: statusCode, Code: code, Message: message}
	}
	message := httpdetail.Bytes(data)
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return &APIError{StatusCode: statusCode, Message: message}
}

// IsNotFound reports whether an error came from a 404 response.
func IsNotFound(err error) bool {
	var apiError *APIError
	return errors.As(err, &apiError) && apiError.StatusCode == http.StatusNotFound
}
