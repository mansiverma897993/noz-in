package signoz

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// QueryExecutionResult summarizes the target data returned for one named query.
type QueryExecutionResult struct {
	Series int
	Points int
	Rows   int
}

// MetricPoint is one SigNoz metric sample.
type MetricPoint struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
	Partial   bool    `json:"partial,omitempty"`
}

// UnmarshalJSON accepts SigNoz's numeric samples and its string representation
// for Prometheus non-finite values such as NaN and +/-Inf.
func (point *MetricPoint) UnmarshalJSON(data []byte) error {
	var raw struct {
		Timestamp int64           `json:"timestamp"`
		Value     json.RawMessage `json:"value"`
		Partial   bool            `json:"partial"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	value, err := decodeMetricValue(raw.Value)
	if err != nil {
		return fmt.Errorf("decode metric value: %w", err)
	}
	point.Timestamp = raw.Timestamp
	point.Value = value
	point.Partial = raw.Partial
	return nil
}

func decodeMetricValue(raw json.RawMessage) (float64, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strconv.ParseFloat(text, 64)
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, err
	}
	return value, nil
}

// MetricSeries is one named v5 query result series.
type MetricSeries struct {
	Labels map[string]string `json:"labels"`
	Values []MetricPoint     `json:"values"`
}

// HasData reports whether the query returned at least one point or scalar/table row.
func (result QueryExecutionResult) HasData() bool {
	return result.Points > 0 || result.Rows > 0
}

type queryRangeResponse struct {
	Status string                  `json:"status"`
	Error  json.RawMessage         `json:"error"`
	Data   *queryRangeResponseData `json:"data"`
}

type queryRangeResponseData struct {
	Data *queryRangeResultData `json:"data"`
}

type queryRangeResultData struct {
	Results *[]queryResult `json:"results"`
}

type queryResult struct {
	QueryName    string             `json:"queryName"`
	Aggregations []queryAggregation `json:"aggregations"`
	Series       []querySeries      `json:"series"`
	Data         []json.RawMessage  `json:"data"`
}

type queryAggregation struct {
	Series []querySeries `json:"series"`
}

type querySeries struct {
	Labels []queryLabel  `json:"labels"`
	Values []MetricPoint `json:"values"`
}

type queryLabel struct {
	Key struct {
		Name string `json:"name"`
	} `json:"key"`
	Value string `json:"value"`
}

// QueryRange executes a strict v5 request and summarizes its named results.
func (client *Client) QueryRange(ctx context.Context, request QueryRangeRequest) (map[string]QueryExecutionResult, error) {
	response, err := client.executeQueryRange(ctx, request)
	if err != nil {
		return nil, err
	}

	rawResults := *response.Data.Data.Results
	results := make(map[string]QueryExecutionResult, len(rawResults))
	for _, raw := range rawResults {
		if raw.QueryName == "" {
			continue
		}
		result := results[raw.QueryName]
		result.Rows += len(raw.Data)
		result.Series += len(raw.Series)
		for _, series := range raw.Series {
			result.Points += len(series.Values)
		}
		for _, aggregation := range raw.Aggregations {
			result.Series += len(aggregation.Series)
			for _, series := range aggregation.Series {
				result.Points += len(series.Values)
			}
		}
		results[raw.QueryName] = result
	}
	return results, nil
}

// QueryRangeSeries executes a strict v5 request and returns metric samples by query name.
func (client *Client) QueryRangeSeries(ctx context.Context, request QueryRangeRequest) (map[string][]MetricSeries, error) {
	response, err := client.executeQueryRange(ctx, request)
	if err != nil {
		return nil, err
	}

	rawResults := *response.Data.Data.Results
	results := make(map[string][]MetricSeries, len(rawResults))
	for _, raw := range rawResults {
		if raw.QueryName == "" {
			continue
		}
		for _, series := range raw.Series {
			results[raw.QueryName] = append(results[raw.QueryName], exportMetricSeries(series))
		}
		for _, aggregation := range raw.Aggregations {
			for _, series := range aggregation.Series {
				results[raw.QueryName] = append(results[raw.QueryName], exportMetricSeries(series))
			}
		}
	}
	return results, nil
}

func (client *Client) executeQueryRange(ctx context.Context, request QueryRangeRequest) (queryRangeResponse, error) {
	var response queryRangeResponse
	if err := client.do(ctx, http.MethodPost, "/api/v5/query_range", nil, request, &response); err != nil {
		return queryRangeResponse{}, err
	}
	if response.Status != "success" {
		return queryRangeResponse{}, fmt.Errorf("SigNoz query failed with status %q: %s", response.Status, compactJSON(response.Error))
	}
	if response.Data == nil || response.Data.Data == nil || response.Data.Data.Results == nil {
		return queryRangeResponse{}, fmt.Errorf("SigNoz query success response is missing data.data.results")
	}
	return response, nil
}

func exportMetricSeries(series querySeries) MetricSeries {
	labels := make(map[string]string, len(series.Labels))
	for _, label := range series.Labels {
		if label.Key.Name != "" {
			labels[label.Key.Name] = label.Value
		}
	}
	return MetricSeries{Labels: labels, Values: append([]MetricPoint(nil), series.Values...)}
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "no error detail"
	}
	var message string
	if json.Unmarshal(raw, &message) == nil {
		return message
	}
	return string(raw)
}
