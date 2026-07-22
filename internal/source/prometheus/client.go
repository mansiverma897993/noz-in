package prometheus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/mansiverma897993/noz-in/internal/httpdetail"
	"github.com/mansiverma897993/noz-in/internal/transportpolicy"
)

const maxQueryResponseSize = 32 << 20

// QueryPoint is one Prometheus sample, with its timestamp normalized to milliseconds.
type QueryPoint struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
}

// QuerySeries is one Prometheus matrix series.
type QuerySeries struct {
	Labels map[string]string `json:"labels"`
	Values []QueryPoint      `json:"values"`
}

// Client calls the Prometheus HTTP query API.
type Client struct {
	baseURL     *url.URL
	bearerToken string
	httpClient  *http.Client
}

// NewClient constructs a Prometheus client.
func NewClient(baseURL, bearerToken string, httpClient *http.Client) (*Client, error) {
	return NewClientWithOptions(baseURL, bearerToken, httpClient, ClientOptions{})
}

// ClientOptions contains explicit transport-risk acknowledgements.
type ClientOptions struct {
	AllowInsecureHTTP bool
}

// NewClientWithOptions constructs a Prometheus client with explicit policy.
func NewClientWithOptions(baseURL, bearerToken string, httpClient *http.Client, options ClientOptions) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, errors.New("parse Prometheus URL: invalid URL")
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("prometheus URL must be an absolute HTTP or HTTPS URL")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("prometheus URL must not contain user information; use the bearer-token option")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("prometheus URL must not contain a query or fragment")
	}
	bearerToken = strings.TrimSpace(bearerToken)
	if containsHeaderControl(bearerToken) {
		return nil, errors.New("prometheus bearer token contains invalid control characters")
	}
	if err := transportpolicy.RequireProtectedCredentials(parsed, bearerToken != "", options.AllowInsecureHTTP, "Prometheus"); err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	} else {
		clone := *httpClient
		httpClient = &clone
	}
	// API endpoints must be configured at their final origin. Refusing redirects
	// prevents a bearer token from following an unexpected proxy response.
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: parsed, bearerToken: bearerToken, httpClient: httpClient}, nil
}

func containsHeaderControl(value string) bool {
	return strings.IndexFunc(value, func(character rune) bool {
		return character < ' ' || character == 0x7f
	}) >= 0
}

// QueryRange evaluates one expression as a range query.
func (client *Client) QueryRange(
	ctx context.Context,
	expression string,
	start time.Time,
	end time.Time,
	step time.Duration,
) ([]QuerySeries, error) {
	if strings.TrimSpace(expression) == "" {
		return nil, fmt.Errorf("prometheus query must not be empty")
	}
	if !end.After(start) {
		return nil, fmt.Errorf("prometheus query end must be after start")
	}
	if step <= 0 {
		return nil, fmt.Errorf("prometheus query step must be positive")
	}

	query := make(url.Values)
	query.Set("query", expression)
	query.Set("start", formatPrometheusTime(start))
	query.Set("end", formatPrometheusTime(end))
	query.Set("step", formatPrometheusStep(step))
	endpoint := *client.baseURL
	endpoint.Path = path.Join(endpoint.Path, "/api/v1/query_range")
	endpoint.RawPath = ""
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create Prometheus query request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if client.bearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+client.bearerToken)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call Prometheus query API: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxQueryResponseSize+1))
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, errors.Join(fmt.Errorf("read Prometheus query response: %w", readErr), closeErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close Prometheus query response: %w", closeErr)
	}
	if len(data) > maxQueryResponseSize {
		return nil, fmt.Errorf("prometheus query response exceeds %d bytes", maxQueryResponseSize)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		detail := httpdetail.Bytes(data)
		if detail == "" {
			detail = http.StatusText(response.StatusCode)
		}
		return nil, fmt.Errorf("prometheus query API returned HTTP %d: %s", response.StatusCode, detail)
	}

	var envelope queryRangeEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode Prometheus query response: %w", err)
	}
	if envelope.Status != "success" {
		errorType := httpdetail.Text(envelope.ErrorType)
		if errorType == "" || len(errorType) > 256 {
			errorType = "unknown"
		}
		detail := httpdetail.Text(envelope.Error)
		if detail == "" {
			detail = "upstream returned an error without detail"
		}
		return nil, fmt.Errorf("prometheus query failed (%s): %s", errorType, detail)
	}
	if envelope.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("prometheus range query returned result type %q, want matrix", envelope.Data.ResultType)
	}
	return decodeQuerySeries(envelope.Data.Result)
}

type queryRangeEnvelope struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

type rawQuerySeries struct {
	Metric map[string]string   `json:"metric"`
	Values [][]json.RawMessage `json:"values"`
}

func decodeQuerySeries(raw json.RawMessage) ([]QuerySeries, error) {
	var input []rawQuerySeries
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("decode Prometheus matrix: %w", err)
	}
	result := make([]QuerySeries, 0, len(input))
	for seriesIndex, series := range input {
		decoded := QuerySeries{Labels: series.Metric, Values: make([]QueryPoint, 0, len(series.Values))}
		for pointIndex, rawPoint := range series.Values {
			if len(rawPoint) != 2 {
				return nil, fmt.Errorf("prometheus matrix series %d point %d has %d fields, want 2", seriesIndex, pointIndex, len(rawPoint))
			}
			timestamp, err := decodeTimestamp(rawPoint[0])
			if err != nil {
				return nil, fmt.Errorf("decode Prometheus matrix series %d point %d timestamp: %w", seriesIndex, pointIndex, err)
			}
			value, err := decodeSample(rawPoint[1])
			if err != nil {
				return nil, fmt.Errorf("decode Prometheus matrix series %d point %d value: %w", seriesIndex, pointIndex, err)
			}
			decoded.Values = append(decoded.Values, QueryPoint{Timestamp: timestamp, Value: value})
		}
		result = append(result, decoded)
	}
	return result, nil
}

func decodeTimestamp(raw json.RawMessage) (int64, error) {
	var seconds float64
	if err := json.Unmarshal(raw, &seconds); err != nil {
		return 0, err
	}
	return int64(math.Round(seconds * 1000)), nil
}

func decodeSample(raw json.RawMessage) (float64, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		value, parseErr := strconv.ParseFloat(text, 64)
		if parseErr != nil {
			return 0, parseErr
		}
		return value, nil
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, err
	}
	return value, nil
}

func formatPrometheusTime(value time.Time) string {
	return strconv.FormatFloat(float64(value.UnixMilli())/1000, 'f', 3, 64)
}

func formatPrometheusStep(value time.Duration) string {
	return strconv.FormatFloat(value.Seconds(), 'f', -1, 64)
}
